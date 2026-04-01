package backtest

import (
	"time"

	"github.com/Quantix/quantix/internal/strategy"
)

// Position represents an open holding in a single symbol.
type Position struct {
	Symbol     string
	Qty        float64
	AvgPrice   float64
	Side       strategy.Side
	OpenedAt   time.Time
}

// Trade records a completed round-trip (entry + exit).
type Trade struct {
	Symbol     string
	Side       strategy.Side
	EntryTime  time.Time
	ExitTime   time.Time
	EntryPrice float64
	ExitPrice  float64
	Qty        float64
	GrossPnL   float64
	Fee        float64
	NetPnL     float64
	PnLPct     float64
}

// EquityPoint records portfolio value at a point in time.
type EquityPoint struct {
	Time   time.Time
	Equity float64
	Cash   float64
}

// Portfolio tracks cash, open positions, trade history and equity curve.
type Portfolio struct {
	initialCapital float64
	cash           float64
	positions      map[string]*Position // symbol → position
	Trades         []Trade
	EquityCurve    []EquityPoint
}

// NewPortfolio creates a portfolio with the given starting capital.
func NewPortfolio(capital float64) *Portfolio {
	return &Portfolio{
		initialCapital: capital,
		cash:           capital,
		positions:      make(map[string]*Position),
	}
}

// InitialCapital returns the starting capital.
func (p *Portfolio) InitialCapital() float64 { return p.initialCapital }

// ─── PortfolioView interface (used by strategy.Context) ───────────────────────

func (p *Portfolio) Cash() float64 { return p.cash }

func (p *Portfolio) Position(symbol string) (qty, avgPrice float64, ok bool) {
	pos, exists := p.positions[symbol]
	if !exists {
		return 0, 0, false
	}
	return pos.Qty, pos.AvgPrice, true
}

func (p *Portfolio) Equity(prices map[string]float64) float64 {
	total := p.cash
	for sym, pos := range p.positions {
		if price, ok := prices[sym]; ok {
			total += pos.Qty * price
		} else {
			total += pos.Qty * pos.AvgPrice // fallback to entry price
		}
	}
	return total
}

// ─── Internal methods used by SimBroker ───────────────────────────────────────

// applyFill updates cash and positions after an order is filled.
// Returns a completed Trade if this fill closes a position.
func (p *Portfolio) applyFill(fill strategy.Fill, barTime time.Time) *Trade {
	switch fill.Side {
	case strategy.SideBuy:
		cost := fill.Qty*fill.Price + fill.Fee
		p.cash -= cost

		pos, exists := p.positions[fill.Symbol]
		if !exists {
			pos = &Position{
				Symbol:   fill.Symbol,
				Side:     strategy.SideBuy,
				OpenedAt: barTime,
			}
			p.positions[fill.Symbol] = pos
		}
		// Average up
		totalQty := pos.Qty + fill.Qty
		pos.AvgPrice = (pos.Qty*pos.AvgPrice + fill.Qty*fill.Price) / totalQty
		pos.Qty = totalQty
		return nil

	case strategy.SideSell:
		pos, exists := p.positions[fill.Symbol]
		if !exists {
			return nil
		}

		proceeds := fill.Qty * fill.Price
		p.cash += proceeds - fill.Fee

		gross := fill.Qty * (fill.Price - pos.AvgPrice)
		var pnlPct float64
		if pos.AvgPrice > 0 {
			pnlPct = (fill.Price - pos.AvgPrice) / pos.AvgPrice * 100
		}

		trade := &Trade{
			Symbol:     fill.Symbol,
			Side:       strategy.SideBuy, // was long
			EntryTime:  pos.OpenedAt,
			ExitTime:   barTime,
			EntryPrice: pos.AvgPrice,
			ExitPrice:  fill.Price,
			Qty:        fill.Qty,
			GrossPnL:   gross,
			Fee:        fill.Fee,
			NetPnL:     gross - fill.Fee,
			PnLPct:     pnlPct,
		}

		pos.Qty -= fill.Qty
		if pos.Qty <= 1e-10 {
			delete(p.positions, fill.Symbol)
		}
		return trade
	}
	return nil
}

// recordEquity appends an equity snapshot to the curve.
func (p *Portfolio) recordEquity(t time.Time, prices map[string]float64) {
	p.EquityCurve = append(p.EquityCurve, EquityPoint{
		Time:   t,
		Equity: p.Equity(prices),
		Cash:   p.cash,
	})
}
