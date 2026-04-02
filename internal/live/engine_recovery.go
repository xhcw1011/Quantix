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
		e.log.Info("DB recovery: no active orders found, proceeding normally")
		return true // no clean-slate needed either
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
			// Never reached exchange — treat as rejected
			e.log.Warn("DB recovery: PENDING order never reached exchange, marking rejected",
				zap.String("order_id", ord.ID), zap.String("db_id", rec.ID))
			e.omsInst.Restore(ord) //nolint:errcheck
			e.omsInst.Reject(ord.ID, "recovered: never reached exchange") //nolint:errcheck
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

// SetPositionSyncer injects the Redis-backed position syncer.
func (e *Engine) SetPositionSyncer(s *position.Syncer) {
	e.posSyncer = s
	// Make syncer available to strategy via Context.Extra
	if e.stratCtx != nil {
		e.stratCtx.Extra["position_syncer"] = s
	}
}

// SetCachedEquity seeds the exchange equity cache (called at startup).
func (e *Engine) SetCachedEquity(eq float64) {
	e.cachedEquityBits.Store(math.Float64bits(eq))
	e.lastEquityQuery = time.Now()
	e.broker.equity.Store(eq)
}
