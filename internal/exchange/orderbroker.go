package exchange

import "context"

// PositionMarginInfo contains margin health data for a single open position.
type PositionMarginInfo struct {
	Symbol       string
	PositionSide string  // "LONG", "SHORT", or "" (net/one-way)
	MarginRatio  float64 // maintenance margin ratio as a fraction (e.g. 0.15 = 15%); higher is safer
	Size         float64 // current position size in base asset
}

// MarginQuerier can retrieve live margin health for open positions.
// Implemented by futures/swap brokers (OKX SWAP, Binance USDM Futures).
// Spot brokers are not required to implement this interface.
type MarginQuerier interface {
	GetMarginRatios(ctx context.Context) ([]PositionMarginInfo, error)
}

// EquityQuerier returns the true account equity from the exchange.
// This accounts for margin lock, unrealized PnL — the exchange's view of your wealth.
type EquityQuerier interface {
	GetEquity(ctx context.Context, asset string) (float64, error)
}

// OrderSide is the direction of a market order.
type OrderSide string

const (
	OrderSideBuy  OrderSide = "BUY"
	OrderSideSell OrderSide = "SELL"
)

// OrderFill contains execution details returned after placing a market order.
type OrderFill struct {
	ExchangeID   string  // exchange-assigned order ID
	FilledQty    float64 // base-asset quantity filled
	AvgPrice     float64 // volume-weighted average fill price
	Fee          float64 // total fee in quote asset
	Status       string  // "filled", "partial_fill", "live"
	Symbol       string  // e.g. "ETHUSDT" (populated by UDS for cash accounting)
	Side         string  // "BUY" or "SELL" (populated by UDS for cash accounting)
	PositionSide string  // "LONG", "SHORT", or "" (populated by UDS for cash accounting)
	IsReduceOnly bool    // true if this is a reduce-only order (closing trade)
}

// OrderStatusChecker polls the current status of a resting order (limit/stop/TP).
// Optional interface — only brokers that support it enable automatic fill-confirmation polling.
// Implemented by OKX SWAP and Binance USDM Futures brokers.
// Returned status values are exchange-specific (e.g. "filled", "FILLED", "live", "NEW").
// A status containing "fill" (case-insensitive) or equal to "FILLED" is treated as filled.
type OrderStatusChecker interface {
	GetOrderStatus(ctx context.Context, symbol, orderID string) (status string, fill OrderFill, err error)
}

// OpenOrdersCanceller is an optional extension of OrderClient that supports
// cancelling all open orders for a symbol in a single call.
// Implemented by Binance USDM Futures and OKX SWAP brokers.
// Used by live.Engine on startup ("clean-slate") to prevent orphaned orders
// from a previous crashed session from accumulating.
type OpenOrdersCanceller interface {
	CancelAllOpenOrders(ctx context.Context, symbol string) error
}

// OrderClient abstracts exchange-specific order operations.
// Implementations: binance.OrderBroker (spot), binance_futures.OrderBroker (USDM),
// okx.OrderBroker (SWAP demo/live).
type OrderClient interface {
	// PlaceMarketOrder submits a market order. qty is in base-asset units (e.g. BTC).
	// positionSide: "LONG", "SHORT", or "" (net/spot/one-way mode).
	// clientOrderID: 32-char idempotency key (pass "" to skip).
	PlaceMarketOrder(ctx context.Context, symbol string, side OrderSide, positionSide string, qty float64, clientOrderID string) (OrderFill, error)

	// PlaceLimitOrder submits a limit order and returns the exchange order ID.
	// positionSide: "LONG", "SHORT", or "" (net/spot).
	// clientOrderID: 32-char idempotency key (pass "" to skip).
	PlaceLimitOrder(ctx context.Context, symbol string, side OrderSide, positionSide string, qty, price float64, clientOrderID string) (string, error)

	// PlaceStopMarketOrder places a stop-market order that executes at market price
	// when the trigger price (stopPrice) is reached.
	// positionSide: "LONG", "SHORT", or "".
	// clientOrderID: 32-char idempotency key (pass "" to skip).
	PlaceStopMarketOrder(ctx context.Context, symbol string, side OrderSide, positionSide string, qty, stopPrice float64, clientOrderID string) (string, error)

	// PlaceTakeProfitMarketOrder places a take-profit market order that executes
	// when the trigger price is reached.
	// positionSide: "LONG", "SHORT", or "".
	// clientOrderID: 32-char idempotency key (pass "" to skip).
	PlaceTakeProfitMarketOrder(ctx context.Context, symbol string, side OrderSide, positionSide string, qty, triggerPrice float64, clientOrderID string) (string, error)

	// SetLeverage configures the leverage for a symbol (futures/swap only).
	// Spot brokers return an error or no-op.
	SetLeverage(ctx context.Context, symbol string, leverage int) error

	// PlaceReduceOnlyLimitOrder places a reduce-only GTC limit order.
	// Used for staged take-profit orders that close (not open) positions.
	// positionSide: "LONG", "SHORT", or "".
	// clientOrderID: 32-char idempotency key (pass "" to skip).
	PlaceReduceOnlyLimitOrder(ctx context.Context, symbol string, side OrderSide, positionSide string, qty, price float64, clientOrderID string) (string, error)

	// CancelOrder cancels a live order by exchange order ID.
	CancelOrder(ctx context.Context, symbol, exchangeID string) error

	// GetBalance returns the free balance for a given asset (e.g. "USDT", "BTC").
	GetBalance(ctx context.Context, asset string) (float64, error)
}

// UserDataSubscriber subscribes to real-time order/account/position updates from the exchange.
// Implemented by futures/swap brokers that support User Data Streams.
type UserDataSubscriber interface {
	SubscribeUserData(ctx context.Context,
		handler func(fill OrderFill, clientOrderID string, status string),
		accountHandler func(walletBalance float64, crossUnPnl float64),
		positionHandler func(symbol, side string, qty, entryPrice float64))
}
