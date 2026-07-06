// Package pool implements the Capital Architecture Layer (Layer 3): capital is
// grouped into pools (Growth / Yield / Cash), and drawdown + directional exposure
// risk is reasoned at the pool, not the strategy. See
// docs/superpowers/specs/2026-07-06-capital-layer-design.md.
//
// A Pool DECIDES (computes its state, publishes a Snapshot); it never blocks an
// order. Enforcement is the ORG's job (the single gate) — the PoolGateRule reads a
// pool's published Snapshot. Pools are virtual for now (a notional slice of one
// shared exchange account) but the abstraction is upgradeable to real sub-accounts.
package pool

// Status is a pool's trading state.
type Status int

const (
	Active Status = iota
	Halted
)

func (s Status) String() string {
	if s == Halted {
		return "HALTED"
	}
	return "ACTIVE"
}

// Config is a pool's static definition.
type Config struct {
	Name            string
	NotionalCap     float64 // C_pool: capital allotted (virtual)
	MaxDrawdown     float64 // pool DD ≥ this → HALT
	RecoverDrawdown float64 // DD must fall back below this to un-halt (< MaxDrawdown)
	RecoverBars     int     // durable-recovery persistence before un-halt
	MaxLongExp      float64 // directional exposure caps (fraction of pool equity)
	MaxShortExp     float64
}

// MemberState is one member strategy's contribution, fed each update.
type MemberState struct {
	Realized      float64 // locked PnL — does not recover
	Unrealized    float64 // mark PnL — can recover
	LongNotional  float64 // abs notional of long exposure
	ShortNotional float64 // abs notional of short exposure
}

// Snapshot is the published, read-only pool status the ORG consumes. Directional
// exposure is never netted.
type Snapshot struct {
	Name        string
	Status      Status
	Equity      float64
	Realized    float64
	Unrealized  float64
	Drawdown    float64
	LongExp     float64 // long notional / equity
	ShortExp    float64 // short notional / equity
	MaxLongExp  float64
	MaxShortExp float64
}

// Pool is a single pool's state machine.
type Pool struct {
	cfg      Config
	peak     float64
	status   Status
	recovern int // consecutive durable-recovery updates while halted
}

// New creates a pool, ACTIVE, with peak seeded at its notional capital.
func New(cfg Config) *Pool {
	return &Pool{cfg: cfg, peak: cfg.NotionalCap, status: Active}
}

// Update aggregates member states, advances the drawdown + halt/recovery hysteresis,
// and returns the published Snapshot.
func (p *Pool) Update(members []MemberState) Snapshot {
	var realized, unrealized, longN, shortN float64
	for _, m := range members {
		realized += m.Realized
		unrealized += m.Unrealized
		longN += m.LongNotional
		shortN += m.ShortNotional
	}
	equity := p.cfg.NotionalCap + realized + unrealized
	if equity > p.peak {
		p.peak = equity
	}
	dd := 0.0
	if p.peak > 0 {
		dd = (p.peak - equity) / p.peak
	}

	switch p.status {
	case Active:
		if p.cfg.MaxDrawdown > 0 && dd >= p.cfg.MaxDrawdown {
			p.status = Halted
			p.recovern = 0
		}
	case Halted:
		// Recovery only when DD falls back below the (lower) recover line AND holds
		// for RecoverBars — a one-bar mark bounce does not un-halt.
		if dd < p.cfg.RecoverDrawdown {
			p.recovern++
			if p.recovern >= maxInt(1, p.cfg.RecoverBars) {
				p.status = Active
				p.recovern = 0
			}
		} else {
			p.recovern = 0
		}
	}

	longExp, shortExp := 0.0, 0.0
	if equity > 0 {
		longExp = longN / equity
		shortExp = shortN / equity
	}
	return Snapshot{
		Name: p.cfg.Name, Status: p.status, Equity: equity,
		Realized: realized, Unrealized: unrealized, Drawdown: dd,
		LongExp: longExp, ShortExp: shortExp,
		MaxLongExp: p.cfg.MaxLongExp, MaxShortExp: p.cfg.MaxShortExp,
	}
}

// SetStatus is a manual operator override (e.g. force ACTIVE/HALTED).
func (p *Pool) SetStatus(s Status) { p.status = s; p.recovern = 0 }

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
