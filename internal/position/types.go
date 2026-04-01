// Package position provides a PositionSyncer that keeps strategy state
// in sync with exchange positions via Redis (real-time) + DB (backup).
package position

import "time"

// ExchangePosition represents a position as reported by the exchange.
type ExchangePosition struct {
	Symbol        string  `json:"symbol"`
	Side          string  `json:"side"` // "LONG" or "SHORT"
	Qty           float64 `json:"qty"`
	EntryPrice    float64 `json:"entry_price"`
	MarkPrice     float64 `json:"mark_price"`
	UnrealizedPnL float64 `json:"unrealized_pnl"`
	Leverage      int     `json:"leverage"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// StrategyPosition extends ExchangePosition with strategy-specific state.
type StrategyPosition struct {
	ExchangePosition

	Mode       string  `json:"mode"`        // "trend" or "range"
	StopLoss   float64 `json:"stop_loss"`
	TakeProfit float64 `json:"take_profit"`
	Trailing   float64 `json:"trailing"`
	PeakPrice  float64 `json:"peak_price"`
	R          float64 `json:"r"`
	InitQty    float64 `json:"init_qty"`
	TP1Hit     bool    `json:"tp1_hit"`
	TP2Hit     bool    `json:"tp2_hit"`
	BarsHeld   int     `json:"bars_held"`
	OrderID    string  `json:"order_id"`
	Filled     bool    `json:"filled"`
}

// PositionEvent describes a change detected by the syncer.
type PositionEvent struct {
	Type     string           // "opened", "closed", "modified", "external_close", "external_open"
	Position ExchangePosition
}
