// Package cascade simulates the liquidation-cascade-reversion (crash-fade) strategy: buy a
// coin at the close of a bar whose return is a large negative sigma-shock, hold a fixed
// number of bars, then exit. The simulator is event-driven over a global bar grid with a
// hard cap on concurrent positions — the cap is the whole point, since coins cascade
// together in market-wide crashes and an uncapped book would go max-long into the crash.
package cascade

import "sort"

// Bar is one aligned candle for one symbol. Has=false means the symbol has no bar at this
// global index (a data gap) — positions hold flat through it.
type Bar struct {
	Close float64
	Shock float64 // return / trailing-vol, precomputed by the caller
	Has   bool
}

// Series is one symbol's bars indexed by global bar index.
type Series = []Bar

// Config parameterizes the crash-fade sim.
type Config struct {
	K             float64 // enter when Shock <= -K
	HoldBars      int     // fixed holding period in bars
	FracPerTrade  float64 // capital fraction committed per position
	MaxConcurrent int     // hard cap on simultaneous open positions
	CostRT        float64 // round-trip cost (entry+exit), fractional
	StopLoss      float64 // exit early if price falls this fraction below entry (0 = off)
	WickMin       float64 // entry confirmation: require intrabar recovery >= this (paper Step only)
}

// Trade is one realized round trip.
type Trade struct {
	Symbol          string
	EntryGi, ExitGi int
	Ret             float64 // net of cost
}

// Result is the sim output: a mark-to-market equity curve (indexed by global bar) and the
// realized trades.
type Result struct {
	Equity []float64
	Gross  []float64 // sum of open-position weights per bar (exposure / crowding)
	Trades []Trade
}

type openPos struct {
	sym     string
	entryGi int
	exitGi  int
	entryPx float64
	w       float64
}

// Simulate runs the crash-fade portfolio over the global grid `times` (only its length is
// used for indexing) with per-symbol aligned `data`. Equity starts at 1.0.
func Simulate(times []int64, data map[string]Series, cfg Config) Result {
	nBars := len(times)
	equity := 1.0
	eq := make([]float64, nBars)
	gross := make([]float64, nBars)
	var open []openPos
	var trades []Trade

	// stable symbol order for deterministic tie-breaking
	syms := make([]string, 0, len(data))
	for s := range data {
		syms = append(syms, s)
	}
	sort.Strings(syms)

	priceRet := func(sym string, gi int) float64 {
		if gi == 0 {
			return 0
		}
		s := data[sym]
		if gi >= len(s) || !s[gi].Has || !s[gi-1].Has || s[gi-1].Close <= 0 {
			return 0 // gap → hold flat
		}
		return s[gi].Close/s[gi-1].Close - 1
	}

	for gi := 0; gi < nBars; gi++ {
		// 1. mark-to-market: return from positions held into this bar
		ret := 0.0
		for _, p := range open {
			ret += p.w * priceRet(p.sym, gi)
		}
		costDrag := 0.0

		// 2. exits: hold elapsed, final bar, or stop-loss hit
		stillOpen := open[:0]
		for _, p := range open {
			px := p.entryPx
			if s := data[p.sym]; gi < len(s) && s[gi].Has && s[gi].Close > 0 {
				px = s[gi].Close
			}
			stopHit := cfg.StopLoss > 0 && px <= p.entryPx*(1-cfg.StopLoss)
			if p.exitGi == gi || gi == nBars-1 || stopHit {
				trades = append(trades, Trade{p.sym, p.entryGi, gi, px/p.entryPx - 1 - cfg.CostRT})
				costDrag += p.w * cfg.CostRT / 2 // exit leg
			} else {
				stillOpen = append(stillOpen, p)
			}
		}
		open = stillOpen

		// 3. entries: symbols shocking this bar, biggest shocks first, up to the cap
		if gi < nBars-1 {
			type cand struct {
				sym   string
				shock float64
				px    float64
			}
			var cands []cand
			held := map[string]bool{}
			for _, p := range open {
				held[p.sym] = true
			}
			for _, sym := range syms {
				s := data[sym]
				if gi >= len(s) || !s[gi].Has || s[gi].Close <= 0 || held[sym] {
					continue
				}
				if s[gi].Shock <= -cfg.K {
					cands = append(cands, cand{sym, s[gi].Shock, s[gi].Close})
				}
			}
			sort.Slice(cands, func(i, j int) bool { return cands[i].shock < cands[j].shock })
			for _, c := range cands {
				if len(open) >= cfg.MaxConcurrent {
					break
				}
				exit := gi + cfg.HoldBars
				if exit >= nBars {
					exit = nBars - 1
				}
				open = append(open, openPos{c.sym, gi, exit, c.px, cfg.FracPerTrade})
				costDrag += cfg.FracPerTrade * cfg.CostRT / 2 // entry leg
			}
		}

		equity *= 1 + ret - costDrag
		eq[gi] = equity
		for _, p := range open {
			gross[gi] += p.w
		}
	}
	return Result{Equity: eq, Gross: gross, Trades: trades}
}
