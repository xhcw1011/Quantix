// Package strategy defines the core interfaces and types that all trading
// strategies must implement, and that the backtest / live engine uses.
package strategy

import (
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
	StopPrice    float64 // STOP_MARKET / STOP_LIMIT: trigger price
	// Protective orders auto-placed by the live broker after a fill.
	StopLoss   float64 // trigger price for stop-loss order (0 = disabled)
	TakeProfit float64 // trigger price for take-profit order (0 = disabled)
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
