package aistrat

import "time"

// ─── Position ────────────────────────────────────────────────────────────────

type posMode int
const (
	modeTrend posMode = iota
	modeRange
)

type posState struct {
	side       string  // "LONG" or "SHORT"
	mode       posMode
	entryPrice float64
	initQty    float64
	remainQty  float64
	R          float64
	stopLoss   float64
	takeProfit float64 // range mode: fixed TP price
	trailing   float64
	peakPrice  float64
	tp1RHit    bool
	barsHeld   int
	filled     bool
	filledAt   time.Time
	orderID    string
	limitBar   int

	// Staged TP (trend mode): exchange-native limit orders
	stagedTPPlaced bool // true once SL + TP orders are on the exchange
	breakevenMoved bool // true once SL has been moved to breakeven at +0.5R

	// Grid orders (range mode only)
	gridOrders []*gridOrder
}

type gridOrder struct {
	entryPrice float64
	qty        float64
	tp         float64 // take-profit price
	filled     bool
	filledAt   time.Time
	orderID    string
	limitBar   int
}
