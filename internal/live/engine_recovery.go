package live

import (
	"context"
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
)

// recoverFromDB loads active orders from the DB and reconciles OMS state with the exchange.
// Returns true if recovery was performed (caller should skip clean-slate cancel).
// Returns false if recovery is not possible (no DB, no active orders, or exchange unsupported).
func (e *Engine) recoverFromDB(ctx context.Context, symbol string) bool {
	activeOrders, err := e.cfg.Store.GetActiveOrders(ctx, e.cfg.UserID, e.cfg.StrategyID)
	if err != nil {
		e.log.Warn("DB recovery: failed to query active orders (falling back to clean-slate)",
			zap.Error(err))
		return false
	}
	if len(activeOrders) == 0 {
		e.log.Info("DB recovery: no active orders in DB, will run clean-slate cancel for exchange orphans")
		return false // run clean-slate to cancel any orphan orders on exchange
	}

	sc, hasChecker := e.broker.orderClient.(exchange.OrderStatusChecker)
	if !hasChecker {
		// Exchange doesn't support order status queries (Spot) — fall back to clean-slate
		e.log.Info("DB recovery: exchange does not support OrderStatusChecker, falling back to clean-slate",
			zap.Int("active_orders", len(activeOrders)))
		return false
	}

	e.log.Info("DB recovery: reconciling active orders with exchange",
		zap.Int("count", len(activeOrders)))

	for _, rec := range activeOrders {
		ord := &oms.Order{
			ID:            rec.ID,
			ClientOrderID: rec.ClientOrderID,
			ExchangeID:    rec.ExchangeID,
			Symbol:        rec.Symbol,
			Side:          strategy.Side(rec.Side),
			PositionSide:  strategy.PositionSide(rec.PositionSide),
			Type:          strategy.OrderType(rec.Type),
			Status:        oms.OrderStatus(rec.Status),
			Mode:          oms.ModeLive,
			StrategyID:    rec.StrategyID,
			Qty:           rec.Quantity,
			Price:         rec.Price,
			StopPrice:     rec.StopPrice,
			FilledQty:     rec.FilledQuantity,
			AvgFillPrice:  rec.AvgFillPrice,
			Commission:    rec.Commission,
			RejectReason:  rec.RejectReason,
			Role:          rec.OrderRole,
			CreatedAt:     rec.CreatedAt,
			UpdatedAt:     rec.UpdatedAt,
		}

		if ord.ExchangeID == "" {
			// Never reached exchange — skip restoring to OMS entirely.
			// Previously we Restore+Reject, but if DB status was OPEN (not PENDING),
			// the OMS state machine rejects the transition and the order stays OPEN → blocks new orders.
			e.log.Warn("DB recovery: PENDING order never reached exchange, skipping",
				zap.String("order_id", ord.ID), zap.String("db_id", rec.ID))
			// Cancel only THIS specific order in the DB (not all active orders).
			dbCtx, dbCancel := context.WithTimeout(ctx, 10*time.Second)
			if err := e.cfg.Store.CancelOrderByID(dbCtx, rec.ID); err != nil {
				e.log.Warn("DB recovery: cancel order in DB failed", zap.String("db_id", rec.ID), zap.Error(err))
			}
			dbCancel()
			continue
		}

		// Query exchange for current status
		qCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		status, fill, err := sc.GetOrderStatus(qCtx, ord.Symbol, ord.ExchangeID)
		cancel()

		if err != nil {
			// Conservative: cancel the order at exchange level and mark cancelled
			e.log.Warn("DB recovery: failed to query exchange order status, cancelling conservatively",
				zap.String("order_id", ord.ID),
				zap.String("exchange_id", ord.ExchangeID),
				zap.Error(err))
			e.omsInst.Restore(ord) //nolint:errcheck
			if cErr := e.broker.orderClient.CancelOrder(ctx, ord.Symbol, ord.ExchangeID); cErr != nil {
				e.log.Warn("DB recovery: cancel order at exchange failed", zap.Error(cErr))
			}
			e.omsInst.Cancel(ord.ID) //nolint:errcheck
			continue
		}

		switch status {
		case "filled", "FILLED":
			e.log.Info("DB recovery: order was FILLED on exchange, restoring fill",
				zap.String("order_id", ord.ID),
				zap.Float64("filled_qty", fill.FilledQty))
			e.omsInst.Restore(ord) //nolint:errcheck
			// Transition to OPEN first so Fill() is valid
			e.omsInst.Accept(ord.ID)  //nolint:errcheck
			stratFill := strategy.Fill{
				ID:           ord.ID + "-recovered",
				Symbol:       ord.Symbol,
				Side:         ord.Side,
				PositionSide: ord.PositionSide,
				Qty:          fill.FilledQty,
				Price:        fill.AvgPrice,
				Fee:          fill.Fee,
				Timestamp:    ord.UpdatedAt,
			}
			e.omsInst.Fill(ord.ID, stratFill) //nolint:errcheck

		case "canceled", "CANCELED", "cancelled", "CANCELLED", "expired", "EXPIRED":
			e.log.Info("DB recovery: order was CANCELLED/EXPIRED on exchange",
				zap.String("order_id", ord.ID),
				zap.String("status", status))
			e.omsInst.Restore(ord)    //nolint:errcheck
			e.omsInst.Cancel(ord.ID)  //nolint:errcheck

		default:
			// OPEN / PARTIALLY_FILLED / NEW — restore and restart poller
			e.log.Info("DB recovery: order still active on exchange, resuming tracking",
				zap.String("order_id", ord.ID),
				zap.String("status", status))
			e.omsInst.Restore(ord) //nolint:errcheck
			if sc2, ok := e.broker.orderClient.(exchange.OrderStatusChecker); ok && ord.ExchangeID != "" {
				req := strategy.OrderRequest{
					Symbol:       ord.Symbol,
					Side:         ord.Side,
					PositionSide: ord.PositionSide,
					Type:         ord.Type,
					Qty:          ord.Qty,
					Price:        ord.Price,
					StopPrice:    ord.StopPrice,
				}
				go e.broker.pollOrderUntilFilled(e.broker.engineCtx, sc2, ord.ExchangeID, ord.ID, req)
			}
		}
	}

	// Rebuild broker's in-memory protectiveOrders map from DB-persisted stop/TP orders.
	e.broker.RebuildProtectiveOrders(activeOrders)

	return true
}

// claimOrphanOrders queries the exchange for open orders and adopts any not
// already tracked in OMS (e.g. limit orders that survived a crash before the DB
// row was persisted). Adopted orders are restored at StatusOpen and registered
// with the order poller so subsequent fills route through the matched-fill path
// (with TG notification) instead of unmatched-fill.
//
// Called from engine.Run after recoverFromDB. No-op if the exchange does not
// implement exchange.OpenOrdersLister.
func (e *Engine) claimOrphanOrders(ctx context.Context, symbol string) {
	lister, ok := e.broker.orderClient.(exchange.OpenOrdersLister)
	if !ok {
		return
	}
	qCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	openOrders, err := lister.ListOpenOrders(qCtx, symbol)
	if err != nil {
		e.log.Warn("orphan-claim: list exchange open orders failed", zap.Error(err))
		return
	}
	if len(openOrders) == 0 {
		return
	}

	known := make(map[string]bool)
	for _, ord := range e.omsInst.All() {
		if ord.ExchangeID != "" {
			known[ord.ExchangeID] = true
		}
	}

	sc, hasSC := e.broker.orderClient.(exchange.OrderStatusChecker)
	claimed := 0
	for _, exo := range openOrders {
		if known[exo.ExchangeID] {
			continue
		}
		ord := &oms.Order{
			ID:            "ADOPTED-" + exo.ExchangeID,
			ExchangeID:    exo.ExchangeID,
			ClientOrderID: exo.ClientOrderID,
			Symbol:        exo.Symbol,
			Side:          strategy.Side(exo.Side),
			PositionSide:  strategy.PositionSide(exo.PositionSide),
			Type:          strategy.OrderType(exo.Type),
			Status:        oms.StatusOpen,
			Mode:          oms.ModeLive,
			StrategyID:    e.cfg.StrategyID,
			Qty:           exo.Qty,
			Price:         exo.Price,
			StopPrice:     exo.StopPrice,
			FilledQty:     exo.FilledQty,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		}
		if err := e.omsInst.Restore(ord); err != nil {
			e.log.Warn("orphan-claim: OMS restore failed",
				zap.String("exchange_id", exo.ExchangeID), zap.Error(err))
			continue
		}
		e.log.Info("orphan-claim: adopted exchange order",
			zap.String("oms_id", ord.ID),
			zap.String("exchange_id", exo.ExchangeID),
			zap.String("side", exo.Side),
			zap.String("position_side", exo.PositionSide),
			zap.String("type", exo.Type),
			zap.Float64("qty", exo.Qty),
			zap.Float64("price", exo.Price),
			zap.Float64("stop_price", exo.StopPrice),
			zap.Bool("reduce_only", exo.ReduceOnly),
		)
		if hasSC {
			req := strategy.OrderRequest{
				Symbol:       exo.Symbol,
				Side:         strategy.Side(exo.Side),
				PositionSide: strategy.PositionSide(exo.PositionSide),
				Type:         strategy.OrderType(exo.Type),
				Qty:          exo.Qty,
				Price:        exo.Price,
				StopPrice:    exo.StopPrice,
			}
			go e.broker.pollOrderUntilFilled(e.broker.engineCtx, sc, exo.ExchangeID, ord.ID, req)
		}
		claimed++
	}
	if claimed > 0 {
		e.log.Info("orphan-claim: done", zap.Int("count", claimed))
	}
}

// SetPositionSyncer injects the Redis-backed position syncer.
func (e *Engine) SetPositionSyncer(s *position.Syncer) {
	e.posSyncer = s
	// Make syncer available to strategy via Context.Extra
	if e.stratCtx != nil {
		e.stratCtx.Extra["position_syncer"] = s
	}
	// Wire the hard gross-exposure guard onto the broker, fed by the syncer's
	// EXCHANGE-truth position. The live order path otherwise has no position-size
	// check (risk.Check is paper-only), which is how the 31-ETH runaway happened.
	e.broker.SetExposureGuard(func() float64 {
		var g float64
		if lp := s.GetLong(); lp != nil {
			g += lp.Qty
		}
		if sp := s.GetShort(); sp != nil {
			g += sp.Qty
		}
		return g
	}, e.cfg.Leverage, defaultMaxGrossExposureFrac)
}

// SetCachedEquity seeds the exchange equity cache (called at startup).
func (e *Engine) SetCachedEquity(eq float64) {
	e.cachedEquityBits.Store(math.Float64bits(eq))
	e.lastEquityQuery = time.Now()
	e.broker.equity.Store(eq)
}
