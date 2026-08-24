// Package live implements a real-money broker that routes orders to a configured exchange.
package live

import (
	"context"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

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
	stopID   string
	tpID     string
	tpIDs    []string // staged TP: exchange order IDs
	tpOmsIDs []string // staged TP: OMS order IDs (parallel to tpIDs)
}

// Broker submits real orders via an exchange.OrderClient and tracks fills via the OMS.
// It implements strategy.Broker.
type Broker struct {
	orderClient exchange.OrderClient
	omsInst     *oms.OMS
	positions   *oms.PositionManager
	notifier    *notify.Notifier // may be nil; used for critical alerts (e.g. unhedged position)
	log         *zap.Logger

	cash           atomic.Value // float64 — internal accounting (affected by leverage/margin)
	equity         atomic.Value // float64
	walletBalance  atomic.Value // float64 — true exchange wallet balance, independent of leverage
	lastPrice      atomic.Value // float64; updated by engine before each OnBar
	dayStartEquity atomic.Value // float64; equity at day start (for ORG daily-loss rule)
	warmup         atomic.Bool  // true while replaying startup backfill bars; PlaceOrder is a no-op

	// engineCtx is set by engine.Run before processing begins.
	// Poll goroutines for limit/stop orders use this context so they
	// are automatically cancelled when the engine stops.
	engineCtx    context.Context
	pollInterval time.Duration

	// protectiveOrders maps posKey(symbol, positionSide) → protectiveIDs
	// so that stop-loss and take-profit orders can be cancelled when the position is closed.
	protMu           sync.Mutex
	protectiveOrders map[string]protectiveIDs

	// Hard gross-exposure guard. The live order path has NO risk.Check (that is
	// paper-only), so without this an opening order can run exposure away when the
	// strategy's internal position view desyncs from the exchange (the 31-ETH-on-
	// $3.6k case). grossQtyFn returns the EXCHANGE-truth gross qty (long+short)
	// from the position syncer; an opening order is blocked when projected gross
	// notional would exceed equity × maxLeverage × maxGrossFrac. nil = guard off.
	grossQtyFn   func() float64
	maxLeverage  int
	maxGrossFrac float64
}

// defaultMaxGrossExposureFrac caps real gross notional at 80% of full-leverage
// notional (equity × leverage), leaving 20% margin headroom.
const defaultMaxGrossExposureFrac = 0.8

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
	b.walletBalance.Store(0.0)
	b.lastPrice.Store(0.0)
	b.dayStartEquity.Store(0.0)
	return b
}

// SetEngineCtx sets the engine's lifecycle context so that fill-polling goroutines
// are cancelled automatically when the engine stops.
// Must be called once at the start of engine.Run before any orders are placed.
func (b *Broker) SetEngineCtx(ctx context.Context) { b.engineCtx = ctx }

// SetLastPrice records the most recent market price.
func (b *Broker) SetLastPrice(price float64) { b.lastPrice.Store(price) }

// LastPrice returns the most recent market price (0 if unset).
func (b *Broker) LastPrice() float64 { return safeLoadFloat64(&b.lastPrice) }

// SetDayStartEquity records the equity baseline for the current trading day
// (used by the ORG daily-loss rule).
func (b *Broker) SetDayStartEquity(v float64) { b.dayStartEquity.Store(v) }

// DayStartEquity returns the current day's equity baseline (0 if unset).
func (b *Broker) DayStartEquity() float64 { return safeLoadFloat64(&b.dayStartEquity) }

// GrossQty returns the exchange-truth gross position qty (long+short) via the
// exposure guard's source, or 0 when the guard is not wired.
func (b *Broker) GrossQty() float64 {
	if b.grossQtyFn != nil {
		return b.grossQtyFn()
	}
	return 0
}

// MaxLeverage returns the account max leverage as set by the exposure guard
// (0 until SetExposureGuard runs).
func (b *Broker) MaxLeverage() int { return b.maxLeverage }

// SetExposureGuard wires the hard gross-exposure cap. grossQty returns the REAL
// exchange gross position qty (long+short, from the position syncer); leverage
// and frac define the cap (gross notional ≤ equity × leverage × frac).
func (b *Broker) SetExposureGuard(grossQty func() float64, leverage int, frac float64) {
	b.grossQtyFn = grossQty
	b.maxLeverage = leverage
	b.maxGrossFrac = frac
}

// isOpeningOrder reports whether req increases exposure (opens/adds) vs reduces.
// Hedge mode: Buy+LONG / Sell+SHORT (and net Buy) open; Sell+LONG / Buy+SHORT close.
func isOpeningOrder(req strategy.OrderRequest) bool {
	if req.PositionSide == strategy.PositionSideShort {
		return req.Side == strategy.SideSell
	}
	return req.Side == strategy.SideBuy
}

// exceedsGrossExposure reports whether adding newQty on top of grossQty pushes
// gross notional past equity × leverage × frac. Pure, for testability.
// Returns false (don't block) when inputs are insufficient to judge.
func exceedsGrossExposure(equity, leverage, frac, grossQty, newQty, price float64) bool {
	if equity <= 0 || price <= 0 || frac <= 0 {
		return false
	}
	if leverage < 1 {
		leverage = 1
	}
	capNotional := equity * leverage * frac
	return (grossQty+newQty)*price > capNotional
}

// SyncBalance fetches the current balance for the given asset and seeds the cash field.
func (b *Broker) SyncBalance(ctx context.Context, asset string) error {
	free, err := b.orderClient.GetBalance(ctx, asset)
	if err != nil {
		return fmt.Errorf("sync balance: %w", err)
	}
	b.cash.Store(free)
	b.equity.Store(free)
	b.walletBalance.Store(free)
	b.log.Info("balance synced",
		zap.String("asset", asset),
		zap.Float64("free", free))
	return nil
}

// PlaceOrder implements strategy.Broker.
// Routes to the appropriate exchange method based on req.Type.
// For MARKET orders, submits synchronously and returns the OMS order ID.
// For LIMIT/STOP orders, submits asynchronously (fill tracking via future WS integration).
// SetWarmup toggles warmup mode. While true (startup backfill replay), PlaceOrder
// is a no-op so a strategy priming its indicators on historical bars does not
// place real orders on stale signals. The engine turns it off on the first live bar.
func (b *Broker) SetWarmup(v bool) { b.warmup.Store(v) }

func (b *Broker) PlaceOrder(req strategy.OrderRequest) string {
	// Warmup replay: prime indicators without trading on historical bars.
	if b.warmup.Load() {
		return ""
	}

	// Soft idempotency: block duplicate orders for the same symbol+side+positionSide
	// to prevent double-position after network retries. positionSide matters — see
	// FindPending's doc comment (hedge-mode direction flips share Side across
	// independent legs and must not be treated as duplicates of each other).
	if existing := b.omsInst.FindPending(req.Symbol, req.Side, req.PositionSide); existing != nil {
		// Stale pending orders (>5min) from DB recovery should not block new orders
		if time.Since(existing.CreatedAt) > 5*time.Minute {
			b.log.Info("clearing stale OMS order — cancelling on exchange too", zap.String("id", existing.ID))
			// Cancel on exchange FIRST to prevent ghost fills
			if existing.ExchangeID != "" {
				if err := b.orderClient.CancelOrder(b.engineCtx, req.Symbol, existing.ExchangeID); err != nil {
					b.log.Warn("stale order: exchange cancel failed (may already be filled)",
						zap.String("exchange_id", existing.ExchangeID), zap.Error(err))
				}
			}
			b.omsInst.Cancel(existing.ID)
		} else {
			b.log.Warn("duplicate order blocked — pending order already exists",
				zap.String("symbol", req.Symbol),
				zap.String("side", string(req.Side)),
				zap.String("existing_id", existing.ID),
				zap.String("existing_status", string(existing.Status)),
			)
			return existing.ID
		}
	}

	// Resolve auto-sized (Qty==0) market/limit orders up-front so the exposure
	// guard, the OMS order record, and fill reconciliation all use the real
	// quantity. Otherwise the OMS order keeps Qty=0 and OMS.Fill rejects every
	// real fill as an over-fill (fill.Qty > 0 = order.Qty), leaving the order
	// stuck OPEN and the strategy's hedge state (macross hasLong/hasShort) never
	// updating. Limit orders need this too (not just market) — placeLimitOrderAsync
	// already resolves Qty internally for the exchange call, but without this
	// up-front resolution the OMS record and exposure guard above never see it.
	// Explicit Qty>0 orders (e.g. aistrat) skip this.
	if req.Qty == 0 && (req.Type == strategy.OrderMarket || req.Type == strategy.OrderType("") || req.Type == strategy.OrderLimit) {
		resolved, err := b.resolveQty(req, string(req.PositionSide))
		if err != nil {
			b.log.Warn("auto-size: cannot resolve order qty — dropping order",
				zap.String("symbol", req.Symbol), zap.String("side", string(req.Side)),
				zap.String("position_side", string(req.PositionSide)), zap.Error(err))
			return ""
		}
		req.Qty = resolved
	}

	// Hard gross-exposure guard — the live path has no risk.Check (paper-only).
	// Block an OPENING order whose projected gross notional would exceed
	// equity × leverage × frac, using the syncer's exchange-truth position so a
	// desync can't run exposure away (the 31-ETH-on-$3.6k case).
	if b.grossQtyFn != nil && isOpeningOrder(req) {
		price := safeLoadFloat64(&b.lastPrice)
		if price <= 0 {
			price = req.Price
		}
		gross := b.grossQtyFn()
		if exceedsGrossExposure(b.Equity(), float64(b.maxLeverage), b.maxGrossFrac, gross, req.Qty, price) {
			b.log.Warn("敞口保护：开仓单被拦截 — 预计总敞口会超过权益×杠杆×比例上限",
				zap.String("symbol", req.Symbol), zap.String("side", string(req.Side)),
				zap.String("pos_side", string(req.PositionSide)), zap.Float64("new_qty", req.Qty),
				zap.Float64("exchange_gross_qty", gross), zap.Float64("equity", b.Equity()),
				zap.Int("leverage", b.maxLeverage), zap.Float64("frac", b.maxGrossFrac),
				zap.Float64("price", price))
			if b.notifier != nil {
				b.notifier.SystemAlert("WARN", "敞口保护拦截了一笔开仓单——实际仓位会超过总敞口上限（可能是账户状态不同步），请检查仓位")
			}
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
// placeMarketOrder submits a market order and returns the OMS ID.
// Fill confirmation is handled asynchronously via WS User Data Stream + background poller.
// Does NOT block the engine loop waiting for fill.
func (b *Broker) placeMarketOrder(ctx context.Context, ordID string, req strategy.OrderRequest, posSide string) string {
	qty, err := b.resolveQty(req, posSide)
	if err != nil {
		b.omsInst.Reject(ordID, err.Error()) //nolint:errcheck
		return ""
	}

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

	b.omsInst.Accept(ordID) //nolint:errcheck

	if fill.ExchangeID != "" {
		if err := b.omsInst.SetExchangeID(ordID, fill.ExchangeID); err != nil {
			b.log.Warn("SetExchangeID failed", zap.String("order_id", ordID), zap.Error(err))
		}
	}

	// If exchange returned fill data immediately, apply it now.
	if fill.FilledQty > 0 {
		b.applyMarketFill(ordID, req, posSide, fill)
	} else if fill.ExchangeID != "" {
		// No immediate fill (common on Binance Futures) — poll asynchronously.
		// WS User Data Stream will also deliver the fill, whichever arrives first wins.
		if sc, ok := b.orderClient.(exchange.OrderStatusChecker); ok {
			go b.pollMarketOrderFill(b.engineCtx, sc, fill.ExchangeID, ordID, req, posSide)
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

// applyMarketFill processes a market order fill (called synchronously or from async poller).
func (b *Broker) applyMarketFill(ordID string, req strategy.OrderRequest, posSide string, fill exchange.OrderFill) {
	// Captured before b.omsInst.Fill below — that call's ApplyFill happens
	// asynchronously later (via the engine's fill-processing loop), so
	// b.positions still reflects the position as it was BEFORE this fill.
	preFillQty := b.currentPositionQty(req.Symbol, posSide)

	stratFill := strategy.Fill{
		ID:           ordID + "-live",
		Symbol:       req.Symbol,
		Side:         req.Side,
		PositionSide: req.PositionSide,
		Qty:          fill.FilledQty,
		Price:        fill.AvgPrice,
		Fee:          fill.Fee,
		Reason:       req.Reason,
		Timestamp:    time.Now(),
	}
	// fillErr != nil means this call LOST a race against another path
	// applying the same fill first (2026-08-17/18 finding: the WS
	// user-data-stream handler in engine_run.go and this broker's own
	// pollMarketOrderFill both call into applyMarketFill/Fill for the same
	// exchange fill; the OMS's own duplicate/over-fill rejection is what
	// decides the winner). When we lose, preFillQty above was read AFTER the
	// winner already reduced the position, so it looks smaller than it
	// really was pre-fill — closesEntirePosition would then misjudge a
	// genuine partial reduce as a full close and cancel a stop-loss that's
	// still protecting the (already-updated-by-the-winner) remaining
	// position. Skip all fill-driven side effects when we didn't actually
	// apply anything.
	fillErr := b.omsInst.Fill(ordID, stratFill)
	if fillErr != nil {
		b.log.Debug("applyMarketFill: Fill rejected (lost race to another path), skipping protective-order side effects",
			zap.String("order_id", ordID), zap.Error(fillErr))
		return
	}

	if b.isOpeningFill(req) && (req.StopLoss > 0 || req.TakeProfit > 0) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b.placeProtectiveOrders(ctx, req, "", fill.FilledQty)
	}
	b.maybeCancelProtectiveOrdersOnClose(req.Symbol, posSide, req.Side, preFillQty, fill.FilledQty)
}

// maybeCancelProtectiveOrdersOnClose cancels a position's protective orders
// if, and only if, the given fill fully closes it — a partial reduce (e.g.
// macross's AsymmetricExit confirmed-loss reduce) must leave the remaining
// position's stop-loss/take-profit in place (2026-08-17 finding: cancelling
// on every reduce left the remainder with zero exchange-side protection
// until the position eventually closed some other way). Hedge-mode
// protective orders are position-side-scoped on the exchange, so leaving one
// in place after a partial reduce is safe — it can never close more than
// what's actually still open.
//
// Shared by applyMarketFill (REST-driven fills) and engine_run.go's WS
// user-data-stream handler (which only has an *oms.Order, not a
// strategy.OrderRequest, and never went through applyMarketFill at all —
// this used to leave that path with zero protective-order handling; see the
// 2026-08-17/18 incident). Callers MUST snapshot preFillQty (via
// currentPositionQty) BEFORE applying the fill that produced filledQty, and
// MUST NOT call this if their own Fill() call lost a duplicate/over-fill
// race — otherwise preFillQty reflects a position already reduced by
// whichever path won, making a genuine partial reduce look like a full close.
func (b *Broker) maybeCancelProtectiveOrdersOnClose(symbol, posSide string, side strategy.Side, preFillQty, filledQty float64) {
	req := strategy.OrderRequest{Symbol: symbol, Side: side, PositionSide: strategy.PositionSide(posSide)}
	if b.isClosingFill(req) && closesEntirePosition(preFillQty, filledQty) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b.cancelProtectiveOrders(ctx, symbol, posSide)
	}
}

// currentPositionQty returns the qty currently believed open for the given
// symbol/positionSide ("" for one-way/net mode), or 0 if none is tracked.
func (b *Broker) currentPositionQty(symbol, posSide string) float64 {
	switch posSide {
	case string(strategy.PositionSideLong):
		if p, ok := b.positions.LongPosition(symbol); ok {
			return p.Qty
		}
	case string(strategy.PositionSideShort):
		if p, ok := b.positions.ShortPosition(symbol); ok {
			return p.Qty
		}
	default:
		if p, ok := b.positions.Position(symbol); ok {
			return p.Qty
		}
	}
	return 0
}

// pollMarketOrderFill polls for market order fill in background goroutine.
func (b *Broker) pollMarketOrderFill(ctx context.Context, sc exchange.OrderStatusChecker, exchangeID, ordID string, req strategy.OrderRequest, posSide string) {
	b.log.Info("market order pending fill, polling async...",
		zap.String("exchange_id", exchangeID))
	for i := 0; i < 10; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
		status, fill, err := sc.GetOrderStatus(ctx, req.Symbol, exchangeID)
		if err != nil {
			continue
		}
		if status == "FILLED" || status == "filled" {
			b.log.Info("market order fill confirmed (async)",
				zap.Float64("qty", fill.FilledQty),
				zap.Float64("price", fill.AvgPrice))
			b.applyMarketFill(ordID, req, posSide, fill)
			return
		}
	}
	b.log.Warn("market order fill poll exhausted — relying on WS User Data Stream",
		zap.String("exchange_id", exchangeID))
}

// placeLimitOrderAsync submits a limit order and returns the OMS ID without waiting for fill.
// If the exchange supports OrderStatusChecker, a background goroutine polls for fill confirmation.
func (b *Broker) placeLimitOrderAsync(ctx context.Context, ordID string, req strategy.OrderRequest, posSide string) string {
	qty, err := b.resolveQty(req, posSide)
	if err != nil {
		b.omsInst.Reject(ordID, err.Error()) //nolint:errcheck
		return ""
	}

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
	b.omsInst.Accept(ordID) //nolint:errcheck
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
	b.omsInst.Accept(ordID) //nolint:errcheck
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

// resolveQty computes the order quantity based on position side and available balance/position.
// defaultQtyStep is the LOT_SIZE step applied to auto-sized (Qty:0) order
// quantities so they satisfy the exchange's quantity precision. Binance
// BTCUSDT/ETHUSDT futures both use 0.001. Stopgap until per-symbol stepSize is
// sourced from exchangeInfo; strategies that pass an explicit Qty>0 (e.g.
// aistrat) return early below and are unaffected.
const defaultQtyStep = 0.001

// roundQtyToStep floors qty down to the nearest multiple of step so an
// auto-sized quantity passes the exchange LOT_SIZE filter (a raw float like
// 0.05120565 is rejected with -1111). Floor (not round) so the order never
// exceeds available margin. The +1e-9 nudge keeps values that are exact
// multiples but land just below due to float error (2.116/0.001 =
// 2115.9999999998) from flooring one step short. step<=0 or qty<=0 → unchanged.
func roundQtyToStep(qty, step float64) float64 {
	if step <= 0 || qty <= 0 {
		return qty
	}
	return math.Floor(qty/step+1e-9) * step
}

// sizingLeverage returns the multiplier auto-sized opening orders use against
// available cash. Mirrors exceedsGrossExposure's own "at least 1x" floor —
// unset (0, before SetExposureGuard has run) or spot/one-way (1) both size as
// unleveraged. Configured futures leverage (e.g. 5x) scales sizing directly,
// so a 5x account actually opens 5x-sized positions instead of always sizing
// as if unleveraged and leaving most of the account's capital efficiency
// unused (2026-08-13 finding).
func (b *Broker) sizingLeverage() float64 {
	if b.maxLeverage < 1 {
		return 1
	}
	return float64(b.maxLeverage)
}

// sizingCashFrac is the fraction of available cash an auto-sized (Qty:0)
// opening order uses, before the leverage multiplier. Must stay meaningfully
// below exceedsGrossExposure's defaultMaxGrossExposureFrac (0.8): both
// figures get multiplied by the SAME configured leverage, so the leverage
// cancels out of the comparison — sizing at 0.99 against an 0.8 cap meant
// ANY auto-sized opening order, from a completely flat account, at ANY
// leverage, always exceeded the exposure guard and got rejected outright
// (2026-08-13 production incident: a real opening order blocked with zero
// pre-existing exposure). 0.6 leaves headroom under 0.8 for cash-vs-equity
// drift (unrealized PnL), small pre-existing gross exposure, and price
// movement between this snapshot and the guard's own re-check.
const sizingCashFrac = 0.6

func (b *Broker) resolveQty(req strategy.OrderRequest, posSide string) (float64, error) {
	if req.Qty > 0 {
		return req.Qty, nil
	}

	lp := safeLoadFloat64(&b.lastPrice)

	switch {
	// Opening long or net buy: use available cash × configured leverage
	case (posSide == "" && req.Side == strategy.SideBuy) ||
		(posSide == string(strategy.PositionSideLong) && req.Side == strategy.SideBuy):
		if lp <= 0 {
			return 0, fmt.Errorf("all-in buy: no last price available")
		}
		cash := safeLoadFloat64(&b.cash)
		return roundQtyToStep(cash*sizingCashFrac*b.sizingLeverage()/lp, defaultQtyStep), nil

	// Opening short: use cash as margin × configured leverage
	case posSide == string(strategy.PositionSideShort) && req.Side == strategy.SideSell:
		if lp <= 0 {
			return 0, fmt.Errorf("all-in short: no last price available")
		}
		cash := safeLoadFloat64(&b.cash)
		return roundQtyToStep(cash*sizingCashFrac*b.sizingLeverage()/lp, defaultQtyStep), nil

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

// StagedTP describes one level in a staged take-profit plan.
type StagedTP struct {
	Price float64
	Qty   float64
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

// WalletBalance returns the true exchange wallet balance, independent of leverage.
func (b *Broker) WalletBalance() float64 { return safeLoadFloat64(&b.walletBalance) }

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
