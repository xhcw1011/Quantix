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

// UpdateStop advances the trailing stop. Before activation the stop is fixed at
// its initial protective level; once P&L crosses ActivateR the stop ratchets in
// the trade's favour and never loosens.
func (p *Protection) UpdateStop(price, atr float64) {
	if !p.cfg.TrailEnabled {
		return
	}
	pnlR := p.PnlR(price)
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

// RiskUSD is the worst-case loss to the initial stop (qty × per-unit risk).
func (p *Protection) RiskUSD() float64 { return p.Qty * p.R }

// RiskPct is the initial stop distance as a percent of entry.
func (p *Protection) RiskPct() float64 {
	if p.Entry > 0 {
		return p.R / p.Entry * 100
	}
	return 0
}
