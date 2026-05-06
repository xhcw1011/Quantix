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

func TestCalcQtyNormal(t *testing.T) {
	// equity 5000, risk 0.5%, sl distance $20 → qty = $25 / $20 = 1.25
	q := calcQty(5000, 0.005, 20.0, 100.0, 0.20, 2.0)
	if math.Abs(q-1.25) > 0.001 {
		t.Errorf("calcQty = %f, want 1.25", q)
	}
}

func TestCalcQtyZeroDistance(t *testing.T) {
	// SL distance 0 → fall back to leverage cap
	// equity 5000, leverage 2, price 100, max 0.20 → max qty = 5000 * 0.20 * 2 / 100 = 20
	q := calcQty(5000, 0.005, 0.0, 100.0, 0.20, 2.0)
	if q != 20 {
		t.Errorf("calcQty zero-distance = %f, want 20 (leverage cap)", q)
	}
}

func TestCalcQtyMaxPositionCap(t *testing.T) {
	// equity 5000, risk 5% (huge), sl distance $1, price $100, max 0.20, leverage 2
	// risk-based: 5000 * 0.05 / 1 = 250 qty
	// max:        5000 * 0.20 * 2 / 100 = 20 qty
	// → 20 (cap binds)
	q := calcQty(5000, 0.05, 1.0, 100.0, 0.20, 2.0)
	if q != 20 {
		t.Errorf("calcQty = %f, want 20 (max cap)", q)
	}
}

func TestShouldExitNoPosition(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	closes := []float64{100, 100, 100}
	highs := []float64{101, 101, 101}
	lows := []float64{99, 99, 99}
	closeIt, reason := shouldExit(closes, highs, lows, cfg, nil, 0)
	if closeIt {
		t.Errorf("shouldExit = (true, %s), want (false, _) when no position", reason)
	}
}

func TestShouldExitTPHitFromShort(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 0.0)
	pos := &positionState{Side: "SHORT", EntryBar: 5}
	closeIt, reason := shouldExit(c, h, l, cfg, pos, 10)
	if !closeIt || reason != "tp" {
		t.Errorf("shouldExit = (%v, %s), want (true, tp)", closeIt, reason)
	}
}

func TestShouldExitTPHitFromLong(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 0.0)
	pos := &positionState{Side: "LONG", EntryBar: 5}
	closeIt, reason := shouldExit(c, h, l, cfg, pos, 10)
	if !closeIt || reason != "tp" {
		t.Errorf("shouldExit = (%v, %s), want (true, tp)", closeIt, reason)
	}
}

func TestShouldExitSLHitFromShort(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 3.6) // SHORT entered low; price spikes high → SL
	pos := &positionState{Side: "SHORT", EntryBar: 5}
	closeIt, reason := shouldExit(c, h, l, cfg, pos, 10)
	if !closeIt || reason != "sl" {
		t.Errorf("shouldExit = (%v, %s), want (true, sl)", closeIt, reason)
	}
}

func TestShouldExitSLHitFromLong(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, -3.6)
	pos := &positionState{Side: "LONG", EntryBar: 5}
	closeIt, reason := shouldExit(c, h, l, cfg, pos, 10)
	if !closeIt || reason != "sl" {
		t.Errorf("shouldExit = (%v, %s), want (true, sl)", closeIt, reason)
	}
}

func TestShouldExitTimeStop(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	cfg.TimeStopBars = 5
	// z = +1 (no TP for SHORT, no SL), bars held = 5 → time stop
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 1.0)
	pos := &positionState{Side: "SHORT", EntryBar: 0}
	closeIt, reason := shouldExit(c, h, l, cfg, pos, 5)
	if !closeIt || reason != "time" {
		t.Errorf("shouldExit = (%v, %s), want (true, time)", closeIt, reason)
	}
}

func TestShouldExitNoExitConditions(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	// z = +1 (SHORT not at TP, |z|<3.5), bars held 2 < TimeStop 12
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 1.0)
	pos := &positionState{Side: "SHORT", EntryBar: 8}
	closeIt, _ := shouldExit(c, h, l, cfg, pos, 10)
	if closeIt {
		t.Error("shouldExit returned true, want false (no condition met)")
	}
}
