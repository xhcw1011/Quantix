package guardian

import "fmt"

// AlertCtx carries the per-evaluation snapshot the rules read. It is populated by
// the Guardian each bar/tick.
type AlertCtx struct {
	Price     float64
	PnlR      float64
	StopDistR float64 // distance from price to current stop, in R
	ATR       float64
	AvgATR    float64 // longer-run average ATR, for spike detection
	BarsHeld  int
	MA        float64
	HasMA     bool
}

// Alert is one fired notification.
type Alert struct {
	Title string
	Msg   string
}

// AlertRule evaluates one condition each bar/tick. Rules are stateful so they can
// be edge-triggered (fire on transition) and avoid spamming. Returns whether to
// fire and the (factual) message — never a buy/sell instruction.
type AlertRule interface {
	Name() string
	Eval(c AlertCtx) (fire bool, msg string)
}

// ── Position-class rules ────────────────────────────────────────────

// ProfitMilestone fires once each time P&L crosses another Step of R.
type ProfitMilestone struct {
	Step float64
	last int
}

func (r *ProfitMilestone) Name() string { return "profit_milestone" }
func (r *ProfitMilestone) Eval(c AlertCtx) (bool, string) {
	if r.Step <= 0 {
		r.Step = 1
	}
	m := int(c.PnlR / r.Step)
	if m >= 1 && m > r.last {
		r.last = m
		return true, fmt.Sprintf("浮盈 +%.0fR,考虑上移止损锁利", float64(m)*r.Step)
	}
	return false, ""
}

// StopProximity fires when price comes within WithinR of the stop; edge-triggered
// (re-arms after leaving the zone).
type StopProximity struct {
	WithinR float64
	inside  bool
}

func (r *StopProximity) Name() string { return "stop_proximity" }
func (r *StopProximity) Eval(c AlertCtx) (bool, string) {
	near := c.StopDistR <= r.WithinR
	if near && !r.inside {
		r.inside = true
		return true, fmt.Sprintf("价格逼近止损(距止损 %.2fR)", c.StopDistR)
	}
	if !near {
		r.inside = false
	}
	return false, ""
}

// Stagnation fires once when the position has been held Bars bars with |P&L| below BandR.
type Stagnation struct {
	Bars  int
	BandR float64
	fired bool
}

func (r *Stagnation) Name() string { return "stagnation" }
func (r *Stagnation) Eval(c AlertCtx) (bool, string) {
	if r.fired {
		return false, ""
	}
	if c.BarsHeld >= r.Bars && c.PnlR < r.BandR && c.PnlR > -r.BandR {
		r.fired = true
		return true, fmt.Sprintf("已持仓 %d 根无进展(±%.1fR 内),留意是否离场", c.BarsHeld, r.BandR)
	}
	return false, ""
}

// VolSpike fires when ATR jumps to >= Mult times its longer-run average; edge-triggered.
type VolSpike struct {
	Mult    float64
	spiking bool
}

func (r *VolSpike) Name() string { return "vol_spike" }
func (r *VolSpike) Eval(c AlertCtx) (bool, string) {
	spike := c.AvgATR > 0 && c.ATR >= r.Mult*c.AvgATR
	if spike && !r.spiking {
		r.spiking = true
		return true, "波动率突然放大,止损可能偏紧,注意仓位"
	}
	if !spike {
		r.spiking = false
	}
	return false, ""
}

// ── Market-fact rules (statements, never predictions) ───────────────

// LevelCross fires when price crosses a user-defined Level in either direction.
type LevelCross struct {
	Level    float64
	initted  bool
	wasAbove bool
}

func (r *LevelCross) Name() string { return "level_cross" }
func (r *LevelCross) Eval(c AlertCtx) (bool, string) {
	above := c.Price >= r.Level
	if !r.initted {
		r.initted, r.wasAbove = true, above
		return false, ""
	}
	if above != r.wasAbove {
		r.wasAbove = above
		dir := "跌破"
		if above {
			dir = "突破"
		}
		return true, fmt.Sprintf("价格%s你设的 %.4g", dir, r.Level)
	}
	return false, ""
}

// MAState fires when price crosses its reference MA (fact, not forecast).
type MAState struct {
	initted  bool
	wasAbove bool
}

func (r *MAState) Name() string { return "ma_state" }
func (r *MAState) Eval(c AlertCtx) (bool, string) {
	if !c.HasMA {
		return false, ""
	}
	above := c.Price >= c.MA
	if !r.initted {
		r.initted, r.wasAbove = true, above
		return false, ""
	}
	if above != r.wasAbove {
		r.wasAbove = above
		dir := "跌破"
		if above {
			dir = "站上"
		}
		return true, fmt.Sprintf("价格%s均线(MA %.4g)", dir, c.MA)
	}
	return false, ""
}

// ── Engine ──────────────────────────────────────────────────────────

// AlertEngine evaluates a set of rules each bar/tick and returns the fired alerts.
type AlertEngine struct {
	rules []AlertRule
}

// NewAlertEngine creates an empty engine.
func NewAlertEngine() *AlertEngine { return &AlertEngine{} }

// Add registers a rule.
func (e *AlertEngine) Add(r AlertRule) { e.rules = append(e.rules, r) }

// Evaluate runs every rule against the snapshot and collects those that fire.
func (e *AlertEngine) Evaluate(c AlertCtx) []Alert {
	var out []Alert
	for _, r := range e.rules {
		if fire, msg := r.Eval(c); fire {
			out = append(out, Alert{Title: r.Name(), Msg: msg})
		}
	}
	return out
}

// Len reports how many rules are registered.
func (e *AlertEngine) Len() int { return len(e.rules) }
