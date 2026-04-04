package oms

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
)

// filledEps is the floating-point tolerance for determining if an order is
// fully filled (accounts for tiny rounding errors in exchange qty arithmetic).
const filledEps = 1e-9

// FillEvent carries a completed fill back to the engine and strategy.
type FillEvent struct {
	Order Order
	Fill  strategy.Fill
}

// OMS manages all orders in memory with full state-machine enforcement.
// It is safe for concurrent use.
type OMS struct {
	mu       sync.RWMutex
	orders   map[string]*Order
	fillsCh  chan FillEvent  // subscribers listen here
	ordersCh chan OrderEvent // order lifecycle events for persistence
	counter  atomic.Int64
	mode     TradingMode
	log      *zap.Logger
	ctxMu    sync.RWMutex    // protects ctx field
	ctx      context.Context // engine lifecycle context; set via SetContext before Run
}

// New creates an OMS operating in the given trading mode.
func New(mode TradingMode, log *zap.Logger) *OMS {
	return &OMS{
		orders:   make(map[string]*Order),
		fillsCh:  make(chan FillEvent, 4096),
		ordersCh: make(chan OrderEvent, 256),
		mode:     mode,
		log:      log,
		ctx:      context.Background(),
	}
}

// SetContext sets the engine lifecycle context. Publish methods will block
// until the channel drains or ctx is cancelled (engine shutdown), preventing
// silent event drops that desync position tracking from exchange reality.
// Safe to call concurrently.
func (o *OMS) SetContext(ctx context.Context) {
	o.ctxMu.Lock()
	o.ctx = ctx
	o.ctxMu.Unlock()
}

// engineCtx returns the current engine lifecycle context (thread-safe).
func (o *OMS) engineCtx() context.Context {
	o.ctxMu.RLock()
	c := o.ctx
	o.ctxMu.RUnlock()
	return c
}

// Fills returns a read-only channel that receives every fill event.
func (o *OMS) Fills() <-chan FillEvent { return o.fillsCh }

// Orders returns a read-only channel that receives every order lifecycle event.
// Consumers should drain this channel to avoid blocking the OMS.
func (o *OMS) Orders() <-chan OrderEvent { return o.ordersCh }

// ─── Order lifecycle ──────────────────────────────────────────────────────────

// Submit registers a new order from an OrderRequest.
// Returns the created Order (in PENDING state) or an error.
func (o *OMS) Submit(req strategy.OrderRequest, strategyID string) (*Order, error) {
	if req.Symbol == "" {
		return nil, fmt.Errorf("order symbol is empty")
	}
	if req.Side != strategy.SideBuy && req.Side != strategy.SideSell {
		return nil, fmt.Errorf("unknown order side: %s", req.Side)
	}
	// Qty == 0 is valid (all-in), but negative is never valid.
	if req.Qty < 0 {
		return nil, fmt.Errorf("order qty must be non-negative (got %f)", req.Qty)
	}
	if req.Price < 0 {
		return nil, fmt.Errorf("order price must be non-negative (got %f)", req.Price)
	}
	if req.StopPrice < 0 {
		return nil, fmt.Errorf("order stop price must be non-negative (got %f)", req.StopPrice)
	}
	// Limit orders require a positive price.
	if req.Type == strategy.OrderLimit && req.Price <= 0 {
		return nil, fmt.Errorf("limit order requires positive price (got %f)", req.Price)
	}
	// Stop-market orders require a positive stop price.
	if req.Type == strategy.OrderStopMarket && req.StopPrice <= 0 {
		return nil, fmt.Errorf("stop-market order requires positive stop price (got %f)", req.StopPrice)
	}

	id := o.nextID()
	// Generate a 32-char UUID (no dashes) as idempotency key for exchange submissions.
	// 32 chars fits OKX clOrdId limit exactly; Binance accepts any string ≤36 chars.
	clientOrderID := strings.ReplaceAll(uuid.New().String(), "-", "")
	now := time.Now()
	ord := &Order{
		ID:            id,
		ClientOrderID: clientOrderID,
		Symbol:        req.Symbol,
		Side:          req.Side,
		PositionSide:  req.PositionSide,
		Type:          req.Type,
		Status:        StatusPending,
		Mode:          o.mode,
		StrategyID:    strategyID,
		Qty:           req.Qty,
		Price:         req.Price,
		StopPrice:     req.StopPrice,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if ord.Type == "" {
		ord.Type = strategy.OrderMarket
	}

	o.mu.Lock()
	o.orders[id] = ord
	o.mu.Unlock()

	o.log.Debug("order submitted",
		zap.String("id", id),
		zap.String("client_order_id", clientOrderID),
		zap.String("symbol", req.Symbol),
		zap.String("side", string(req.Side)),
		zap.Float64("qty", req.Qty),
	)

	o.publishOrderEvent(*ord, "submitted")
	return ord, nil
}

// Accept transitions a PENDING order to OPEN.
func (o *OMS) Accept(id string) error {
	if err := o.transition(id, StatusOpen); err != nil {
		return err
	}
	if ord := o.Get(id); ord != nil {
		o.publishOrderEvent(*ord, "accepted")
	}
	return nil
}

// Reject moves a PENDING order to REJECTED with a reason.
func (o *OMS) Reject(id, reason string) error {
	o.mu.Lock()

	ord, ok := o.orders[id]
	if !ok {
		o.mu.Unlock()
		return fmt.Errorf("order %s not found", id)
	}
	if err := ord.TransitionTo(StatusRejected); err != nil {
		o.mu.Unlock()
		return err
	}
	ord.RejectReason = reason
	snapshot := *ord
	o.mu.Unlock()

	o.log.Warn("order rejected",
		zap.String("id", id),
		zap.String("reason", reason),
	)
	o.publishOrderEvent(snapshot, "rejected")
	return nil
}

// Fill records an execution against an OPEN order.
// If the order is fully filled it transitions to FILLED, otherwise PARTIAL.
// Publishes a FillEvent to the Fills() channel.
func (o *OMS) Fill(id string, fill strategy.Fill) error {
	o.mu.Lock()
	ord, ok := o.orders[id]
	if !ok {
		o.mu.Unlock()
		return fmt.Errorf("order %s not found", id)
	}

	if ord.Status != StatusOpen && ord.Status != StatusPartial {
		o.mu.Unlock()
		return fmt.Errorf("cannot fill order in state %s", ord.Status)
	}
	if fill.Qty <= 0 {
		o.mu.Unlock()
		return fmt.Errorf("fill qty must be positive (got %f)", fill.Qty)
	}

	// Update average fill price
	totalFilled := ord.FilledQty + fill.Qty
	ord.AvgFillPrice = (ord.FilledQty*ord.AvgFillPrice + fill.Qty*fill.Price) / totalFilled
	ord.FilledQty = totalFilled
	ord.Commission += fill.Fee

	var nextStatus OrderStatus
	if ord.FilledQty >= ord.Qty-filledEps {
		nextStatus = StatusFilled
	} else {
		nextStatus = StatusPartial
	}
	if err := ord.TransitionTo(nextStatus); err != nil {
		o.mu.Unlock()
		return err
	}

	snapshot := *ord // copy before releasing lock
	o.mu.Unlock()

	o.log.Info("order filled",
		zap.String("id", id),
		zap.String("status", string(nextStatus)),
		zap.Float64("filled_qty", fill.Qty),
		zap.Float64("price", fill.Price),
		zap.Float64("fee", fill.Fee),
	)

	// Blocking publish: wait for channel to drain or engine shutdown.
	// A dropped fill desyncs position tracking from exchange reality,
	// so we prefer backpressure over silent data loss.
	fe := FillEvent{Order: snapshot, Fill: fill}
	select {
	case o.fillsCh <- fe:
	default:
		// Channel full — log warning and block until space or ctx done.
		o.log.Warn("fills channel full, applying backpressure",
			zap.String("order_id", id),
			zap.Int("pending", len(o.fillsCh)),
			zap.Int("cap", cap(o.fillsCh)),
		)
		select {
		case o.fillsCh <- fe:
		case <-o.engineCtx().Done():
			o.log.Error("fills channel full and engine shutting down — fill event DROPPED",
				zap.String("order_id", id),
				zap.String("symbol", fill.Symbol),
				zap.String("side", string(fill.Side)),
			)
		}
	}

	o.publishOrderEvent(snapshot, "filled")
	return nil
}

// Cancel moves an open or pending order to CANCELLED.
func (o *OMS) Cancel(id string) error {
	if err := o.transition(id, StatusCancelled); err != nil {
		return err
	}
	if ord := o.Get(id); ord != nil {
		o.publishOrderEvent(*ord, "cancelled")
	}
	return nil
}

// SetExchangeID stores the exchange-assigned order ID (e.g. Binance OrderID)
// against an existing OMS order. Used by the live broker after exchange confirmation.
func (o *OMS) SetExchangeID(id, exchangeID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	ord, ok := o.orders[id]
	if !ok {
		return fmt.Errorf("order %s not found", id)
	}
	ord.ExchangeID = exchangeID
	return nil
}

// ─── Queries ──────────────────────────────────────────────────────────────────

// Get returns a snapshot of the order. Returns nil if not found.
func (o *OMS) Get(id string) *Order {
	o.mu.RLock()
	defer o.mu.RUnlock()
	ord, ok := o.orders[id]
	if !ok {
		return nil
	}
	snapshot := *ord
	return &snapshot
}

// OpenOrders returns all orders that are not in a terminal state.
func (o *OMS) OpenOrders() []Order {
	o.mu.RLock()
	defer o.mu.RUnlock()
	var result []Order
	for _, ord := range o.orders {
		if !ord.IsTerminal() {
			result = append(result, *ord)
		}
	}
	return result
}

// FindByClientOrderID returns a copy of the order with the given clientOrderID.
func (o *OMS) FindByClientOrderID(clientOrderID string) *Order {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, ord := range o.orders {
		if ord.ClientOrderID == clientOrderID {
			cp := *ord
			return &cp
		}
	}
	return nil
}

// FindPending returns a copy of the first non-terminal order matching the given
// symbol and side. Returns nil if no such order exists.
// Used by Broker.PlaceOrder to block duplicate orders (soft idempotency).
func (o *OMS) FindPending(symbol string, side strategy.Side) *Order {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, ord := range o.orders {
		if !ord.IsTerminal() && ord.Symbol == symbol && ord.Side == side {
			cp := *ord
			return &cp
		}
	}
	return nil
}

// PendingOrders returns copies of all non-terminal orders (PENDING, OPEN, PARTIAL).
// Used by Broker.CancelAllPendingOrders on engine shutdown.
func (o *OMS) PendingOrders() []Order {
	o.mu.RLock()
	defer o.mu.RUnlock()
	var result []Order
	for _, ord := range o.orders {
		if !ord.IsTerminal() {
			result = append(result, *ord)
		}
	}
	return result
}

// All returns all orders (for reporting).
func (o *OMS) All() []Order {
	o.mu.RLock()
	defer o.mu.RUnlock()
	result := make([]Order, 0, len(o.orders))
	for _, ord := range o.orders {
		result = append(result, *ord)
	}
	return result
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func (o *OMS) transition(id string, next OrderStatus) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	ord, ok := o.orders[id]
	if !ok {
		return fmt.Errorf("order %s not found", id)
	}
	return ord.TransitionTo(next)
}

func (o *OMS) nextID() string {
	n := o.counter.Add(1)
	return fmt.Sprintf("OMS-%06d", n)
}

// publishOrderEvent sends an OrderEvent to ordersCh with backpressure.
// Blocks until the channel drains or the engine context is cancelled.
func (o *OMS) publishOrderEvent(ord Order, event string) {
	oe := OrderEvent{Order: ord, Event: event}
	select {
	case o.ordersCh <- oe:
	default:
		o.log.Warn("orders channel full, applying backpressure",
			zap.String("order_id", ord.ID),
			zap.String("event", event),
			zap.Int("pending", len(o.ordersCh)),
		)
		select {
		case o.ordersCh <- oe:
		case <-o.engineCtx().Done():
			o.log.Error("orders channel full and engine shutting down — order event DROPPED",
				zap.String("order_id", ord.ID),
				zap.String("event", event),
			)
		}
	}
}

// SetRole sets the order's role field between Submit() and Accept() so that the
// Accept event (and subsequent UpsertOrder) carries the correct role.
// Intended for marking auto-placed protective orders ("stop_loss" | "take_profit").
func (o *OMS) SetRole(ordID, role string) {
	o.mu.Lock()
	if ord, ok := o.orders[ordID]; ok {
		ord.Role = role
	}
	o.mu.Unlock()
}

// Restore writes an order (with its existing status) directly into the OMS map
// without triggering ordersCh events. Used on engine restart to rebuild in-memory
// state from DB-persisted orders without causing duplicate persistence.
func (o *OMS) Restore(ord *Order) error {
	if ord == nil || ord.ID == "" {
		return fmt.Errorf("restore: nil or empty order")
	}
	o.mu.Lock()
	o.orders[ord.ID] = ord
	o.mu.Unlock()
	return nil
}

// PruneTerminal removes terminal orders (FILLED, CANCELLED, REJECTED) that
// were last updated more than maxAge ago. Call periodically (e.g. every 5 min)
// to prevent unbounded memory growth in long-running engines.
func (o *OMS) PruneTerminal(maxAge time.Duration) int {
	cutoff := time.Now().Add(-maxAge)
	o.mu.Lock()
	defer o.mu.Unlock()
	pruned := 0
	for id, ord := range o.orders {
		if ord.IsTerminal() && ord.UpdatedAt.Before(cutoff) {
			delete(o.orders, id)
			pruned++
		}
	}
	if pruned > 0 {
		o.log.Debug("pruned terminal orders from OMS",
			zap.Int("pruned", pruned),
			zap.Int("remaining", len(o.orders)),
		)
	}
	return pruned
}
