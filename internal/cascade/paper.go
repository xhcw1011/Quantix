package cascade

import "sort"

// Tick is one coin's latest CLOSED 4h bar, as seen by the live paper executor.
type Tick struct {
	Symbol string
	Close  float64
	Shock  float64 // return / trailing-vol
	Wick   float64 // intrabar recovery (close-low)/(high-low)
}

// PaperPos is one open paper position.
type PaperPos struct {
	Symbol  string  `json:"symbol"`
	EntryPx float64 `json:"entry_px"`
	LastPx  float64 `json:"last_px"` // mark from the previous tick (for bar MTM)
	EntryTs int64   `json:"entry_ts"`
	ExitTs  int64   `json:"exit_ts"`
	Weight  float64 `json:"weight"`
}

// PaperTrade is one realized round trip.
type PaperTrade struct {
	Symbol  string  `json:"symbol"`
	EntryTs int64   `json:"entry_ts"`
	ExitTs  int64   `json:"exit_ts"`
	Ret     float64 `json:"ret"` // net of cost
}

// PaperState is the persisted forward-test state.
type PaperState struct {
	Equity    float64      `json:"equity"`
	Positions []PaperPos   `json:"positions"`
	Trades    []PaperTrade `json:"trades"`
	LastTs    int64        `json:"last_ts"`
}

// StepEvent describes what happened this tick (for logging).
type StepEvent struct {
	Opened []string
	Closed []PaperTrade
}

// Step advances the paper state by one 4h bar using this tick's market snapshot. It mirrors
// the batch Simulate per-bar logic — mark-to-market, exits (hold elapsed or stop), then
// entries (shock + wick confirmation, biggest shocks first, up to MaxConcurrent). `now` is
// the closed bar's timestamp (ms); `barMs` is the bar length in ms. Returns the new state
// and the events. Idempotent guard: a tick at or before LastTs is ignored.
func Step(st PaperState, ticks []Tick, now, barMs int64, cfg Config) (PaperState, StepEvent) {
	var ev StepEvent
	if st.Equity == 0 {
		st.Equity = 1.0
	}
	if st.LastTs != 0 && now <= st.LastTs {
		return st, ev // already processed this bar
	}
	px := make(map[string]Tick, len(ticks))
	for _, t := range ticks {
		px[t.Symbol] = t
	}

	// 1. mark-to-market held positions
	ret := 0.0
	for i := range st.Positions {
		p := &st.Positions[i]
		if t, ok := px[p.Symbol]; ok && p.LastPx > 0 {
			ret += p.Weight * (t.Close/p.LastPx - 1)
			p.LastPx = t.Close
		}
	}
	costDrag := 0.0

	// 2. exits: hold elapsed or stop-loss
	keep := st.Positions[:0]
	for _, p := range st.Positions {
		cur := p.LastPx
		if t, ok := px[p.Symbol]; ok {
			cur = t.Close
		}
		stopHit := cfg.StopLoss > 0 && cur <= p.EntryPx*(1-cfg.StopLoss)
		if now >= p.ExitTs || stopHit {
			tr := PaperTrade{p.Symbol, p.EntryTs, now, cur/p.EntryPx - 1 - cfg.CostRT}
			st.Trades = append(st.Trades, tr)
			ev.Closed = append(ev.Closed, tr)
			costDrag += p.Weight * cfg.CostRT / 2
		} else {
			keep = append(keep, p)
		}
	}
	st.Positions = keep

	// 3. entries: shock + wick confirmation, biggest shocks first, respect the cap
	held := map[string]bool{}
	for _, p := range st.Positions {
		held[p.Symbol] = true
	}
	var cands []Tick
	for _, t := range ticks {
		if !held[t.Symbol] && t.Close > 0 && t.Shock <= -cfg.K && t.Wick >= cfg.WickMin {
			cands = append(cands, t)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Shock < cands[j].Shock })
	for _, c := range cands {
		if len(st.Positions) >= cfg.MaxConcurrent {
			break
		}
		st.Positions = append(st.Positions, PaperPos{
			Symbol: c.Symbol, EntryPx: c.Close, LastPx: c.Close,
			EntryTs: now, ExitTs: now + int64(cfg.HoldBars)*barMs, Weight: cfg.FracPerTrade,
		})
		ev.Opened = append(ev.Opened, c.Symbol)
		costDrag += cfg.FracPerTrade * cfg.CostRT / 2
	}

	st.Equity *= 1 + ret - costDrag
	st.LastTs = now
	return st, ev
}
