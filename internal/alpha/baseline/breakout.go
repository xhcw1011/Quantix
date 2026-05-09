package baseline

import (
	"fmt"

	"github.com/Quantix/quantix/internal/alpha"
)

// Breakout is a 10-bar Donchian breakout alpha. Long when close exceeds the
// 10-bar high (exclusive of current bar). Strength scales with breakout
// magnitude divided by ATR, capped at 1.0.
type Breakout struct {
	MinATRPct float64 // skip when ATR/price < this (default 0.001 = 0.1%)
}

// NewBreakout returns a Breakout alpha with default settings.
func NewBreakout() *Breakout {
	return &Breakout{MinATRPct: 0.001}
}

func (b *Breakout) Name() string { return "breakout" }

func (b *Breakout) Predict(f alpha.Features) alpha.Signal {
	if f.ATR <= 0 || f.Close <= 0 {
		return alpha.Signal{Reason: "no_atr_or_price"}
	}
	if f.ATR/f.Close < b.MinATRPct {
		return alpha.Signal{Reason: "low_atr"}
	}
	if f.Close > f.High10 {
		strength := (f.Close - f.High10) / f.ATR
		if strength > 1.0 {
			strength = 1.0
		}
		return alpha.Signal{
			Direction: alpha.DirLong,
			Strength:  strength,
			TargetR:   1.5,
			Reason:    fmt.Sprintf("close>%.2f (h10), %.2fATR", f.High10, strength),
		}
	}
	if f.Close < f.Low10 {
		strength := (f.Low10 - f.Close) / f.ATR
		if strength > 1.0 {
			strength = 1.0
		}
		return alpha.Signal{
			Direction: alpha.DirShort,
			Strength:  strength,
			TargetR:   1.5,
			Reason:    fmt.Sprintf("close<%.2f (l10), %.2fATR", f.Low10, strength),
		}
	}
	return alpha.Signal{Reason: "inside_range"}
}
