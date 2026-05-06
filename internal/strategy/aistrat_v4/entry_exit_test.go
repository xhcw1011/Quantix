package aistrat_v4

import (
	"math"
	"testing"
)

// barsAtZ builds (highs, lows, closes) such that the latest close has the given z-score
// from a flat SMA-window of `mean` price with given `std`.
func barsAtZ(t *testing.T, lookback int, mean, std, targetZ float64) (h, l, c []float64) {
	t.Helper()
	c = make([]float64, lookback+1)
	h = make([]float64, lookback+1)
	l = make([]float64, lookback+1)
	for i := 0; i < lookback; i++ {
		if i%2 == 0 {
			c[i] = mean + std
		} else {
			c[i] = mean - std
		}
		h[i] = c[i] + 0.5
		l[i] = c[i] - 0.5
	}
	c[lookback] = mean + targetZ*std
	h[lookback] = c[lookback] + 0.5
	l[lookback] = c[lookback] - 0.5
	return
}

func TestShouldEnterAboveThreshold_ShortSignal(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 3.0) // z = +3 → SHORT
	side, reason := shouldEnter(c, h, l, cfg, nil, 0)
	if side != "SHORT" {
		t.Errorf("side = %q (reason=%s), want SHORT", side, reason)
	}
}

func TestShouldEnterBelowThreshold_LongSignal(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, -3.0) // z = -3 → LONG
	side, reason := shouldEnter(c, h, l, cfg, nil, 0)
	if side != "LONG" {
		t.Errorf("side = %q (reason=%s), want LONG", side, reason)
	}
}

func TestShouldEnterBelowZThreshold_NoSignal(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 1.5) // z = +1.5 < 2.5
	side, _ := shouldEnter(c, h, l, cfg, nil, 0)
	if side != "" {
		t.Errorf("side = %q, want empty (z below threshold)", side)
	}
}

func TestShouldEnterPositionExists_NoSignal(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 3.0)
	pos := &positionState{Side: "LONG"}
	side, _ := shouldEnter(c, h, l, cfg, pos, 0)
	if side != "" {
		t.Errorf("side = %q, want empty (already in position)", side)
	}
}

func TestShouldEnterATRFloorBlocks(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	cfg.MinATRPct = 0.20 // 20% — far above typical alternating-bar ATR (~9%)
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 3.0)
	side, reason := shouldEnter(c, h, l, cfg, nil, 0)
	if side != "" {
		t.Errorf("side = %q (reason=%s), want empty (ATR floor block)", side, reason)
	}
}

func TestShouldEnterCooldownBlocksSameSide(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	cfg.CooldownBars = 5
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 3.0) // z = +3 → SHORT
	cd := cooldown{LastShortCloseBar: 18}
	side, _ := shouldEnter(c, h, l, cfg, nil, 20, withCooldown(cd))
	if side != "" {
		t.Errorf("side = %q, want empty (SHORT cooldown active: bar 20 - 18 = 2 < 5)", side)
	}
}

func TestShouldEnterCooldownAllowsOppositeSide(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	cfg.CooldownBars = 5
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, -3.0) // z = -3 → LONG
	cd := cooldown{LastShortCloseBar: 18}             // SHORT cooldown does not block LONG
	side, _ := shouldEnter(c, h, l, cfg, nil, 20, withCooldown(cd))
	if side != "LONG" {
		t.Errorf("side = %q, want LONG (SHORT cooldown does not block LONG)", side)
	}
}

// math used by ATR test
var _ = math.Abs
