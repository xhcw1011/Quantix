// Package oms implements the Order Management System.
// It tracks the full lifecycle of every order and provides thread-safe
// access to order state from strategies, the paper engine, and the UI.
package oms

import (
	"errors"
	"time"

	"github.com/Quantix/quantix/internal/strategy"
)

// OrderStatus represents the lifecycle state of an order.
type OrderStatus string

const (
	StatusPending   OrderStatus = "PENDING"   // submitted, not yet accepted by exchange
	StatusOpen      OrderStatus = "OPEN"      // accepted, waiting to fill
	StatusFilled    OrderStatus = "FILLED"    // fully executed
	StatusPartial   OrderStatus = "PARTIAL"   // partially filled, still open
	StatusCancelled OrderStatus = "CANCELLED" // cancelled before fill
	StatusRejected  OrderStatus = "REJECTED"  // refused by risk or exchange
)

// TradingMode distinguishes paper from live orders.
type TradingMode string

const (
	ModePaper TradingMode = "paper"
	ModeLive  TradingMode = "live"
)

// OrderEvent carries an order state-change notification to persistence listeners.
type OrderEvent struct {
	Order Order
	Event string // "submitted" | "accepted" | "filled" | "cancelled" | "rejected"
}

// Order represents one order in the system with its full lifecycle state.
type Order struct {
	ID            string
	ClientOrderID string // 32-char UUID without dashes; set in Submit() for idempotency
	ExchangeID    string // set when live order is acknowledged by exchange
	Symbol        string
	Side         strategy.Side
	PositionSide strategy.PositionSide // hedge mode direction: "LONG", "SHORT", or "" (net/spot)
	Type         strategy.OrderType
	Status       OrderStatus
	Mode         TradingMode
	StrategyID   string
	Qty          float64 // requested quantity
	Price        float64 // limit price (0 for market orders)
	StopPrice    float64 // stop trigger price for STOP_MARKET / STOP_LIMIT orders
	FilledQty    float64
	AvgFillPrice float64
	Commission   float64
	RejectReason string
	// Role distinguishes auto-placed protective orders from regular entry orders.
	// "" = normal entry order; "stop_loss" | "take_profit" = auto-placed by broker.
	Role      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ─── State machine transitions ────────────────────────────────────────────────

// ErrInvalidTransition is returned when a state change is not allowed.
var ErrInvalidTransition = errors.New("invalid order status transition")

// validTransitions defines which status changes are permitted.
var validTransitions = map[OrderStatus][]OrderStatus{
	StatusPending:   {StatusOpen, StatusRejected, StatusCancelled},
	StatusOpen:      {StatusFilled, StatusPartial, StatusCancelled},
	StatusPartial:   {StatusFilled, StatusCancelled},
	StatusFilled:    {}, // terminal
	StatusCancelled: {}, // terminal
	StatusRejected:  {}, // terminal
}

// CanTransitionTo returns true if transitioning from current status to next is valid.
func (o *Order) CanTransitionTo(next OrderStatus) bool {
	for _, allowed := range validTransitions[o.Status] {
		if allowed == next {
			return true
		}
	}
	return false
}

// TransitionTo applies a state change, returning an error if not allowed.
func (o *Order) TransitionTo(next OrderStatus) error {
	if !o.CanTransitionTo(next) {
		return ErrInvalidTransition
	}
	o.Status = next
	o.UpdatedAt = time.Now()
	return nil
}

// IsTerminal returns true if the order is in a final state.
func (o *Order) IsTerminal() bool {
	switch o.Status {
	case StatusFilled, StatusCancelled, StatusRejected:
		return true
	}
	return false
}

// RemainingQty returns how much of the order has not yet been filled.
func (o *Order) RemainingQty() float64 {
	return o.Qty - o.FilledQty
}
