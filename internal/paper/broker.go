// Package paper implements paper trading: strategies run against live market
// data but no real orders are sent to any exchange.
package paper

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/risk"
	"github.com/Quantix/quantix/internal/strategy"
)

// pendingKind classifies a deferred paper order.
type pendingKind int

const (
	kindLimit      pendingKind = iota // LIMIT order — fills when price crosses limit
	kindStopMarket                    // STOP_MARKET — fills at market when stopPrice touched
	kindTakeProfit                    // auto-placed TP — fills when TP price touched
)

// pendingOrder holds a deferred paper order waiting for the price trigger.
type pendingOrder struct {
	ordID        string
	req          strategy.OrderRequest
	qty          float64     // resolved at submission
	triggerPrice float64     // limit / stop / TP price
	kind         pendingKind
}

// Broker implements strategy.Broker for paper trading.
//
// Market orders fill immediately at the current close price (± slippage).
// LIMIT and STOP_MARKET orders are deferred: they are stored as pending and
// checked on each bar via ProcessBar(high, low, close).
// Short positions are supported via PositionSide = "SHORT" (hedge mode).
type Broker struct {
	oms        *oms.OMS
	risk       *risk.Manager
	positions  *oms.PositionManager
	strategyID string
	feeRate    float64
	slippage   float64
	leverage   int // futures leverage; 0 or 1 = no leverage (spot)
	log        *zap.Logger

	lastPrice atomic.Value // float64
	equity    atomic.Value // float64
	cash      atomic.Value // float64

	pendingMu     sync.Mutex
	pendingOrders []pendingOrder
}

// NewBroker creates a paper broker.
// leverage: futures leverage multiplier (e.g. 10 = 10x); 0 or 1 = spot (full margin).
func NewBroker(
	o *oms.OMS,
	rm *risk.Manager,
	pm *oms.PositionManager,
	strategyID string,
	initialCash float64,
	feeRate, slippage float64,
	leverage int,
	log *zap.Logger,
) *Broker {
	if leverage < 1 {
		leverage = 1
	}
	b := &Broker{
		oms:        o,
		risk:       rm,
		positions:  pm,
		strategyID: strategyID,
		feeRate:    feeRate,
		slippage:   slippage,
		leverage:   leverage,
		log:        log,
	}
	b.cash.Store(initialCash)
	b.equity.Store(initialCash)
	b.lastPrice.Store(0.0)
	return b
}

// SetLastPrice updates the current market price used for synthetic fills.
func (b *Broker) SetLastPrice(price float64) { b.lastPrice.Store(price) }

// SetEquity updates current equity (called by engine after fills).
func (b *Broker) SetEquity(equity float64) { b.equity.Store(equity) }

// SetCash updates available cash (called by engine after fills).
func (b *Broker) SetCash(cash float64) { b.cash.Store(cash) }

// Cash returns current available cash.
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

// SetCashEquity atomically sets both cash and equity. Used at engine startup
// to restore from the most recent equity snapshot.
func (b *Broker) SetCashEquity(cash, equity float64) {
	b.cash.Store(cash)
	b.equity.Store(equity)
}

// SetPositions replaces the broker's PositionManager with a pre-populated one.
// Used at engine startup to restore open positions reconstructed from fills.
func (b *Broker) SetPositions(pm *oms.PositionManager) {
	b.positions = pm
}

// RestorePendingOrder re-queues a PENDING order from a DB record into the broker's
// pending orders list. The caller is responsible for marking the old DB record as
// CANCELLED before calling this method (to avoid client_order_id conflicts).
func (b *Broker) RestorePendingOrder(rec *data.OrderRecord) error {
	req := strategy.OrderRequest{
		Symbol:       rec.Symbol,
		Side:         strategy.Side(rec.Side),
		PositionSide: strategy.PositionSide(rec.PositionSide),
		Qty:          rec.Quantity,
	}

	var triggerPrice float64
	var kind pendingKind
	switch rec.Type {
	case string(strategy.OrderLimit):
		triggerPrice = rec.Price
		kind = kindLimit
	case string(strategy.OrderStopMarket):
		triggerPrice = rec.StopPrice
		kind = kindStopMarket
	case "TAKE_PROFIT_MARKET":
		triggerPrice = rec.StopPrice
		kind = kindTakeProfit
	default:
		return fmt.Errorf("unknown pending order type for recovery: %s", rec.Type)
	}
	if triggerPrice <= 0 {
		return fmt.Errorf("restored order has zero trigger price (type=%s)", rec.Type)
	}

	// Submit a fresh OMS order (new clientOrderID) and accept it as pending.
	ord, err := b.oms.Submit(req, b.strategyID)
	if err != nil {
		return fmt.Errorf("OMS submit for recovery: %w", err)
	}
	b.oms.Accept(ord.ID) //nolint:errcheck

	b.pendingMu.Lock()
	b.pendingOrders = append(b.pendingOrders, pendingOrder{
		ordID:        ord.ID,
		req:          req,
		qty:          rec.Quantity,
		triggerPrice: triggerPrice,
		kind:         kind,
	})
	b.pendingMu.Unlock()
	return nil
}

// PlaceOrder implements strategy.Broker.
// Market orders fill immediately; LIMIT and STOP_MARKET orders are deferred.
func (b *Broker) PlaceOrder(req strategy.OrderRequest) string {
	currentPrice := safeLoadFloat64(&b.lastPrice)
	if currentPrice <= 0 {
		b.log.Warn("PlaceOrder: no known price", zap.String("symbol", req.Symbol))
		return ""
	}

	equity := safeLoadFloat64(&b.equity)
	cash := safeLoadFloat64(&b.cash)

	// Current position value for risk check (net/long position)
	var posValue float64
	if pos, ok := b.positions.Position(req.Symbol); ok {
		posValue = pos.Qty * currentPrice
	}

	if err := b.risk.Check(req, equity, posValue, currentPrice); err != nil {
		b.log.Warn("order blocked by risk",
			zap.String("symbol", req.Symbol),
			zap.String("side", string(req.Side)),
			zap.Error(err))
		return ""
	}

	ord, err := b.oms.Submit(req, b.strategyID)
	if err != nil {
		b.log.Error("OMS submit failed", zap.Error(err))
		return ""
	}

	switch req.Type {
	case strategy.OrderLimit:
		return b.queuePending(ord.ID, req, cash, req.Price, kindLimit)
	case strategy.OrderStopMarket:
		return b.queuePending(ord.ID, req, cash, req.StopPrice, kindStopMarket)
	default: // Market or ""
		return b.executeMarket(ord, req, currentPrice, cash)
	}
}

// executeMarket fills a market order immediately and auto-places protective orders.
func (b *Broker) executeMarket(ord *oms.Order, req strategy.OrderRequest, currentPrice, cash float64) string {
	b.oms.Accept(ord.ID) //nolint:errcheck

	fill, err := b.syntheticFill(ord, currentPrice, cash)
	if err != nil {
		b.oms.Reject(ord.ID, err.Error()) //nolint:errcheck
		b.log.Warn("synthetic fill rejected",
			zap.String("order_id", ord.ID), zap.Error(err))
		return ""
	}

	b.applyCashForFill(fill)

	if err := b.oms.Fill(ord.ID, fill); err != nil {
		b.log.Error("OMS fill failed", zap.Error(err))
	}

	// Auto-place protective stop-loss / take-profit pending orders
	if isOpeningFill(req) {
		if req.StopLoss > 0 {
			b.addProtectivePending(req, fill.Qty, req.StopLoss, kindStopMarket)
		}
		if req.TakeProfit > 0 {
			b.addProtectivePending(req, fill.Qty, req.TakeProfit, kindTakeProfit)
		}
	}
	// Cancel outstanding protective orders when closing a position
	if isClosingFill(req) {
		b.cancelProtectivePending(req.Symbol, string(req.PositionSide))
	}

	return ord.ID
}

// queuePending stores a deferred order waiting for bar-level price trigger.
func (b *Broker) queuePending(ordID string, req strategy.OrderRequest, cash, triggerPrice float64, kind pendingKind) string {
	b.oms.Accept(ordID) //nolint:errcheck

	qty := req.Qty
	if qty == 0 {
		qty = b.resolveQty(req, cash)
		if qty <= 0 {
			b.oms.Reject(ordID, "cannot resolve qty for pending order") //nolint:errcheck
			return ""
		}
	}

	b.pendingMu.Lock()
	b.pendingOrders = append(b.pendingOrders, pendingOrder{
		ordID:        ordID,
		req:          req,
		qty:          qty,
		triggerPrice: triggerPrice,
		kind:         kind,
	})
	b.pendingMu.Unlock()

	b.log.Debug("pending order queued",
		zap.String("order_id", ordID),
		zap.String("symbol", req.Symbol),
		zap.String("kind", kindName(kind)),
		zap.Float64("trigger_price", triggerPrice),
	)
	return ordID
}

// addProtectivePending adds an auto-placed stop or TP protective pending order.
// The order is registered in OMS (Submit + SetRole + Accept) so that it is
// persisted to the database and can be recovered on restart.
func (b *Broker) addProtectivePending(orig strategy.OrderRequest, qty, triggerPrice float64, kind pendingKind) {
	// Protective orders always close the position: opposite side
	closeSide := strategy.SideSell
	if orig.PositionSide == strategy.PositionSideShort {
		closeSide = strategy.SideBuy
	}

	closeReq := strategy.OrderRequest{
		Symbol:       orig.Symbol,
		Side:         closeSide,
		PositionSide: orig.PositionSide,
		Type:         strategy.OrderStopMarket,
		Qty:          qty,
		StopPrice:    triggerPrice,
	}

	ord, err := b.oms.Submit(closeReq, b.strategyID)
	if err != nil {
		b.log.Error("protective order OMS submit failed", zap.Error(err))
		return
	}

	role := "stop_loss"
	if kind == kindTakeProfit {
		role = "take_profit"
	}
	b.oms.SetRole(ord.ID, role)
	b.oms.Accept(ord.ID) //nolint:errcheck

	b.pendingMu.Lock()
	b.pendingOrders = append(b.pendingOrders, pendingOrder{
		ordID:        ord.ID,
		req:          closeReq,
		qty:          qty,
		triggerPrice: triggerPrice,
		kind:         kind,
	})
	b.pendingMu.Unlock()
}

// cancelProtectivePending removes ALL protective (stop/TP) pending orders for a position.
// Used when a position is manually closed.
func (b *Broker) cancelProtectivePending(symbol, posSide string) {
	b.cancelPendingByKind(symbol, posSide, kindStopMarket)
	b.cancelPendingByKind(symbol, posSide, kindTakeProfit)
}

// cancelPendingByKind removes pending orders of a specific kind for a position.
// Used to cancel only the complementary protective order when the other fires.
func (b *Broker) cancelPendingByKind(symbol, posSide string, kind pendingKind) {
	b.pendingMu.Lock()
	defer b.pendingMu.Unlock()

	n := 0
	for _, p := range b.pendingOrders {
		if p.req.Symbol == symbol && string(p.req.PositionSide) == posSide && p.kind == kind {
			b.log.Debug("protective pending order cancelled",
				zap.String("order_id", p.ordID),
				zap.String("kind", kindName(p.kind)))
			continue
		}
		b.pendingOrders[n] = p
		n++
	}
	b.pendingOrders = b.pendingOrders[:n]
}

// ProcessBar checks all pending orders against the bar's high/low and triggers fills.
// Called by the engine before strategy.OnBar on each closed bar.
func (b *Broker) ProcessBar(high, low, _ float64) {
	b.pendingMu.Lock()
	var triggered []pendingOrder
	n := 0
	for _, p := range b.pendingOrders {
		if isTriggered(p, high, low) {
			triggered = append(triggered, p)
		} else {
			b.pendingOrders[n] = p
			n++
		}
	}
	b.pendingOrders = b.pendingOrders[:n]
	b.pendingMu.Unlock()

	for _, p := range triggered {
		b.executePendingFill(p)
	}
}

// isTriggered returns true when the bar's high/low crosses the order's trigger price.
func isTriggered(p pendingOrder, high, low float64) bool {
	switch p.kind {
	case kindLimit:
		if p.req.Side == strategy.SideBuy {
			return low <= p.triggerPrice // buy limit fills when price drops to limit
		}
		return high >= p.triggerPrice // sell limit fills when price rises to limit

	case kindStopMarket:
		if p.req.Side == strategy.SideSell {
			return low <= p.triggerPrice // stop-sell (close long) fires when price drops
		}
		return high >= p.triggerPrice // stop-buy (close short) fires when price rises

	case kindTakeProfit:
		if p.req.Side == strategy.SideSell {
			return high >= p.triggerPrice // TP-sell fires when price rises to TP
		}
		return low <= p.triggerPrice // TP-buy (short TP) fires when price drops to TP
	}
	return false
}

// executePendingFill executes a triggered pending order at the trigger price.
func (b *Broker) executePendingFill(p pendingOrder) {
	execPrice := p.triggerPrice
	switch p.req.Side {
	case strategy.SideBuy:
		execPrice *= (1 + b.slippage)
	case strategy.SideSell:
		execPrice *= (1 - b.slippage)
	}

	fill := strategy.Fill{
		ID:           p.ordID + "-fill",
		Symbol:       p.req.Symbol,
		Side:         p.req.Side,
		PositionSide: p.req.PositionSide,
		Qty:          p.qty,
		Price:        execPrice,
		Fee:          p.qty * execPrice * b.feeRate,
		Timestamp:    time.Now(),
	}

	b.applyCashForFill(fill)

	if err := b.oms.Fill(p.ordID, fill); err != nil {
		b.log.Warn("OMS fill for pending order failed",
			zap.String("order_id", p.ordID), zap.Error(err))
	}

	b.log.Info("pending order triggered and filled",
		zap.String("order_id", p.ordID),
		zap.String("symbol", p.req.Symbol),
		zap.String("kind", kindName(p.kind)),
		zap.Float64("trigger_price", p.triggerPrice),
		zap.Float64("exec_price", execPrice),
		zap.Float64("qty", p.qty),
	)

	// When a stop fires, cancel the complementary TP (and vice versa).
	// Do NOT call cancelProtectivePending which would cancel both.
	switch p.kind {
	case kindStopMarket:
		b.cancelPendingByKind(p.req.Symbol, string(p.req.PositionSide), kindTakeProfit)
	case kindTakeProfit:
		b.cancelPendingByKind(p.req.Symbol, string(p.req.PositionSide), kindStopMarket)
	}
}

// marginRate returns the initial margin rate as a fraction: 1/leverage.
// For spot (leverage=1) this is 1.0 (full notional); for 10x it is 0.10.
func (b *Broker) marginRate() float64 {
	return 1.0 / float64(b.leverage)
}

// applyCashForFill adjusts the cash balance after any fill.
// For futures (leverage > 1), only margin (notional / leverage) is locked/released.
// For spot (leverage = 1), full notional is used (marginRate = 1.0).
func (b *Broker) applyCashForFill(fill strategy.Fill) {
	prevCash := safeLoadFloat64(&b.cash)
	ps := string(fill.PositionSide)
	mr := b.marginRate()

	isOpeningLong := ps == string(strategy.PositionSideLong) && fill.Side == strategy.SideBuy
	isClosingLong := ps == string(strategy.PositionSideLong) && fill.Side == strategy.SideSell
	isOpeningShort := ps == string(strategy.PositionSideShort) && fill.Side == strategy.SideSell
	isClosingShort := ps == string(strategy.PositionSideShort) && fill.Side == strategy.SideBuy

	switch {
	case isOpeningLong:
		// Lock margin for long: notional * marginRate (spot: mr=1.0, futures: mr=1/leverage)
		b.cash.Store(prevCash - fill.Qty*fill.Price*mr - fill.Fee)
	case isClosingLong:
		// Return margin (realized PnL added separately in applyFillEvent)
		b.cash.Store(prevCash + fill.Qty*fill.Price*mr - fill.Fee)
	case isOpeningShort:
		b.cash.Store(prevCash - fill.Qty*fill.Price*mr - fill.Fee)
	case isClosingShort:
		b.cash.Store(prevCash + fill.Qty*fill.Price*mr - fill.Fee)
	case fill.Side == strategy.SideBuy:
		// Net/spot mode: full notional
		b.cash.Store(prevCash - fill.Qty*fill.Price - fill.Fee)
	case fill.Side == strategy.SideSell:
		b.cash.Store(prevCash + fill.Qty*fill.Price - fill.Fee)
	}
}

// CancelOrder cancels a pending or open order by OMS ID.
func (b *Broker) CancelOrder(id string) error {
	b.pendingMu.Lock()
	n := 0
	for _, p := range b.pendingOrders {
		if p.ordID != id {
			b.pendingOrders[n] = p
			n++
		}
	}
	b.pendingOrders = b.pendingOrders[:n]
	b.pendingMu.Unlock()

	return b.oms.Cancel(id)
}

// syntheticFill creates a paper fill for a market order.
func (b *Broker) syntheticFill(ord *oms.Order, currentPrice, cash float64) (strategy.Fill, error) {
	execPrice := currentPrice
	switch ord.Side {
	case strategy.SideBuy:
		execPrice *= (1 + b.slippage)
	case strategy.SideSell:
		execPrice *= (1 - b.slippage)
	}

	qty := ord.Qty
	ps := string(ord.PositionSide)

	switch {
	// Opening long or net buy
	case (ps == "" && ord.Side == strategy.SideBuy) ||
		(ps == string(strategy.PositionSideLong) && ord.Side == strategy.SideBuy):
		if qty == 0 {
			available := cash * 0.99
			if available <= 0 {
				return strategy.Fill{}, fmt.Errorf("insufficient cash: %.4f", cash)
			}
			qty = available / (execPrice * (1 + b.feeRate))
		}
		cost := qty * execPrice
		fee := cost * b.feeRate
		if cost+fee > cash {
			qty = cash / (execPrice * (1 + b.feeRate))
		}
		if qty <= 0 {
			return strategy.Fill{}, fmt.Errorf("zero quantity after scaling")
		}

	// Opening short (sell-short): use cash as margin (notional * marginRate)
	case ps == string(strategy.PositionSideShort) && ord.Side == strategy.SideSell:
		mr := b.marginRate()
		if qty == 0 {
			available := cash * 0.99
			if available <= 0 {
				return strategy.Fill{}, fmt.Errorf("insufficient cash for short margin: %.4f", cash)
			}
			qty = available / (execPrice * mr * (1 + b.feeRate))
		}
		// Margin required must not exceed cash
		marginRequired := qty * execPrice * mr
		if marginRequired > cash {
			qty = cash * 0.99 / (execPrice * mr)
		}
		if qty <= 0 {
			return strategy.Fill{}, fmt.Errorf("zero quantity for short after scaling")
		}

	// Closing long or net sell
	case (ps == "" && ord.Side == strategy.SideSell) ||
		(ps == string(strategy.PositionSideLong) && ord.Side == strategy.SideSell):
		pos, ok := b.positions.LongPosition(ord.Symbol)
		if !ok {
			// Fall back to net position
			netPos, netOk := b.positions.Position(ord.Symbol)
			if !netOk || netPos.Qty <= 0 {
				return strategy.Fill{}, fmt.Errorf("no long position to sell for %s", ord.Symbol)
			}
			pos.Qty = netPos.Qty
		}
		if pos.Qty <= 0 {
			return strategy.Fill{}, fmt.Errorf("no long position to sell for %s", ord.Symbol)
		}
		if qty == 0 || qty > pos.Qty {
			qty = pos.Qty
		}

	// Closing short (buy-to-cover)
	case ps == string(strategy.PositionSideShort) && ord.Side == strategy.SideBuy:
		pos, ok := b.positions.ShortPosition(ord.Symbol)
		if !ok || pos.Qty <= 0 {
			return strategy.Fill{}, fmt.Errorf("no short position to cover for %s", ord.Symbol)
		}
		if qty == 0 || qty > pos.Qty {
			qty = pos.Qty
		}

	default:
		return strategy.Fill{}, fmt.Errorf("cannot resolve qty for side=%s positionSide=%s", ord.Side, ps)
	}

	return strategy.Fill{
		ID:           ord.ID + "-fill",
		Symbol:       ord.Symbol,
		Side:         ord.Side,
		PositionSide: ord.PositionSide,
		Qty:          qty,
		Price:        execPrice,
		Fee:          qty * execPrice * b.feeRate,
		Timestamp:    time.Now(),
	}, nil
}

// resolveQty estimates order quantity when req.Qty == 0 for a pending order.
func (b *Broker) resolveQty(req strategy.OrderRequest, cash float64) float64 {
	lp := safeLoadFloat64(&b.lastPrice)
	if lp <= 0 {
		return 0
	}
	ps := string(req.PositionSide)
	switch {
	case (ps == "" && req.Side == strategy.SideBuy) ||
		(ps == string(strategy.PositionSideLong) && req.Side == strategy.SideBuy):
		return cash * 0.99 / lp

	case ps == string(strategy.PositionSideShort) && req.Side == strategy.SideSell:
		return cash * 0.99 / (lp * b.marginRate())

	case (ps == "" && req.Side == strategy.SideSell) ||
		(ps == string(strategy.PositionSideLong) && req.Side == strategy.SideSell):
		if pos, ok := b.positions.LongPosition(req.Symbol); ok {
			return pos.Qty
		}
		if netPos, ok := b.positions.Position(req.Symbol); ok {
			return netPos.Qty
		}
		return 0

	case ps == string(strategy.PositionSideShort) && req.Side == strategy.SideBuy:
		if pos, ok := b.positions.ShortPosition(req.Symbol); ok {
			return pos.Qty
		}
		return 0
	}
	return 0
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func isOpeningFill(req strategy.OrderRequest) bool {
	ps := string(req.PositionSide)
	return (ps == string(strategy.PositionSideLong) && req.Side == strategy.SideBuy) ||
		(ps == string(strategy.PositionSideShort) && req.Side == strategy.SideSell) ||
		(ps == "" && req.Side == strategy.SideBuy)
}

func isClosingFill(req strategy.OrderRequest) bool {
	return !isOpeningFill(req)
}

func kindName(k pendingKind) string {
	switch k {
	case kindLimit:
		return "LIMIT"
	case kindStopMarket:
		return "STOP_MARKET"
	case kindTakeProfit:
		return "TAKE_PROFIT"
	}
	return "UNKNOWN"
}
