package guardian

import "math"

// Position side constants.
const (
	SideLong  = "long"
	SideShort = "short"
)

// StopMode selects how the initial stop distance is derived.
type StopMode int

const (
	StopPrice StopMode = iota // StopValue is an absolute price
	StopPct                   // StopValue is a fraction of entry (0.03 = 3%)
	StopATR                   // StopValue is a multiple of ATR (k)
)

// TrailMode selects how the trailing distance is derived.
type TrailMode int

const (
	TrailR   TrailMode = iota // TrailValue is a multiple of initial risk R
	TrailATR                  // TrailValue is a multiple of current ATR
)

// TPMode selects how the take-profit target is derived (TPNone = disabled).
type TPMode int

const (
	TPNone  TPMode = iota
	TPPrice        // TPValue is an absolute price
	TPPct          // TPValue is a fraction of entry
	TPR            // TPValue is a multiple of initial risk R
)

// ProtectionConfig is the user-supplied stop / trailing / take-profit setup.
type ProtectionConfig struct {
	StopMode  StopMode
	StopValue float64

	TrailEnabled bool
	ActivateR    float64 // start trailing once P&L >= this many R
	TrailMode    TrailMode
	TrailValue   float64

	// PeakProfitLockFrac, once trailing has activated, additionally never lets
	// the stop sit worse than locking in this fraction of the best P&L (in R)
	// seen so far — independent of, and composed with, the TrailValue distance
	// above (whichever of the two implies the tighter stop wins). The R/ATR
	// trail sits a FIXED distance behind the peak; when the initial risk R is
	// itself large (e.g. a wide %-stop on a high-priced asset), that fixed
	// distance can equal most of the profit made, so a reversal near the peak
	// gives back nearly everything even with trailing "on" (2026-08-24
	// finding: a short peaked at ~$11.7 unrealised profit and closed at
	// $1.82 — the ~$9.6 giveback was almost exactly the 0.5R trail distance).
	// This ratchets a floor that scales with the profit actually achieved
	// instead of with R. 0 = disabled (trail distance alone governs).
	PeakProfitLockFrac float64

	BreakEvenAtR float64 // once P&L >= this many R, move the stop to entry (0 = off)

	PartialTPAtR      float64 // once P&L >= this many R, close PartialTPFraction of the position (0 = off)
	PartialTPFraction float64 // fraction to close on partial TP (default 0.5)

	TPMode  TPMode
	TPValue float64
}

// Protection holds the live protective state for a single position. All methods
// are pure with respect to external I/O and are driven from one goroutine.
type Protection struct {
	Side  string
	Entry float64
	Qty   float64
	R     float64 // initial risk per unit = |entry - initial stop|
	Stop  float64 // current (possibly trailed) stop price
	PeakR float64 // best P&L in R seen since activation

	cfg       ProtectionConfig
	tp        float64 // computed take-profit price (0 => disabled)
	activated bool
}

// NewProtection computes the initial stop, initial risk R, and take-profit price.
func NewProtection(side string, entry, qty float64, cfg ProtectionConfig, atr float64) *Protection {
	p := &Protection{Side: side, Entry: entry, Qty: qty, cfg: cfg}

	dist := stopDistance(cfg.StopMode, cfg.StopValue, entry, atr)
	if side == SideLong {
		p.Stop = entry - dist
	} else {
		p.Stop = entry + dist
	}
	p.R = math.Abs(entry - p.Stop)
	p.tp = takeProfitPrice(side, entry, p.R, cfg)
	return p
}

func stopDistance(mode StopMode, val, entry, atr float64) float64 {
	switch mode {
	case StopPrice:
		return math.Abs(entry - val)
	case StopPct:
		return entry * val
	case StopATR:
		return val * atr
	default:
		return entry * val
	}
}

func takeProfitPrice(side string, entry, r float64, cfg ProtectionConfig) float64 {
	var dist float64
	switch cfg.TPMode {
	case TPNone:
		return 0
	case TPPrice:
		return cfg.TPValue
	case TPPct:
		dist = entry * cfg.TPValue
	case TPR:
		dist = cfg.TPValue * r
	}
	if side == SideLong {
		return entry + dist
	}
	return entry - dist
}

// PnlR returns the current unrealised P&L in R multiples.
func (p *Protection) PnlR(price float64) float64 {
	if p.R <= 0 {
		return 0
	}
	if p.Side == SideLong {
		return (price - p.Entry) / p.R
	}
	return (p.Entry - price) / p.R
}

// UpdateStop applies break-even (independent of trailing) and then advances the
// trailing stop. Before trail activation the stop is fixed at its initial level;
// once P&L crosses ActivateR the stop ratchets in the trade's favour and never
// loosens.
func (p *Protection) UpdateStop(price, atr float64) {
	pnlR := p.PnlR(price)

	// Break-even: once far enough in profit, never let the stop sit worse than
	// entry — the trade can no longer become a loss. Works with trailing off too.
	if p.cfg.BreakEvenAtR > 0 && pnlR >= p.cfg.BreakEvenAtR {
		if p.Side == SideLong && p.Stop < p.Entry {
			p.Stop = p.Entry
		} else if p.Side == SideShort && p.Stop > p.Entry {
			p.Stop = p.Entry
		}
	}

	if !p.cfg.TrailEnabled {
		return
	}
	if !p.activated {
		if pnlR >= p.cfg.ActivateR {
			p.activated = true
			p.PeakR = pnlR
		} else {
			return
		}
	}
	if pnlR > p.PeakR {
		p.PeakR = pnlR
	}

	var trailDist float64
	if p.cfg.TrailMode == TrailATR {
		trailDist = p.cfg.TrailValue * atr
	} else {
		trailDist = p.cfg.TrailValue * p.R
	}
	if trailDist <= 0 {
		return
	}

	if p.Side == SideLong {
		if cand := price - trailDist; cand > p.Stop {
			p.Stop = cand
		}
	} else {
		if cand := price + trailDist; cand < p.Stop {
			p.Stop = cand
		}
	}

	// Peak-profit-percentage floor (see PeakProfitLockFrac doc): compared
	// against the stop as it stands after the distance-based trail above, so
	// whichever mechanism implies the tighter stop is the one that sticks.
	if p.cfg.PeakProfitLockFrac > 0 {
		lockR := p.PeakR * p.cfg.PeakProfitLockFrac
		if p.Side == SideLong {
			if cand := p.Entry + lockR*p.R; cand > p.Stop {
				p.Stop = cand
			}
		} else {
			if cand := p.Entry - lockR*p.R; cand < p.Stop {
				p.Stop = cand
			}
		}
	}
}

// UpdateConfig applies a live change to the protective setup on an already-armed
// position. Before trailing engages the base stop moves freely (the user is still
// setting up); once trailing has engaged the stop can only tighten — a live edit
// never gives back locked-in profit. Take-profit and R recompute immediately.
func (p *Protection) UpdateConfig(newCfg ProtectionConfig, atr float64) {
	dist := stopDistance(newCfg.StopMode, newCfg.StopValue, p.Entry, atr)
	var base float64
	if p.Side == SideLong {
		base = p.Entry - dist
	} else {
		base = p.Entry + dist
	}
	switch {
	case !p.activated:
		p.Stop = base // still setting up — free to widen or tighten the initial stop
	case p.Side == SideLong && base > p.Stop:
		p.Stop = base // trailing engaged — tighten only
	case p.Side == SideShort && base < p.Stop:
		p.Stop = base
	}
	p.R = math.Abs(p.Entry - base)
	p.tp = takeProfitPrice(p.Side, p.Entry, p.R, newCfg)
	p.cfg = newCfg
}

// StopHit reports whether price has reached or passed the current stop.
func (p *Protection) StopHit(price float64) bool {
	if p.Side == SideLong {
		return price <= p.Stop
	}
	return price >= p.Stop
}

// TPHit reports whether price has reached the take-profit target (false if none).
func (p *Protection) TPHit(price float64) bool {
	if p.cfg.TPMode == TPNone {
		return false
	}
	if p.Side == SideLong {
		return price >= p.tp
	}
	return price <= p.tp
}

// Activated reports whether the trailing stop is engaged.
func (p *Protection) Activated() bool { return p.activated }

// SetActivated restores the activation flag (used by restart recovery).
func (p *Protection) SetActivated(a bool) { p.activated = a }

// TPPrice returns the computed take-profit price (0 if disabled).
func (p *Protection) TPPrice() float64 { return p.tp }

// PartialTPReady reports whether P&L has reached the partial take-profit trigger.
func (p *Protection) PartialTPReady(price float64) bool {
	return p.cfg.PartialTPAtR > 0 && p.PnlR(price) >= p.cfg.PartialTPAtR
}

// PartialTPQty is the quantity to close on a partial take-profit.
func (p *Protection) PartialTPQty() float64 {
	f := p.cfg.PartialTPFraction
	if f <= 0 || f >= 1 {
		f = 0.5
	}
	return p.Qty * f
}

// RiskUSD is the worst-case loss to the initial stop (qty × per-unit risk).
func (p *Protection) RiskUSD() float64 { return p.Qty * p.R }

// RiskPct is the initial stop distance as a percent of entry.
func (p *Protection) RiskPct() float64 {
	if p.Entry > 0 {
		return p.R / p.Entry * 100
	}
	return 0
}
