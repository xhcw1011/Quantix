// Package strategy defines the core interfaces and types that all trading
// strategies must implement, and that the backtest / live engine uses.
package strategy

import (
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
)

// ─────────────────────────────────────────────
// Order primitives
// ─────────────────────────────────────────────

// Side is the direction of a trade.
type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

// PositionSide distinguishes long vs short legs in hedge-mode futures.
// Use PositionSideNet ("") for spot trading and one-way futures mode.
type PositionSide string

const (
	PositionSideLong  PositionSide = "LONG"
	PositionSideShort PositionSide = "SHORT"
	PositionSideNet   PositionSide = "" // spot / one-way futures
)

// OrderType determines how the order is matched.
type OrderType string

const (
	OrderMarket     OrderType = "MARKET"
	OrderLimit      OrderType = "LIMIT"
	OrderStopMarket OrderType = "STOP_MARKET" // triggered → market execution
	OrderStopLimit  OrderType = "STOP_LIMIT"  // triggered → limit execution
)

// OrderRequest is submitted by a strategy to the broker.
// Qty is in base-asset units (e.g. BTC).
// When Qty == 0 the broker interprets it as "use all available cash"
// for a Buy, or "close full position" for a Sell.
type OrderRequest struct {
	Symbol       string
	Side         Side
	PositionSide PositionSide // "" = net/spot, "LONG"/"SHORT" = hedge mode
	Type         OrderType
	Qty          float64 // 0 = max
	Price        float64 // LIMIT / STOP_LIMIT: limit price
	MakerOnly    bool    // LIMIT only: reject (don't fill as taker) if would cross book. Binance GTX/POST_ONLY semantics.
	StopPrice    float64 // STOP_MARKET / STOP_LIMIT: trigger price
	// Protective orders auto-placed by the live broker after a fill.
	StopLoss   float64 // trigger price for stop-loss order (0 = disabled)
	TakeProfit float64 // trigger price for take-profit order (0 = disabled)
	// Reason tags why this order was placed. On exits it records the trigger
	// ("stop_loss", "take_profit", "trailing", "signal_reversal", "time_exit",
	// …); it is propagated to the resulting Fill and the recorded Trade for
	// post-trade attribution. Optional; empty when unset.
	Reason string
	// Meta carries optional numeric context captured at order time — typically
	// the entry regime snapshot, e.g. {"adx":18,"atr":0.4,"funding":0.0003,
	// "regime":1}. It is propagated onto the Fill and, for opening orders, onto
	// the Trade's EntryMeta for scenario bucketing. Optional; nil when unused.
	Meta map[string]float64
}

// Fill is returned after an order is matched by the broker.
type Fill struct {
	ID           string
	Symbol       string
	Side         Side
	PositionSide PositionSide
	Qty          float64
	Price        float64 // actual execution price (after slippage)
	Fee          float64 // commission deducted
	Timestamp    time.Time
	// Reason / Meta are propagated from the originating OrderRequest so the
	// backtest portfolio (and the live trade_events log) can record why an
	// order fired and the context it fired in. Optional.
	Reason string
	Meta   map[string]float64
}

// ─────────────────────────────────────────────
// Order helper functions (for contract strategies)
// ─────────────────────────────────────────────

// OpenLong returns an OrderRequest to open a long position (BUY side, LONG positionSide).
func OpenLong(symbol string, qty float64) OrderRequest {
	return OrderRequest{Symbol: symbol, Side: SideBuy, PositionSide: PositionSideLong, Type: OrderMarket, Qty: qty}
}

// CloseLong returns an OrderRequest to close a long position (SELL side, LONG positionSide).
func CloseLong(symbol string, qty float64) OrderRequest {
	return OrderRequest{Symbol: symbol, Side: SideSell, PositionSide: PositionSideLong, Type: OrderMarket, Qty: qty}
}

// OpenShort returns an OrderRequest to open a short position (SELL side, SHORT positionSide).
func OpenShort(symbol string, qty float64) OrderRequest {
	return OrderRequest{Symbol: symbol, Side: SideSell, PositionSide: PositionSideShort, Type: OrderMarket, Qty: qty}
}

// CloseShort returns an OrderRequest to close a short position (BUY side, SHORT positionSide).
func CloseShort(symbol string, qty float64) OrderRequest {
	return OrderRequest{Symbol: symbol, Side: SideBuy, PositionSide: PositionSideShort, Type: OrderMarket, Qty: qty}
}

// ─────────────────────────────────────────────
// Broker interface
// ─────────────────────────────────────────────

// Broker is the interface strategies use to submit orders.
// The concrete implementation is either SimBroker (backtest) or LiveBroker (Phase 4).
type Broker interface {
	PlaceOrder(req OrderRequest) string // returns internal order ID
	CancelOrder(orderID string) error
}

// ─────────────────────────────────────────────
// Context
// ─────────────────────────────────────────────

// Context is passed to every strategy callback.
// It gives the strategy access to the current portfolio state and
// the ability to place orders, without exposing engine internals.
type Context struct {
	Portfolio PortfolioView
	Log       *zap.Logger
	broker    Broker
	Extra     map[string]any // optional strategy-specific dependencies (e.g. PositionSyncer)
}

// NewContext creates a strategy context.
func NewContext(pv PortfolioView, broker Broker, log *zap.Logger) *Context {
	return &Context{Portfolio: pv, broker: broker, Log: log, Extra: make(map[string]any)}
}

// PlaceOrder submits an order through the broker and returns the order ID.
func (c *Context) PlaceOrder(req OrderRequest) string {
	return c.broker.PlaceOrder(req)
}

// CancelOrder cancels a previously placed order by its ID.
func (c *Context) CancelOrder(orderID string) error {
	return c.broker.CancelOrder(orderID)
}

// ClosePosition is a convenience method that sells all of the position for symbol.
// Equivalent to PlaceOrder with Side=SELL, Qty=0 (meaning "close all").
func (c *Context) ClosePosition(symbol string) string {
	return c.PlaceOrder(OrderRequest{Symbol: symbol, Side: SideSell, Type: OrderMarket, Qty: 0})
}

// PortfolioView is the read-only view of the portfolio exposed to strategies.
type PortfolioView interface {
	Cash() float64
	// Position returns the net/long position for spot and one-way futures.
	Position(symbol string) (qty float64, avgPrice float64, ok bool)
	Equity(prices map[string]float64) float64
}

// ─────────────────────────────────────────────
// Strategy interface
// ─────────────────────────────────────────────

// Strategy is implemented by every trading strategy.
type Strategy interface {
	// Name returns the unique identifier of the strategy.
	Name() string

	// OnBar is called for each closed candlestick in chronological order.
	OnBar(ctx *Context, bar exchange.Kline)

	// OnFill is called when an order submitted by this strategy is filled.
	OnFill(ctx *Context, fill Fill)
}

// TickReceiver is an optional interface strategies can implement to receive
// real-time price updates (from WS ticker stream). Used for precise TP/SL.
type TickReceiver interface {
	OnTick(ctx *Context, price float64)
}

// StatusReporter is an optional interface strategies can implement to expose
// their internal state for live operator dashboards (regime, position flags,
// cooldown counters, etc.). Returned as a flat map for JSON serialization;
// keys are strategy-specific.
type StatusReporter interface {
	Status() map[string]any
}

// Retired is an optional interface a strategy implements to signal that it
// has permanently finished its job and the engine hosting it should stop
// itself (rather than keep polling/subscribing forever and being blindly
// resurrected on the next server restart — 2026-08-06 finding: guardian's own
// retire() only quieted the strategy's OnBar/OnTick, but the surrounding
// engine session had no way to learn about it).
type Retired interface {
	Retired() bool
}

// ErrRetired is returned by live.Engine.Run / paper.Engine.Run when they stop
// because the strategy reported Retired() == true, distinguishing a
// self-initiated stop from a genuine runtime error or an externally
// requested Stop() (each of which the engine manager must handle
// differently).
var ErrRetired = errors.New("strategy retired")

// LiveUpdatable is an optional interface a strategy implements to accept live
// parameter changes on a running engine. The engine calls UpdateParams from its
// own run-loop goroutine (never concurrently with OnBar/OnTick/OnFill), so
// implementations need no extra locking.
type LiveUpdatable interface {
	UpdateParams(ctx *Context, params map[string]any) error
}

// StagedTP describes one level in a staged take-profit plan.
type StagedTP struct {
	Price float64
	Qty   float64
}

// StagedExitPlacer places exchange-native orders for TP and SL management.
// Injected into Context.Extra["staged_exit"] by the live engine when available.
type StagedExitPlacer interface {
	// PlaceStagedTPOrders places multiple reduce-only limit TP orders (Trend mode, no exchange SL).
	PlaceStagedTPOrders(symbol, posSide, closeSide string, stopPrice, totalQty float64, tps []StagedTP) bool
	// PlaceExchangeSL places an exchange algo SL order (Range mode protection).
	PlaceExchangeSL(symbol, posSide, closeSide string, qty, stopPrice float64) bool
	// ReplaceSLOrder cancels the current SL and places a new one.
	ReplaceSLOrder(symbol, posSide, closeSide string, remainQty, newStopPrice float64) bool
	// CancelExchangeSL cancels only the exchange SL order (preserves TP orders).
	CancelExchangeSL(symbol, posSide string)
	// CancelAllProtective cancels all protective orders for a position.
	CancelAllProtective(symbol, posSide string)
}
