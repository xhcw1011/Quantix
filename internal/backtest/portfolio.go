package backtest

import (
	"math"
	"time"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// Lot is a single opening fill's worth of exposure, tracked independently so
// that overlapping same-side opens (grid layers, hedge-scalp, trend adds all
// stacking on one side) never bleed entry context into each other's exit.
// Closes consume the queue FIFO (oldest lot first), splitting a lot when the
// closing quantity doesn't land on a lot boundary.
type Lot struct {
	Qty      float64
	Price    float64
	OpenedAt time.Time
	// Meta is the entry-context snapshot from the opening order (regime, atr,
	// entry_stop, ...), used for scenario bucketing. Nil when unset.
	Meta map[string]float64
	// StopDist is |entry − stop| in price units at this lot's own entry;
	// zero when no stop was recorded. Basis for MFE/MAE-in-R on this lot.
	StopDist float64
	// FavPrice / AdvPrice: best / worst price seen since THIS lot opened
	// (high/low for long, low/high for short). Seeded at the lot's own entry
	// price, so a lot opened mid-stream never inherits an earlier lot's range.
	FavPrice float64
	AdvPrice float64
}

// Position represents an open holding in a single symbol as a FIFO queue of
// lots. For spot strategies (PositionSide=""), this tracks shares bought with
// cash: open = cash debit by full notional, close = cash credit by full
// notional. For futures hedge mode, the portfolio tracks LONG and SHORT
// independently. SHORT cash accounting is PnL-only (no notional debit on
// open) so that initial capital represents margin, not asset value.
type Position struct {
	Symbol string
	Side   strategy.Side
	Lots   []Lot // FIFO, oldest first
}

// Qty returns the position's total open quantity across all lots.
func (pos *Position) Qty() float64 {
	total := 0.0
	for _, l := range pos.Lots {
		total += l.Qty
	}
	return total
}

// AvgPrice returns the qty-weighted average entry price across all lots.
func (pos *Position) AvgPrice() float64 {
	totalQty, totalCost := 0.0, 0.0
	for _, l := range pos.Lots {
		totalQty += l.Qty
		totalCost += l.Qty * l.Price
	}
	if totalQty == 0 {
		return 0
	}
	return totalCost / totalQty
}

// Trade records a completed round-trip (entry + exit) against a single lot
// (or lot fragment, when a close spans multiple lots).
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

	// ── Post-trade attribution (populated by the broker / engine) ──
	// ExitReason is why the position closed, sourced from the closing Fill's
	// Reason: "stop_loss", "take_profit", "trailing", "signal", "reversal",
	// "time_exit", "backtest_end", … Empty when the strategy did not tag it.
	ExitReason string
	// MFEPct / MAEPct: maximum favourable / adverse excursion over the life of
	// this lot, as a percent of entry price. MFE is signed in the trade's
	// favour (≥ 0); MAE is signed against it (≤ 0).
	MFEPct float64
	MAEPct float64
	// MFER / MAER: the same excursions in R multiples (relative to this lot's
	// own entry stop distance). Zero when no entry stop was recorded.
	MFER float64
	MAER float64
	// EntryMeta is the entry-context snapshot captured on the opening order
	// that produced this specific lot (e.g. adx/atr/funding/regime) for
	// scenario bucketing. Nil when unset.
	EntryMeta map[string]float64
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
	return pos.Qty(), pos.AvgPrice(), true
}

// OpenQty returns the currently-open quantity for the given symbol and side.
// PositionSideShort inspects short positions; anything else inspects long.
// Used by the engine to distinguish a full close from a partial/staged exit.
func (p *Portfolio) OpenQty(symbol string, posSide strategy.PositionSide) float64 {
	if posSide == strategy.PositionSideShort {
		if pos, ok := p.shortPositions[symbol]; ok {
			return pos.Qty()
		}
		return 0
	}
	if pos, ok := p.longPositions[symbol]; ok {
		return pos.Qty()
	}
	return 0
}

// Equity returns total portfolio value: cash + LONG market value + SHORT
// unrealized PnL. SHORT contributes (entry-current_price)*qty since cash
// did not absorb the notional on open.
func (p *Portfolio) Equity(prices map[string]float64) float64 {
	total := p.cash
	for sym, pos := range p.longPositions {
		px, ok := prices[sym]
		qty, avg := pos.Qty(), pos.AvgPrice()
		if !ok {
			px = avg
		}
		total += qty * px
	}
	for sym, pos := range p.shortPositions {
		px, ok := prices[sym]
		qty, avg := pos.Qty(), pos.AvgPrice()
		if !ok {
			px = avg
		}
		total += qty * (avg - px) // unrealized SHORT pnl
	}
	return total
}

// UpdateExcursions folds the current bar's high/low into every open lot's
// running MFE/MAE extremes for the bar's symbol. Must be called once per
// primary bar, before the broker processes closes, so the exit bar's range
// is included in the excursion. Each lot tracks its own range independently
// — a lot opened mid-stream never inherits an earlier lot's accumulated
// extremes.
func (p *Portfolio) UpdateExcursions(bar exchange.Kline) {
	if pos, ok := p.longPositions[bar.Symbol]; ok {
		for i := range pos.Lots {
			l := &pos.Lots[i]
			if bar.High > l.FavPrice {
				l.FavPrice = bar.High
			}
			if bar.Low < l.AdvPrice {
				l.AdvPrice = bar.Low
			}
		}
	}
	if pos, ok := p.shortPositions[bar.Symbol]; ok {
		for i := range pos.Lots {
			l := &pos.Lots[i]
			if bar.Low < l.FavPrice {
				l.FavPrice = bar.Low
			}
			if bar.High > l.AdvPrice {
				l.AdvPrice = bar.High
			}
		}
	}
}

// ─── Internal methods used by SimBroker ───────────────────────────────────────

// applyFill updates cash and positions after an order is filled.
// Returns any completed Trades if this fill closes (or partially closes)
// a position — one per lot consumed, since a close spanning multiple lots
// must not blend their distinct entry contexts.
func (p *Portfolio) applyFill(fill strategy.Fill, barTime time.Time) []Trade {
	if fill.PositionSide == strategy.PositionSideShort {
		return p.applyShortFill(fill, barTime)
	}
	return p.applyLongFill(fill, barTime)
}

func newLot(fill strategy.Fill, barTime time.Time) Lot {
	l := Lot{
		Qty: fill.Qty, Price: fill.Price, OpenedAt: barTime,
		Meta: fill.Meta, FavPrice: fill.Price, AdvPrice: fill.Price,
	}
	if fill.Meta != nil {
		if stop := fill.Meta["entry_stop"]; stop != 0 {
			l.StopDist = math.Abs(fill.Price - stop)
		}
	}
	return l
}

// consumeLotsFIFO closes up to closeQty from pos's oldest lots first,
// splitting the final lot touched if it only partially covers the requested
// quantity. Produces one Trade per lot consumed (or partially consumed), each
// carrying that lot's own entry price/time/meta/MFE-MAE, so overlapping
// same-side opens never bleed context into each other's exit record. Fee is
// prorated across lots by the quantity each contributes to the close.
func consumeLotsFIFO(pos *Position, closeQty, exitPrice, totalFee float64, exitTime time.Time,
	reason string, side strategy.Side, posSide strategy.PositionSide, short bool) []Trade {

	var trades []Trade
	remaining := closeQty
	for remaining > 1e-10 && len(pos.Lots) > 0 {
		lot := &pos.Lots[0]
		qty := math.Min(remaining, lot.Qty)
		var feeShare float64
		if closeQty > 0 {
			feeShare = totalFee * (qty / closeQty)
		}

		var gross, pnlPct float64
		if short {
			gross = qty * (lot.Price - exitPrice)
			if lot.Price > 0 {
				pnlPct = (lot.Price - exitPrice) / lot.Price * 100
			}
		} else {
			gross = qty * (exitPrice - lot.Price)
			if lot.Price > 0 {
				pnlPct = (exitPrice - lot.Price) / lot.Price * 100
			}
		}

		t := Trade{
			Symbol: pos.Symbol, Side: side, PositionSide: posSide,
			EntryTime: lot.OpenedAt, ExitTime: exitTime,
			EntryPrice: lot.Price, ExitPrice: exitPrice,
			Qty: qty, GrossPnL: gross, Fee: feeShare, NetPnL: gross - feeShare,
			PnLPct: pnlPct, ExitReason: reason,
		}
		attachExcursion(&t, lot, short)
		trades = append(trades, t)

		lot.Qty -= qty
		remaining -= qty
		if lot.Qty <= 1e-10 {
			pos.Lots = pos.Lots[1:]
		}
	}
	return trades
}

// attachExcursion stamps MFE/MAE (percent and R) from the lot's accumulated
// extremes and its own entry price/stop distance onto the Trade.
func attachExcursion(t *Trade, lot *Lot, short bool) {
	ep := t.EntryPrice
	if ep <= 0 {
		return
	}
	if short {
		t.MFEPct = (ep - lot.FavPrice) / ep * 100
		t.MAEPct = (ep - lot.AdvPrice) / ep * 100
	} else {
		t.MFEPct = (lot.FavPrice - ep) / ep * 100
		t.MAEPct = (lot.AdvPrice - ep) / ep * 100
	}
	if lot.StopDist > 0 {
		if sdPct := lot.StopDist / ep * 100; sdPct > 0 {
			t.MFER = t.MFEPct / sdPct
			t.MAER = t.MAEPct / sdPct
		}
	}
	t.EntryMeta = lot.Meta
}

func (p *Portfolio) applyLongFill(fill strategy.Fill, barTime time.Time) []Trade {
	switch fill.Side {
	case strategy.SideBuy:
		cost := fill.Qty*fill.Price + fill.Fee
		p.cash -= cost

		pos, exists := p.longPositions[fill.Symbol]
		if !exists {
			pos = &Position{Symbol: fill.Symbol, Side: strategy.SideBuy}
			p.longPositions[fill.Symbol] = pos
		}
		pos.Lots = append(pos.Lots, newLot(fill, barTime))
		return nil

	case strategy.SideSell:
		pos, exists := p.longPositions[fill.Symbol]
		if !exists {
			return nil
		}
		proceeds := fill.Qty * fill.Price
		p.cash += proceeds - fill.Fee

		trades := consumeLotsFIFO(pos, fill.Qty, fill.Price, fill.Fee, barTime,
			fill.Reason, strategy.SideSell, strategy.PositionSideLong, false)

		if pos.Qty() <= 1e-10 {
			delete(p.longPositions, fill.Symbol)
		}
		return trades
	}
	return nil
}

func (p *Portfolio) applyShortFill(fill strategy.Fill, barTime time.Time) []Trade {
	switch fill.Side {
	case strategy.SideSell:
		// Open or add to SHORT. Futures-style: only fee moves cash; the
		// notional is implicitly margined.
		p.cash -= fill.Fee

		pos, exists := p.shortPositions[fill.Symbol]
		if !exists {
			pos = &Position{Symbol: fill.Symbol, Side: strategy.SideSell}
			p.shortPositions[fill.Symbol] = pos
		}
		pos.Lots = append(pos.Lots, newLot(fill, barTime))
		return nil

	case strategy.SideBuy:
		// Buy to cover SHORT.
		pos, exists := p.shortPositions[fill.Symbol]
		if !exists {
			return nil
		}
		trades := consumeLotsFIFO(pos, fill.Qty, fill.Price, fill.Fee, barTime,
			fill.Reason, strategy.SideBuy, strategy.PositionSideShort, true)

		var gross float64
		for _, t := range trades {
			gross += t.GrossPnL
		}
		p.cash += gross - fill.Fee

		if pos.Qty() <= 1e-10 {
			delete(p.shortPositions, fill.Symbol)
		}
		return trades
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
