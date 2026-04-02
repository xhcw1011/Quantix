package live

import (
	"context"
	"fmt"
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// placeProtectiveOrders auto-places stop-loss and/or take-profit orders after an entry fill.
// Each protective order is tracked in OMS (with Role set) so it is persisted to DB and
// can be recovered across engine restarts via RebuildProtectiveOrders().
func (b *Broker) placeProtectiveOrders(ctx context.Context, req strategy.OrderRequest, strategyID string, filledQty float64) {
	posSide := string(req.PositionSide)
	key := brokerPosKey(req.Symbol, posSide)

	// The closing side is opposite to the opening side
	closeSide := exchange.OrderSideSell
	if posSide == string(strategy.PositionSideShort) {
		closeSide = exchange.OrderSideBuy
	}

	ids := protectiveIDs{}

	if req.StopLoss > 0 {
		stopReq := strategy.OrderRequest{
			Symbol:       req.Symbol,
			Side:         strategy.Side(closeSide),
			PositionSide: req.PositionSide,
			Type:         strategy.OrderStopMarket,
			Qty:          filledQty,
			StopPrice:    req.StopLoss,
		}
		stopOrd, err := b.omsInst.Submit(stopReq, strategyID)
		if err != nil {
			b.log.Error("OMS submit stop-loss failed", zap.Error(err))
		} else {
			b.omsInst.SetRole(stopOrd.ID, "stop_loss")
			stopID, exchErr := b.placeProtectiveWithRetry(ctx, "stop-loss", func(c context.Context) (string, error) {
				return b.orderClient.PlaceStopMarketOrder(c, req.Symbol, closeSide, posSide, filledQty, req.StopLoss, stopOrd.ClientOrderID)
			}, req.Symbol, req.StopLoss)
			if exchErr != nil {
				b.omsInst.Reject(stopOrd.ID, exchErr.Error()) //nolint:errcheck
				b.alertUnprotectedPosition(req.Symbol, posSide, "stop-loss", req.StopLoss, exchErr)
			} else {
				ids.stopID = stopID
				if err := b.omsInst.SetExchangeID(stopOrd.ID, stopID); err != nil {
					b.log.Warn("SetExchangeID failed for stop-loss", zap.String("order_id", stopOrd.ID), zap.Error(err))
				}
				b.omsInst.Accept(stopOrd.ID) //nolint:errcheck
				b.log.Info("stop-loss order placed",
					zap.String("symbol", req.Symbol),
					zap.String("stop_id", stopID),
					zap.Float64("stop_price", req.StopLoss))
			}
		}
	}

	if req.TakeProfit > 0 {
		tpReq := strategy.OrderRequest{
			Symbol:       req.Symbol,
			Side:         strategy.Side(closeSide),
			PositionSide: req.PositionSide,
			Type:         strategy.OrderType("TAKE_PROFIT_MARKET"),
			Qty:          filledQty,
			StopPrice:    req.TakeProfit,
		}
		tpOrd, err := b.omsInst.Submit(tpReq, strategyID)
		if err != nil {
			b.log.Error("OMS submit take-profit failed", zap.Error(err))
		} else {
			b.omsInst.SetRole(tpOrd.ID, "take_profit")
			tpID, exchErr := b.placeProtectiveWithRetry(ctx, "take-profit", func(c context.Context) (string, error) {
				return b.orderClient.PlaceTakeProfitMarketOrder(c, req.Symbol, closeSide, posSide, filledQty, req.TakeProfit, tpOrd.ClientOrderID)
			}, req.Symbol, req.TakeProfit)
			if exchErr != nil {
				b.omsInst.Reject(tpOrd.ID, exchErr.Error()) //nolint:errcheck
				b.alertUnprotectedPosition(req.Symbol, posSide, "take-profit", req.TakeProfit, exchErr)
			} else {
				ids.tpID = tpID
				if err := b.omsInst.SetExchangeID(tpOrd.ID, tpID); err != nil {
					b.log.Warn("SetExchangeID failed for take-profit", zap.String("order_id", tpOrd.ID), zap.Error(err))
				}
				b.omsInst.Accept(tpOrd.ID) //nolint:errcheck
				b.log.Info("take-profit order placed",
					zap.String("symbol", req.Symbol),
					zap.String("tp_id", tpID),
					zap.Float64("tp_price", req.TakeProfit))
			}
		}
	}

	if ids.stopID != "" || ids.tpID != "" {
		b.protMu.Lock()
		b.protectiveOrders[key] = ids
		b.protMu.Unlock()
	}
}

// RebuildProtectiveOrders reconstructs the in-memory protectiveOrders map from
// DB-persisted active orders after an engine restart. Should be called after recoverFromDB().
func (b *Broker) RebuildProtectiveOrders(orders []*data.OrderRecord) {
	b.protMu.Lock()
	defer b.protMu.Unlock()
	rebuilt := 0
	for _, rec := range orders {
		if rec.OrderRole != "stop_loss" && rec.OrderRole != "take_profit" {
			continue
		}
		if rec.ExchangeID == "" {
			continue // no exchange ID to track
		}
		key := brokerPosKey(rec.Symbol, rec.PositionSide)
		ids := b.protectiveOrders[key]
		if rec.OrderRole == "stop_loss" {
			ids.stopID = rec.ExchangeID
		} else if rec.OrderRole == "staged_tp" {
			ids.tpIDs = append(ids.tpIDs, rec.ExchangeID)
		} else {
			ids.tpID = rec.ExchangeID
		}
		b.protectiveOrders[key] = ids
		rebuilt++
	}
	if rebuilt > 0 {
		b.log.Info("rebuilt protective orders from DB", zap.Int("count", rebuilt))
	}
}

// cancelProtectiveOrders cancels any outstanding stop-loss / take-profit orders for the position.
func (b *Broker) cancelProtectiveOrders(ctx context.Context, symbol, posSide string) {
	key := brokerPosKey(symbol, posSide)

	b.protMu.Lock()
	ids, ok := b.protectiveOrders[key]
	if ok {
		delete(b.protectiveOrders, key)
	}
	b.protMu.Unlock()

	if !ok {
		return
	}

	if ids.stopID != "" {
		if err := b.orderClient.CancelOrder(ctx, symbol, ids.stopID); err != nil {
			b.log.Warn("cancel stop-loss order failed",
				zap.String("symbol", symbol),
				zap.String("stop_id", ids.stopID),
				zap.Error(err))
		} else {
			b.log.Info("stop-loss order cancelled", zap.String("stop_id", ids.stopID))
		}
	}
	if ids.tpID != "" {
		if err := b.orderClient.CancelOrder(ctx, symbol, ids.tpID); err != nil {
			b.log.Warn("cancel take-profit order failed",
				zap.String("symbol", symbol),
				zap.String("tp_id", ids.tpID),
				zap.Error(err))
		} else {
			b.log.Info("take-profit order cancelled", zap.String("tp_id", ids.tpID))
		}
	}
	for _, tpID := range ids.tpIDs {
		if err := b.orderClient.CancelOrder(ctx, symbol, tpID); err != nil {
			b.log.Warn("cancel staged TP order failed",
				zap.String("symbol", symbol),
				zap.String("tp_id", tpID),
				zap.Error(err))
		} else {
			b.log.Info("staged TP order cancelled", zap.String("tp_id", tpID))
		}
	}
}

// PlaceStagedTPOrders places multiple reduce-only limit orders on the exchange
// for staged take-profit exits. Also places the initial stop-loss.
// totalQty is the full position size (used for the SL order).
// Returns true if at least the SL was placed successfully.
func (b *Broker) PlaceStagedTPOrders(ctx context.Context, symbol, posSide string, closeSide exchange.OrderSide, stopPrice, totalQty float64, tps []StagedTP) bool {
	key := brokerPosKey(symbol, posSide)
	ids := protectiveIDs{}

	// Round price to 2 decimals and qty to 3 decimals (ETHUSDT tick/lot size).
	stopPrice = math.Round(stopPrice*100) / 100
	totalQty = math.Floor(totalQty*1000) / 1000

	// Place SL first (most critical) — use totalQty so exchange knows exact qty to close.
	slID, err := b.orderClient.PlaceStopMarketOrder(ctx, symbol, closeSide, posSide, totalQty, stopPrice, "")
	if err != nil {
		b.log.Error("staged TP: failed to place initial SL",
			zap.String("symbol", symbol), zap.Error(err))
		if b.notifier != nil {
			b.notifier.SystemAlert("CRITICAL", fmt.Sprintf(
				"Failed to place SL for %s %s @ %.2f: %v", symbol, posSide, stopPrice, err))
		}
		return false
	}
	ids.stopID = slID
	b.log.Info("staged TP: SL placed",
		zap.String("sl_id", slID), zap.Float64("stop", stopPrice))

	// Place each TP level as reduce-only limit order, tracked through OMS for fill detection.
	for i, tp := range tps {
		// Submit through OMS so fills are detected via pollOrderUntilFilled → processFills → strategy.OnFill.
		tpReq := strategy.OrderRequest{
			Symbol:       symbol,
			Side:         strategy.Side(closeSide),
			PositionSide: strategy.PositionSide(posSide),
			Type:         strategy.OrderLimit,
			Qty:          tp.Qty,
			Price:        tp.Price,
		}
		tpOrd, omsErr := b.omsInst.Submit(tpReq, "staged_tp")
		if omsErr != nil {
			b.log.Error("staged TP: OMS submit failed", zap.Int("level", i+1), zap.Error(omsErr))
			continue
		}
		b.omsInst.SetRole(tpOrd.ID, "staged_tp")

		tpID, tpErr := b.orderClient.PlaceReduceOnlyLimitOrder(ctx, symbol, closeSide, posSide, tp.Qty, tp.Price, tpOrd.ClientOrderID)
		if tpErr != nil {
			b.omsInst.Reject(tpOrd.ID, tpErr.Error())
			b.log.Error("staged TP: failed to place TP level",
				zap.Int("level", i+1), zap.Float64("price", tp.Price),
				zap.Float64("qty", tp.Qty), zap.Error(tpErr))
			continue
		}
		b.omsInst.SetExchangeID(tpOrd.ID, tpID)
		b.omsInst.Accept(tpOrd.ID)
		ids.tpIDs = append(ids.tpIDs, tpID)

		// Launch fill poller so processFills detects when this TP fires.
		if sc, ok := b.orderClient.(exchange.OrderStatusChecker); ok {
			go b.pollOrderUntilFilled(b.engineCtx, sc, tpID, tpOrd.ID, tpReq)
		}

		b.log.Info("staged TP: level placed",
			zap.Int("level", i+1), zap.String("tp_id", tpID),
			zap.String("oms_id", tpOrd.ID),
			zap.Float64("price", tp.Price), zap.Float64("qty", tp.Qty))
	}

	b.protMu.Lock()
	b.protectiveOrders[key] = ids
	b.protMu.Unlock()
	return true
}

// ReplaceSLOrder cancels the current SL and places a new one at newStopPrice.
// Used for the +0.5R breakeven move. Qty=0 means close entire position.
func (b *Broker) ReplaceSLOrder(ctx context.Context, symbol, posSide string, closeSide exchange.OrderSide, remainQty, newStopPrice float64) bool {
	newStopPrice = math.Round(newStopPrice*100) / 100
	remainQty = math.Floor(remainQty*1000) / 1000
	key := brokerPosKey(symbol, posSide)

	b.protMu.Lock()
	ids, ok := b.protectiveOrders[key]
	b.protMu.Unlock()
	if !ok {
		return false
	}

	// Cancel old SL
	if ids.stopID != "" {
		if err := b.orderClient.CancelOrder(ctx, symbol, ids.stopID); err != nil {
			b.log.Warn("ReplaceSL: cancel old SL failed",
				zap.String("stop_id", ids.stopID), zap.Error(err))
			// Don't return — try to place new one anyway (old might already be filled/cancelled)
		} else {
			b.log.Info("ReplaceSL: old SL cancelled", zap.String("stop_id", ids.stopID))
		}
	}

	// Place new SL
	newID, err := b.orderClient.PlaceStopMarketOrder(ctx, symbol, closeSide, posSide, remainQty, newStopPrice, "")
	if err != nil {
		b.log.Error("ReplaceSL: failed to place new SL",
			zap.Float64("new_stop", newStopPrice), zap.Error(err))
		if b.notifier != nil {
			b.notifier.SystemAlert("CRITICAL", fmt.Sprintf(
				"Failed to replace SL for %s %s @ %.2f: %v", symbol, posSide, newStopPrice, err))
		}
		return false
	}

	b.protMu.Lock()
	ids.stopID = newID
	b.protectiveOrders[key] = ids
	b.protMu.Unlock()

	b.log.Info("ReplaceSL: new SL placed",
		zap.String("sl_id", newID), zap.Float64("stop", newStopPrice))
	return true
}

// CancelAllPendingOrders cancels all non-terminal OMS orders that have been acknowledged
// by the exchange (ExchangeID != ""). Called on engine shutdown to prevent orphaned
// stop-loss and take-profit orders from continuing to execute on the exchange.
func (b *Broker) CancelAllPendingOrders(ctx context.Context) {
	pending := b.omsInst.PendingOrders()
	if len(pending) == 0 {
		return
	}
	b.log.Info("cancelling open exchange orders on shutdown", zap.Int("count", len(pending)))
	for _, ord := range pending {
		if ord.ExchangeID == "" {
			continue // not yet acknowledged by exchange; nothing to cancel
		}
		err := b.orderClient.CancelOrder(ctx, ord.Symbol, ord.ExchangeID)
		if err != nil && isTransientError(err) {
			// Retry once on transient error to avoid orphaned stop-loss/TP orders.
			time.Sleep(500 * time.Millisecond)
			err = b.orderClient.CancelOrder(ctx, ord.Symbol, ord.ExchangeID)
		}
		if err != nil {
			b.log.Warn("cancel order on shutdown failed (order may remain live on exchange)",
				zap.String("order_id", ord.ID),
				zap.String("exchange_id", ord.ExchangeID),
				zap.String("symbol", ord.Symbol),
				zap.String("role", ord.Role),
				zap.Error(err))
			if b.notifier != nil && (ord.Role == "stop_loss" || ord.Role == "take_profit") {
				b.notifier.SystemAlert("CRITICAL", fmt.Sprintf(
					"🚨 SHUTDOWN: failed to cancel %s order %s on exchange\nSymbol: %s | ExchangeID: %s\nMANUAL CANCELLATION REQUIRED",
					ord.Role, ord.ID, ord.Symbol, ord.ExchangeID,
				))
			}
		} else {
			b.omsInst.Cancel(ord.ID) //nolint:errcheck
			b.log.Info("order cancelled on shutdown",
				zap.String("order_id", ord.ID),
				zap.String("exchange_id", ord.ExchangeID),
				zap.String("symbol", ord.Symbol))
		}
	}
}

// placeProtectiveWithRetry attempts to place a protective order with one retry on transient failure.
// Returns the exchange order ID on success, or the last error on failure.
func (b *Broker) placeProtectiveWithRetry(ctx context.Context, kind string, fn func(context.Context) (string, error), symbol string, price float64) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		id, err := fn(ctx)
		if err == nil {
			return id, nil
		}
		lastErr = err
		if !isTransientError(err) {
			break // business error, no point retrying
		}
		b.log.Warn("protective order transient error, retrying",
			zap.String("kind", kind),
			zap.String("symbol", symbol),
			zap.Float64("price", price),
			zap.Int("attempt", attempt+1),
			zap.Error(err))
		time.Sleep(500 * time.Millisecond)
	}
	return "", lastErr
}

// alertUnprotectedPosition logs a critical error and sends a notification
// when a protective order (stop-loss or take-profit) fails to place.
// This means the position is open with no risk protection.
func (b *Broker) alertUnprotectedPosition(symbol, posSide, kind string, price float64, err error) {
	b.log.Error("UNPROTECTED POSITION — protective order failed after retry",
		zap.String("kind", kind),
		zap.String("symbol", symbol),
		zap.String("position_side", posSide),
		zap.Float64("price", price),
		zap.Error(err))
	if b.notifier != nil {
		b.notifier.SystemAlert("CRITICAL", fmt.Sprintf(
			"🚨 UNPROTECTED POSITION — %s order FAILED\n"+
				"Symbol: %s | Side: %s | Price: %.8f\n"+
				"Error: %s\n"+
				"MANUAL INTERVENTION REQUIRED",
			kind, symbol, posSide, price, err.Error(),
		))
	}
}
