package backtest

import (
	"time"

	"github.com/Quantix/quantix/internal/strategy"
)

// Position represents an open holding in a single symbol.
//
// For spot strategies (PositionSide=""), this tracks shares bought with cash:
// open = cash debit by full notional, close = cash credit by full notional.
//
// For futures hedge mode, the portfolio tracks LONG and SHORT independently.
// SHORT cash accounting is PnL-only (no notional debit on open) so that
// initial capital represents margin, not asset value.
type Position struct {
	Symbol   string
	Qty      float64
	AvgPrice float64
	Side     strategy.Side
	OpenedAt time.Time
}

// Trade records a completed round-trip (entry + exit).
// PositionSide identifies LONG vs SHORT; Side reflects the closing order.
type Trade struct {
	Symbol       string
	Side         strategy.Side
	PositionSide strategy.PositionSide
	EntryTime    time.Time
	ExitTime     time.Time
	EntryPrice   float64
	ExitPrice    float64
	Qty          float64
	GrossPnL     float64
	Fee          float64
	NetPnL       float64
	PnLPct       float64
}

// EquityPoint records portfolio value at a point in time.
type EquityPoint struct {
	Time   time.Time
	Equity float64
	Cash   float64
}

// Portfolio tracks cash, open positions, trade history and equity curve.
//
// LONG positions use spot-style accounting: full notional debits cash on open,
// full notional credits cash on close. This preserves backwards compatibility
// with spot strategies (grid, macross, meanreversion, mlstrat) that read
// Cash() and Position().
//
// SHORT positions use futures-style accounting: only fees move cash on
// open/close, and realized PnL ((entry-exit)*qty) credits cash on close.
// Unrealized SHORT PnL is included in Equity() for mark-to-market.
type Portfolio struct {
	initialCapital float64
	cash           float64
	longPositions  map[string]*Position // symbol → LONG position (spot/futures)
	shortPositions map[string]*Position // symbol → SHORT position (futures only)
	Trades         []Trade
	EquityCurve    []EquityPoint
}

// NewPortfolio creates a portfolio with the given starting capital.
func NewPortfolio(capital float64) *Portfolio {
	return &Portfolio{
		initialCapital: capital,
		cash:           capital,
		longPositions:  make(map[string]*Position),
		shortPositions: make(map[string]*Position),
	}
}

// InitialCapital returns the starting capital.
func (p *Portfolio) InitialCapital() float64 { return p.initialCapital }

// ─── PortfolioView interface (used by strategy.Context) ───────────────────────

func (p *Portfolio) Cash() float64 { return p.cash }

// Position returns the LONG position for the given symbol. Spot strategies
// (which never go SHORT) consume this; SHORT lookups are not supported via
// this signature to keep the spot interface backwards-compatible.
func (p *Portfolio) Position(symbol string) (qty, avgPrice float64, ok bool) {
	pos, exists := p.longPositions[symbol]
	if !exists {
		return 0, 0, false
	}
	return pos.Qty, pos.AvgPrice, true
}

// Equity returns total portfolio value: cash + LONG market value + SHORT
// unrealized PnL. SHORT contributes (entry-current_price)*qty since cash
// did not absorb the notional on open.
func (p *Portfolio) Equity(prices map[string]float64) float64 {
	total := p.cash
	for sym, pos := range p.longPositions {
		px, ok := prices[sym]
		if !ok {
			px = pos.AvgPrice
		}
		total += pos.Qty * px
	}
	for sym, pos := range p.shortPositions {
		px, ok := prices[sym]
		if !ok {
			px = pos.AvgPrice
		}
		total += pos.Qty * (pos.AvgPrice - px) // unrealized SHORT pnl
	}
	return total
}

// ─── Internal methods used by SimBroker ───────────────────────────────────────

// applyFill updates cash and positions after an order is filled.
// Returns a completed Trade if this fill closes a position.
//
// Routing by PositionSide:
//   - "" or LONG: spot/LONG path — buy debits cash by notional, sell credits.
//   - SHORT: futures SHORT path — sell to open (no notional cash change),
//     buy to cover (realize PnL into cash).
func (p *Portfolio) applyFill(fill strategy.Fill, barTime time.Time) *Trade {
	if fill.PositionSide == strategy.PositionSideShort {
		return p.applyShortFill(fill, barTime)
	}
	return p.applyLongFill(fill, barTime)
}

func (p *Portfolio) applyLongFill(fill strategy.Fill, barTime time.Time) *Trade {
	switch fill.Side {
	case strategy.SideBuy:
		cost := fill.Qty*fill.Price + fill.Fee
		p.cash -= cost

		pos, exists := p.longPositions[fill.Symbol]
		if !exists {
			pos = &Position{
				Symbol:   fill.Symbol,
				Side:     strategy.SideBuy,
				OpenedAt: barTime,
			}
			p.longPositions[fill.Symbol] = pos
		}
		totalQty := pos.Qty + fill.Qty
		pos.AvgPrice = (pos.Qty*pos.AvgPrice + fill.Qty*fill.Price) / totalQty
		pos.Qty = totalQty
		return nil

	case strategy.SideSell:
		pos, exists := p.longPositions[fill.Symbol]
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
			Symbol:       fill.Symbol,
			Side:         strategy.SideSell,
			PositionSide: strategy.PositionSideLong,
			EntryTime:    pos.OpenedAt,
			ExitTime:     barTime,
			EntryPrice:   pos.AvgPrice,
			ExitPrice:    fill.Price,
			Qty:          fill.Qty,
			GrossPnL:     gross,
			Fee:          fill.Fee,
			NetPnL:       gross - fill.Fee,
			PnLPct:       pnlPct,
		}

		pos.Qty -= fill.Qty
		if pos.Qty <= 1e-10 {
			delete(p.longPositions, fill.Symbol)
		}
		return trade
	}
	return nil
}

func (p *Portfolio) applyShortFill(fill strategy.Fill, barTime time.Time) *Trade {
	switch fill.Side {
	case strategy.SideSell:
		// Open or add to SHORT. Futures-style: only fee moves cash; the
		// notional is implicitly margined.
		p.cash -= fill.Fee

		pos, exists := p.shortPositions[fill.Symbol]
		if !exists {
			pos = &Position{
				Symbol:   fill.Symbol,
				Side:     strategy.SideSell,
				OpenedAt: barTime,
			}
			p.shortPositions[fill.Symbol] = pos
		}
		totalQty := pos.Qty + fill.Qty
		pos.AvgPrice = (pos.Qty*pos.AvgPrice + fill.Qty*fill.Price) / totalQty
		pos.Qty = totalQty
		return nil

	case strategy.SideBuy:
		// Buy to cover SHORT.
		pos, exists := p.shortPositions[fill.Symbol]
		if !exists {
			return nil
		}
		gross := fill.Qty * (pos.AvgPrice - fill.Price) // SHORT PnL
		p.cash += gross - fill.Fee

		var pnlPct float64
		if pos.AvgPrice > 0 {
			pnlPct = (pos.AvgPrice - fill.Price) / pos.AvgPrice * 100
		}

		trade := &Trade{
			Symbol:       fill.Symbol,
			Side:         strategy.SideBuy,
			PositionSide: strategy.PositionSideShort,
			EntryTime:    pos.OpenedAt,
			ExitTime:     barTime,
			EntryPrice:   pos.AvgPrice,
			ExitPrice:    fill.Price,
			Qty:          fill.Qty,
			GrossPnL:     gross,
			Fee:          fill.Fee,
			NetPnL:       gross - fill.Fee,
			PnLPct:       pnlPct,
		}

		pos.Qty -= fill.Qty
		if pos.Qty <= 1e-10 {
			delete(p.shortPositions, fill.Symbol)
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
