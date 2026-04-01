// Package live implements a real-money broker that routes orders to a configured exchange.
package live

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/notify"
	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/strategy"
)

// filledEps is the floating-point tolerance for incremental fill detection.
const filledEps = 1e-9

// protectiveIDs holds exchange order IDs for stop-loss and take-profit orders
// that were auto-placed after an entry fill.
type protectiveIDs struct {
	stopID string
	tpID   string
}

// Broker submits real orders via an exchange.OrderClient and tracks fills via the OMS.
// It implements strategy.Broker.
type Broker struct {
	orderClient exchange.OrderClient
	omsInst     *oms.OMS
	positions   *oms.PositionManager
	notifier    *notify.Notifier // may be nil; used for critical alerts (e.g. unhedged position)
	log         *zap.Logger

	cash      atomic.Value // float64
	equity    atomic.Value // float64
	lastPrice atomic.Value // float64; updated by engine before each OnBar

	// engineCtx is set by engine.Run before processing begins.
	// Poll goroutines for limit/stop orders use this context so they
	// are automatically cancelled when the engine stops.
	engineCtx context.Context
	pollInterval time.Duration

	// protectiveOrders maps posKey(symbol, positionSide) → protectiveIDs
	// so that stop-loss and take-profit orders can be cancelled when the position is closed.
	protMu           sync.Mutex
	protectiveOrders map[string]protectiveIDs
}

// brokerPosKey returns the map key for protective orders.
func brokerPosKey(symbol, positionSide string) string {
	if positionSide == "" {
		return symbol
	}
	return symbol + ":" + positionSide
}

// New creates a live Broker. notif may be nil (alerts disabled).
func New(orderClient exchange.OrderClient, o *oms.OMS, pm *oms.PositionManager, notif *notify.Notifier, log *zap.Logger) *Broker {
	b := &Broker{
		orderClient:      orderClient,
		omsInst:          o,
		positions:        pm,
		notifier:         notif,
		log:              log,
		engineCtx:        context.Background(),
		pollInterval:     5 * time.Second,
		protectiveOrders: make(map[string]protectiveIDs),
	}
	b.cash.Store(0.0)
	b.equity.Store(0.0)
	b.lastPrice.Store(0.0)
	return b
}

// SetEngineCtx sets the engine's lifecycle context so that fill-polling goroutines
// are cancelled automatically when the engine stops.
// Must be called once at the start of engine.Run before any orders are placed.
func (b *Broker) SetEngineCtx(ctx context.Context) { b.engineCtx = ctx }

// SetLastPrice records the most recent market price.
func (b *Broker) SetLastPrice(price float64) { b.lastPrice.Store(price) }

// SyncBalance fetches the current balance for the given asset and seeds the cash field.
func (b *Broker) SyncBalance(ctx context.Context, asset string) error {
	free, err := b.orderClient.GetBalance(ctx, asset)
	if err != nil {
		return fmt.Errorf("sync balance: %w", err)
	}
	b.cash.Store(free)
	b.equity.Store(free)
	b.log.Info("balance synced",
		zap.String("asset", asset),
		zap.Float64("free", free))
	return nil
}

// PlaceOrder implements strategy.Broker.
// Routes to the appropriate exchange method based on req.Type.
// For MARKET orders, submits synchronously and returns the OMS order ID.
// For LIMIT/STOP orders, submits asynchronously (fill tracking via future WS integration).
func (b *Broker) PlaceOrder(req strategy.OrderRequest) string {
	// Soft idempotency: block duplicate orders for the same symbol+side to
	// prevent double-position after network retries.
	if existing := b.omsInst.FindPending(req.Symbol, req.Side); existing != nil {
		// Stale pending orders (>5min) from DB recovery should not block new orders
		if time.Since(existing.CreatedAt) > 5*time.Minute {
			b.log.Info("clearing stale OMS order", zap.String("id", existing.ID))
			b.omsInst.Cancel(existing.ID)
		} else {
			b.log.Warn("duplicate order blocked — pending order already exists",
				zap.String("symbol", req.Symbol),
				zap.String("side", string(req.Side)),
				zap.String("existing_id", existing.ID),
				zap.String("existing_status", string(existing.Status)),
			)
			return ""
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ord, err := b.omsInst.Submit(req, "live")
	if err != nil {
		b.log.Error("OMS submit failed", zap.Error(err))
		return ""
	}

	posSide := string(req.PositionSide)

	switch req.Type {
	case strategy.OrderMarket, strategy.OrderType(""):
		return b.placeMarketOrder(ctx, ord.ID, req, posSide)

	case strategy.OrderLimit:
		return b.placeLimitOrderAsync(ctx, ord.ID, req, posSide)

	case strategy.OrderStopMarket:
		return b.placeStopOrderAsync(ctx, ord.ID, req, posSide)

	default:
		b.omsInst.Reject(ord.ID, fmt.Sprintf("unknown order type: %s", req.Type)) //nolint:errcheck
		return ""
	}
}

// placeMarketOrder executes a market order synchronously and handles fills + protective orders.
func (b *Broker) placeMarketOrder(ctx context.Context, ordID string, req strategy.OrderRequest, posSide string) string {
	qty, err := b.resolveQty(req, posSide)
	if err != nil {
		b.omsInst.Reject(ordID, err.Error()) //nolint:errcheck
		return ""
	}

	b.omsInst.Accept(ordID) //nolint:errcheck

	clientOrderID := ""
	if ord := b.omsInst.Get(ordID); ord != nil {
		clientOrderID = ord.ClientOrderID
	}

	var fill exchange.OrderFill
	retryBackoffs := []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}
	for attempt := 0; attempt < 2; attempt++ {
		fill, err = b.orderClient.PlaceMarketOrder(ctx, req.Symbol, exchange.OrderSide(req.Side), posSide, qty, clientOrderID)
		if err == nil || !isTransientError(err) {
			break
		}
		b.log.Warn("market order transient error, retrying with same clientOrderID",
			zap.Int("attempt", attempt+1),
			zap.String("client_order_id", clientOrderID),
			zap.Error(err))
		time.Sleep(retryBackoffs[attempt])
	}
	if err != nil {
		b.omsInst.Reject(ordID, err.Error()) //nolint:errcheck
		b.log.Error("exchange market order failed",
			zap.String("order_id", ordID), zap.Error(err))
		return ""
	}

	if fill.ExchangeID != "" {
		if err := b.omsInst.SetExchangeID(ordID, fill.ExchangeID); err != nil {
			b.log.Warn("SetExchangeID failed", zap.String("order_id", ordID), zap.Error(err))
		}
	}

	// If exchange returned qty=0 (async fill, common on Binance Futures),
	// poll for the actual fill using OrderStatusChecker.
	if fill.FilledQty == 0 && fill.ExchangeID != "" {
		if sc, ok := b.orderClient.(exchange.OrderStatusChecker); ok {
			b.log.Info("market order pending fill, polling...",
				zap.String("exchange_id", fill.ExchangeID))
			for i := 0; i < 10; i++ {
				time.Sleep(500 * time.Millisecond)
				status, polled, pollErr := sc.GetOrderStatus(ctx, req.Symbol, fill.ExchangeID)
				if pollErr != nil {
					continue
				}
				if status == "FILLED" || status == "filled" {
					fill = polled
					b.log.Info("market order fill confirmed",
						zap.Float64("qty", fill.FilledQty),
						zap.Float64("price", fill.AvgPrice))
					break
				}
			}
		}
	}

	if fill.FilledQty > 0 {
		stratFill := strategy.Fill{
			ID:           ordID + "-live",
			Symbol:       req.Symbol,
			Side:         req.Side,
			PositionSide: req.PositionSide,
			Qty:          fill.FilledQty,
			Price:        fill.AvgPrice,
			Fee:          fill.Fee,
			Timestamp:    time.Now(),
		}
		b.omsInst.Fill(ordID, stratFill) //nolint:errcheck

		// Auto-place protective orders if this is an opening fill
		if b.isOpeningFill(req) && (req.StopLoss > 0 || req.TakeProfit > 0) {
			b.placeProtectiveOrders(ctx, req, "", fill.FilledQty)
		}
		// Cancel protective orders if this is a closing fill
		if b.isClosingFill(req) {
			b.cancelProtectiveOrders(ctx, req.Symbol, posSide)
		}
	}

	b.log.Info("market order placed",
		zap.String("order_id", ordID),
		zap.String("symbol", req.Symbol),
		zap.String("side", string(req.Side)),
		zap.String("position_side", posSide),
		zap.Float64("qty", fill.FilledQty),
		zap.Float64("avg_price", fill.AvgPrice),
	)
	return ordID
}

// placeLimitOrderAsync submits a limit order and returns the OMS ID without waiting for fill.
// If the exchange supports OrderStatusChecker, a background goroutine polls for fill confirmation.
func (b *Broker) placeLimitOrderAsync(ctx context.Context, ordID string, req strategy.OrderRequest, posSide string) string {
	qty, err := b.resolveQty(req, posSide)
	if err != nil {
		b.omsInst.Reject(ordID, err.Error()) //nolint:errcheck
		return ""
	}
	b.omsInst.Accept(ordID) //nolint:errcheck

	clientOrderID := ""
	if ord := b.omsInst.Get(ordID); ord != nil {
		clientOrderID = ord.ClientOrderID
	}

	var exchangeID string
	retryBackoffs := []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}
	for attempt := 0; attempt < 2; attempt++ {
		exchangeID, err = b.orderClient.PlaceLimitOrder(ctx, req.Symbol, exchange.OrderSide(req.Side), posSide, qty, req.Price, clientOrderID)
		if err == nil || !isTransientError(err) {
			break
		}
		b.log.Warn("limit order transient error, retrying with same clientOrderID",
			zap.Int("attempt", attempt+1),
			zap.String("client_order_id", clientOrderID),
			zap.Error(err))
		time.Sleep(retryBackoffs[attempt])
	}
	if err != nil {
		b.omsInst.Reject(ordID, err.Error()) //nolint:errcheck
		b.log.Error("exchange limit order failed", zap.String("order_id", ordID), zap.Error(err))
		return ""
	}
	if exchangeID != "" {
		if err := b.omsInst.SetExchangeID(ordID, exchangeID); err != nil {
			b.log.Warn("SetExchangeID failed", zap.String("order_id", ordID), zap.Error(err))
		}
	}

	// Launch fill-confirmation poller if the exchange supports order status queries.
	if sc, ok := b.orderClient.(exchange.OrderStatusChecker); ok && exchangeID != "" {
		go b.pollOrderUntilFilled(b.engineCtx, sc, exchangeID, ordID, req)
	}

	b.log.Info("limit order submitted (async fill tracking)",
		zap.String("order_id", ordID),
		zap.String("exchange_id", exchangeID),
		zap.String("symbol", req.Symbol),
		zap.Float64("price", req.Price),
	)
	return ordID
}

// placeStopOrderAsync submits a stop-market order via the exchange (e.g. as an algo order).
// If the exchange supports OrderStatusChecker, a background goroutine polls for fill confirmation.
func (b *Broker) placeStopOrderAsync(ctx context.Context, ordID string, req strategy.OrderRequest, posSide string) string {
	qty, err := b.resolveQty(req, posSide)
	if err != nil {
		b.omsInst.Reject(ordID, err.Error()) //nolint:errcheck
		return ""
	}
	b.omsInst.Accept(ordID) //nolint:errcheck

	clientOrderID := ""
	if ord := b.omsInst.Get(ordID); ord != nil {
		clientOrderID = ord.ClientOrderID
	}

	exchangeID, err := b.orderClient.PlaceStopMarketOrder(ctx, req.Symbol, exchange.OrderSide(req.Side), posSide, qty, req.StopPrice, clientOrderID)
	if err != nil {
		b.omsInst.Reject(ordID, err.Error()) //nolint:errcheck
		b.log.Error("exchange stop order failed", zap.String("order_id", ordID), zap.Error(err))
		return ""
	}
	if exchangeID != "" {
		if err := b.omsInst.SetExchangeID(ordID, exchangeID); err != nil {
			b.log.Warn("SetExchangeID failed", zap.String("order_id", ordID), zap.Error(err))
		}
	}

	// Launch fill-confirmation poller if the exchange supports order status queries.
	if sc, ok := b.orderClient.(exchange.OrderStatusChecker); ok && exchangeID != "" {
		go b.pollOrderUntilFilled(b.engineCtx, sc, exchangeID, ordID, req)
	}

	b.log.Info("stop-market order submitted",
		zap.String("order_id", ordID),
		zap.String("exchange_id", exchangeID),
		zap.Float64("stop_price", req.StopPrice),
	)
	return ordID
}

// pollOrderUntilFilled polls the exchange every 5 seconds until the order is filled,
// cancelled, or the engine context is cancelled. On fill, publishes to the OMS so that
// processFills() handles portfolio accounting and strategy.OnFill notification.
func (b *Broker) pollOrderUntilFilled(ctx context.Context, sc exchange.OrderStatusChecker, exchangeID, ordID string, req strategy.OrderRequest) {
	interval := b.pollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Track cumulative filled qty to emit only the incremental portion on each poll,
	// preventing duplicate fills when the exchange reports the same partial fill repeatedly.
	var prevFilledQty float64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			status, fill, err := sc.GetOrderStatus(queryCtx, req.Symbol, exchangeID)
			cancel()
			// Check if engine context was cancelled during the query.
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				b.log.Debug("poll order status error",
					zap.String("exchange_id", exchangeID),
					zap.Error(err))
				continue
			}

			switch status {
			case "filled", "FILLED", "partially_filled", "PARTIALLY_FILLED":
				// Only publish the incremental fill qty since the last poll.
				incrementalQty := fill.FilledQty - prevFilledQty
				if incrementalQty > filledEps {
					stratFill := strategy.Fill{
						ID:           fmt.Sprintf("%s-poll-%d", ordID, time.Now().UnixMilli()),
						Symbol:       req.Symbol,
						Side:         req.Side,
						PositionSide: req.PositionSide,
						Qty:          incrementalQty,
						Price:        fill.AvgPrice,
						Fee:          fill.Fee,
						Timestamp:    time.Now(),
					}
					if fillErr := b.omsInst.Fill(ordID, stratFill); fillErr != nil {
						b.log.Warn("poll fill: OMS fill failed",
							zap.String("order_id", ordID),
							zap.Error(fillErr))
					} else {
						b.log.Info("poll confirmed order filled",
							zap.String("order_id", ordID),
							zap.String("exchange_id", exchangeID),
							zap.Float64("incremental_qty", incrementalQty),
							zap.Float64("total_filled", fill.FilledQty),
							zap.Float64("price", fill.AvgPrice),
						)
					}
					prevFilledQty = fill.FilledQty
				}
				if status == "filled" || status == "FILLED" {
					return // fully filled; stop polling
				}
			case "canceled", "CANCELED", "cancelled", "CANCELLED", "expired", "EXPIRED":
				b.log.Info("polled order no longer active",
					zap.String("order_id", ordID),
					zap.String("status", status))
				return
			}
		}
	}
}

// resolveQty computes the order quantity based on position side and available balance/position.
func (b *Broker) resolveQty(req strategy.OrderRequest, posSide string) (float64, error) {
	if req.Qty > 0 {
		return req.Qty, nil
	}

	lp := safeLoadFloat64(&b.lastPrice)

	switch {
	// Opening long or net buy: use available cash
	case (posSide == "" && req.Side == strategy.SideBuy) ||
		(posSide == string(strategy.PositionSideLong) && req.Side == strategy.SideBuy):
		if lp <= 0 {
			return 0, fmt.Errorf("all-in buy: no last price available")
		}
		cash := safeLoadFloat64(&b.cash)
		return cash * 0.99 / lp, nil

	// Opening short: use cash as margin (1x equiv)
	case posSide == string(strategy.PositionSideShort) && req.Side == strategy.SideSell:
		if lp <= 0 {
			return 0, fmt.Errorf("all-in short: no last price available")
		}
		cash := safeLoadFloat64(&b.cash)
		return cash * 0.99 / lp, nil

	// Closing long or net sell
	case (posSide == "" && req.Side == strategy.SideSell) ||
		(posSide == string(strategy.PositionSideLong) && req.Side == strategy.SideSell):
		pos, ok := b.positions.LongPosition(req.Symbol)
		if !ok {
			// Fall back to net position for spot/one-way mode
			netPos, netOk := b.positions.Position(req.Symbol)
			if !netOk || netPos.Qty <= 0 {
				return 0, fmt.Errorf("no long position to sell for %s", req.Symbol)
			}
			return netPos.Qty, nil
		}
		if pos.Qty <= 0 {
			return 0, fmt.Errorf("no long position to sell for %s", req.Symbol)
		}
		return pos.Qty, nil

	// Closing short
	case posSide == string(strategy.PositionSideShort) && req.Side == strategy.SideBuy:
		pos, ok := b.positions.ShortPosition(req.Symbol)
		if !ok || pos.Qty <= 0 {
			return 0, fmt.Errorf("no short position to cover for %s", req.Symbol)
		}
		return pos.Qty, nil

	default:
		return 0, fmt.Errorf("cannot resolve qty for side=%s positionSide=%s", req.Side, posSide)
	}
}

// isOpeningFill returns true when the fill represents opening (adding to) a position.
func (b *Broker) isOpeningFill(req strategy.OrderRequest) bool {
	posSide := string(req.PositionSide)
	return (posSide == string(strategy.PositionSideLong) && req.Side == strategy.SideBuy) ||
		(posSide == string(strategy.PositionSideShort) && req.Side == strategy.SideSell) ||
		(posSide == "" && req.Side == strategy.SideBuy)
}

// isClosingFill returns true when the fill represents closing a position.
func (b *Broker) isClosingFill(req strategy.OrderRequest) bool {
	return !b.isOpeningFill(req)
}

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

// CancelOrder cancels a live order via the exchange and removes it from the OMS.
func (b *Broker) CancelOrder(id string) error {
	ord := b.omsInst.Get(id)
	if ord == nil {
		return fmt.Errorf("order %s not found", id)
	}
	if ord.IsTerminal() {
		return fmt.Errorf("order %s already %s", id, ord.Status)
	}
	if ord.ExchangeID == "" {
		return fmt.Errorf("order %s has no exchange ID (not yet acknowledged)", id)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := b.orderClient.CancelOrder(ctx, ord.Symbol, ord.ExchangeID); err != nil {
		return fmt.Errorf("exchange cancel: %w", err)
	}
	return b.omsInst.Cancel(id)
}

// Cash returns available cash balance.
func (b *Broker) Cash() float64 { return safeLoadFloat64(&b.cash) }

// Equity returns current total equity.
func (b *Broker) Equity() float64 { return safeLoadFloat64(&b.equity) }

// safeLoadFloat64 loads a float64 from an atomic.Value without panicking.
// Returns 0.0 if the stored value is nil or not a float64.
func safeLoadFloat64(v *atomic.Value) float64 {
	val := v.Load()
	if val == nil {
		return 0.0
	}
	f, ok := val.(float64)
	if !ok {
		return 0.0
	}
	return f
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

// isTransientError returns true if err looks like a transient network-layer failure
// that is safe to retry with the same clientOrderID (since the exchange never saw the request).
// Business rejections (insufficient balance, invalid symbol, etc.) return false.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset by peer") {
		return true
	}
	// Unwrap looking for a *net.OpError (covers DNS failures, dial errors, etc.)
	for unwrapped := err; unwrapped != nil; {
		if _, ok := unwrapped.(*net.OpError); ok { //nolint:errorlint
			return true
		}
		type unwrapper interface{ Unwrap() error }
		if u, ok := unwrapped.(unwrapper); ok {
			unwrapped = u.Unwrap()
		} else {
			break
		}
	}
	return false
}
