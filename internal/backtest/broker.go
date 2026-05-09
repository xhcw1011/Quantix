package backtest

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// pendingOrder pairs a queued order with its assigned ID.
type pendingOrder struct {
	id  string
	req strategy.OrderRequest
}

// stopRecord tracks an active stop-loss for a single open position.
// Phase 2 simplification: one stop per symbol; opening another position
// with a stop on the same symbol overwrites the old one.
type stopRecord struct {
	posSide strategy.PositionSide // "" or LONG = long; SHORT = short
	price   float64
	qty     float64
}

// SimBroker is a simulated broker for backtesting.
// Orders are queued when the strategy calls PlaceOrder, then matched at
// the close price of the next bar (next-bar-open approximation via close).
type SimBroker struct {
	FeeRate  float64 // commission rate, e.g. 0.001 = 0.1%
	Slippage float64 // one-way slippage fraction, e.g. 0.0005

	pending     []pendingOrder
	cancelled   map[string]bool // IDs that have been cancelled
	submitted   map[string]bool // all IDs ever submitted (for validation)
	activeStops map[string]*stopRecord
	portfolio   *Portfolio
	log         *zap.Logger
	nextID      int
}

// NewSimBroker creates a broker wired to the given portfolio.
func NewSimBroker(feeRate, slippage float64, portfolio *Portfolio, log *zap.Logger) *SimBroker {
	return &SimBroker{
		FeeRate:     feeRate,
		Slippage:    slippage,
		portfolio:   portfolio,
		log:         log,
		cancelled:   make(map[string]bool),
		submitted:   make(map[string]bool),
		activeStops: make(map[string]*stopRecord),
	}
}

// PlaceOrder queues an order for execution on the next bar and returns its ID.
// Implements strategy.Broker.
func (b *SimBroker) PlaceOrder(req strategy.OrderRequest) string {
	if req.Type == "" {
		req.Type = strategy.OrderMarket
	}
	b.nextID++
	id := fmt.Sprintf("sim-%d", b.nextID)
	b.submitted[id] = true
	b.pending = append(b.pending, pendingOrder{id: id, req: req})
	return id
}

// CancelOrder marks a pending order as cancelled so it is skipped during Process.
// Returns an error if the order ID was never submitted.
func (b *SimBroker) CancelOrder(id string) error {
	if !b.submitted[id] {
		return fmt.Errorf("order %s not found", id)
	}
	b.cancelled[id] = true
	return nil
}

// Process matches all pending orders against the given bar's close price.
// Returns the list of fills generated. Should be called once per bar,
// AFTER the strategy's OnBar has been invoked.
//
// Order of operations:
//  1. Check active stop-losses against this bar's high/low. Triggered stops
//     emit close fills FIRST (chronologically a stop trips intra-bar before
//     a new entry executes at close).
//  2. Execute pending orders queued during the prior bar's OnBar.
//  3. For each entry fill, register a new stop if req.StopLoss != 0;
//     for each closing fill, clear any active stop on the symbol.
func (b *SimBroker) Process(bar exchange.Kline) []strategy.Fill {
	// 1. Stops first.
	fills := b.checkStops(bar)

	// 2. Pending orders.
	if len(b.pending) > 0 {
		orders := b.pending
		b.pending = nil
		for _, po := range orders {
			if b.cancelled[po.id] {
				b.log.Debug("skipping cancelled order", zap.String("id", po.id))
				continue
			}
			fill, err := b.execute(po.req, bar)
			if err != nil {
				b.log.Warn("order rejected",
					zap.String("symbol", po.req.Symbol),
					zap.String("side", string(po.req.Side)),
					zap.Error(err))
				continue
			}
			fills = append(fills, fill)

			// Update portfolio
			if trade := b.portfolio.applyFill(fill, bar.CloseTime); trade != nil {
				b.portfolio.Trades = append(b.portfolio.Trades, *trade)
			}

			// 3. Register / clear active stop for this symbol.
			if b.isOpeningFill(fill) {
				if po.req.StopLoss != 0 {
					b.activeStops[fill.Symbol] = &stopRecord{
						posSide: po.req.PositionSide,
						price:   po.req.StopLoss,
						qty:     fill.Qty,
					}
				}
			} else {
				// Closing fill — clear any active stop for this symbol.
				delete(b.activeStops, fill.Symbol)
			}

			b.log.Debug("fill",
				zap.String("id", fill.ID),
				zap.String("symbol", fill.Symbol),
				zap.String("side", string(fill.Side)),
				zap.Float64("qty", fill.Qty),
				zap.Float64("price", fill.Price),
				zap.Float64("fee", fill.Fee))
		}
	}
	return fills
}

// execute simulates order execution at the bar's close price with slippage.
//
// Routes by req.PositionSide:
//   - "" or LONG: spot/LONG path. SideBuy opens/adds, SideSell closes against
//     existing long position. Buy uses cash; rejected if insufficient.
//   - SHORT: futures hedge SHORT path. SideSell opens/adds (no cash check —
//     futures margin), SideBuy closes against existing short position.
func (b *SimBroker) execute(req strategy.OrderRequest, bar exchange.Kline) (strategy.Fill, error) {
	if req.PositionSide == strategy.PositionSideShort {
		return b.executeShort(req, bar)
	}
	return b.executeLong(req, bar)
}

func (b *SimBroker) executeLong(req strategy.OrderRequest, bar exchange.Kline) (strategy.Fill, error) {
	execPrice := bar.Close
	switch req.Side {
	case strategy.SideBuy:
		execPrice *= (1 + b.Slippage)
	case strategy.SideSell:
		execPrice *= (1 - b.Slippage)
	}

	qty := req.Qty
	switch req.Side {
	case strategy.SideBuy:
		if qty == 0 {
			available := b.portfolio.cash * 0.99
			if available <= 0 {
				return strategy.Fill{}, fmt.Errorf("insufficient cash: %.4f", b.portfolio.cash)
			}
			qty = available / execPrice
		}
		cost := qty * execPrice
		fee := cost * b.FeeRate
		if cost+fee > b.portfolio.cash {
			qty = b.portfolio.cash / (execPrice * (1 + b.FeeRate))
			cost = qty * execPrice
			fee = cost * b.FeeRate
		}
		if qty <= 0 {
			return strategy.Fill{}, fmt.Errorf("order qty is zero after scaling")
		}
		b.nextID++
		return strategy.Fill{
			ID:           fmt.Sprintf("sim-%d", b.nextID),
			Symbol:       req.Symbol,
			Side:         strategy.SideBuy,
			PositionSide: req.PositionSide,
			Qty:          qty,
			Price:        execPrice,
			Fee:          fee,
			Timestamp:    bar.CloseTime,
		}, nil

	case strategy.SideSell:
		pos, exists := b.portfolio.longPositions[req.Symbol]
		if !exists || pos.Qty <= 0 {
			return strategy.Fill{}, fmt.Errorf("no long position to sell for %s", req.Symbol)
		}
		if qty == 0 || qty > pos.Qty {
			qty = pos.Qty
		}
		proceeds := qty * execPrice
		fee := proceeds * b.FeeRate
		b.nextID++
		return strategy.Fill{
			ID:           fmt.Sprintf("sim-%d", b.nextID),
			Symbol:       req.Symbol,
			Side:         strategy.SideSell,
			PositionSide: req.PositionSide,
			Qty:          qty,
			Price:        execPrice,
			Fee:          fee,
			Timestamp:    bar.CloseTime,
		}, nil
	}
	return strategy.Fill{}, fmt.Errorf("unknown side: %s", req.Side)
}

func (b *SimBroker) executeShort(req strategy.OrderRequest, bar exchange.Kline) (strategy.Fill, error) {
	execPrice := bar.Close
	switch req.Side {
	case strategy.SideBuy:
		execPrice *= (1 + b.Slippage)
	case strategy.SideSell:
		execPrice *= (1 - b.Slippage)
	}

	qty := req.Qty
	switch req.Side {
	case strategy.SideSell:
		// Open / add to SHORT. Margin assumed sufficient (no cash check).
		if qty <= 0 {
			return strategy.Fill{}, fmt.Errorf("short open requires explicit qty")
		}
		fee := qty * execPrice * b.FeeRate
		b.nextID++
		return strategy.Fill{
			ID:           fmt.Sprintf("sim-%d", b.nextID),
			Symbol:       req.Symbol,
			Side:         strategy.SideSell,
			PositionSide: strategy.PositionSideShort,
			Qty:          qty,
			Price:        execPrice,
			Fee:          fee,
			Timestamp:    bar.CloseTime,
		}, nil

	case strategy.SideBuy:
		pos, exists := b.portfolio.shortPositions[req.Symbol]
		if !exists || pos.Qty <= 0 {
			return strategy.Fill{}, fmt.Errorf("no short position to cover for %s", req.Symbol)
		}
		if qty == 0 || qty > pos.Qty {
			qty = pos.Qty
		}
		fee := qty * execPrice * b.FeeRate
		b.nextID++
		return strategy.Fill{
			ID:           fmt.Sprintf("sim-%d", b.nextID),
			Symbol:       req.Symbol,
			Side:         strategy.SideBuy,
			PositionSide: strategy.PositionSideShort,
			Qty:          qty,
			Price:        execPrice,
			Fee:          fee,
			Timestamp:    bar.CloseTime,
		}, nil
	}
	return strategy.Fill{}, fmt.Errorf("unknown side: %s", req.Side)
}

// isOpeningFill returns true when the fill represents opening or adding to
// a position (vs closing). Long opens via SideBuy; short opens via SideSell.
func (b *SimBroker) isOpeningFill(fill strategy.Fill) bool {
	if fill.PositionSide == strategy.PositionSideShort {
		return fill.Side == strategy.SideSell
	}
	return fill.Side == strategy.SideBuy
}

// checkStops scans active stops against the current bar's high/low and
// emits close fills for any that triggered. Triggered stops are removed.
// Long stop trips when bar.Low <= stop.price; short when bar.High >= stop.price.
// Close price is the stop price with slippage (worst-case execution).
func (b *SimBroker) checkStops(bar exchange.Kline) []strategy.Fill {
	if len(b.activeStops) == 0 {
		return nil
	}
	var fills []strategy.Fill
	for sym, sr := range b.activeStops {
		// Stops are per-symbol; only check the bar's symbol.
		if bar.Symbol != "" && bar.Symbol != sym {
			continue
		}
		var (
			triggered bool
			closeSide strategy.Side
			execPrice float64
		)
		if sr.posSide == strategy.PositionSideShort {
			if bar.High >= sr.price {
				triggered = true
				closeSide = strategy.SideBuy
				execPrice = sr.price * (1 + b.Slippage) // BUY pays a bit more
			}
		} else {
			if bar.Low <= sr.price {
				triggered = true
				closeSide = strategy.SideSell
				execPrice = sr.price * (1 - b.Slippage) // SELL receives a bit less
			}
		}
		if !triggered {
			continue
		}
		fee := sr.qty * execPrice * b.FeeRate
		b.nextID++
		fill := strategy.Fill{
			ID:           fmt.Sprintf("sim-%d", b.nextID),
			Symbol:       sym,
			Side:         closeSide,
			PositionSide: sr.posSide,
			Qty:          sr.qty,
			Price:        execPrice,
			Fee:          fee,
			Timestamp:    bar.CloseTime,
		}
		fills = append(fills, fill)
		if trade := b.portfolio.applyFill(fill, bar.CloseTime); trade != nil {
			b.portfolio.Trades = append(b.portfolio.Trades, *trade)
		}
		delete(b.activeStops, sym)
	}
	return fills
}
