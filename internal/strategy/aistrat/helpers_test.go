package aistrat

import (
	"math"
	"testing"

	"github.com/Quantix/quantix/internal/exchange"
)

// closesBars builds a bar slice with the given Close prices (only field
// netMove/momentumDecayed read).
func closesBars(closes ...float64) []exchange.Kline {
	bars := make([]exchange.Kline, len(closes))
	for i, c := range closes {
		bars[i] = exchange.Kline{Close: c}
	}
	return bars
}

func TestRegimeAgeNext(t *testing.T) {
	tests := []struct {
		name       string
		prevAge    int
		sameRegime bool
		want       int
	}{
		{"same regime increments", 5, true, 6},
		{"regime change resets to 0", 5, false, 0},
		{"first bar (age 0) stays 0 on change", 0, false, 0},
		{"first bar (age 0) increments on same", 0, true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := regimeAgeNext(tt.prevAge, tt.sameRegime); got != tt.want {
				t.Errorf("regimeAgeNext(%d, %v) = %d, want %d", tt.prevAge, tt.sameRegime, got, tt.want)
			}
		})
	}
}

func TestTrendAgeBlocked(t *testing.T) {
	tests := []struct {
		name      string
		regimeAge int
		maxAge    int
		want      bool
	}{
		{"disabled: maxAge==0 never blocks", 100, 0, false},
		{"disabled: maxAge negative never blocks", 100, -1, false},
		{"young regime under threshold → allowed", 3, 8, false},
		{"exactly at threshold → allowed", 8, 8, false},
		{"one past threshold → blocked", 9, 8, true},
		{"far past threshold → blocked", 500, 8, true},
		{"fresh regime (age 0) with filter on → allowed", 0, 8, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trendAgeBlocked(tt.regimeAge, tt.maxAge); got != tt.want {
				t.Errorf("trendAgeBlocked(%d, %d) = %v, want %v", tt.regimeAge, tt.maxAge, got, tt.want)
			}
		})
	}
}

func TestPctile(t *testing.T) {
	tests := []struct {
		name   string
		val    float64
		sample []float64
		want   float64
	}{
		{"empty sample → neutral 0.5", 5, nil, 0.5},
		{"val below all → 0", 0, []float64{1, 2, 3}, 0},
		{"val above all → 1", 10, []float64{1, 2, 3}, 1},
		{"val at median of 3 → 2/3", 2, []float64{1, 2, 3}, 2.0 / 3},
		{"val equal to one element counts inclusive (<=)", 1, []float64{1, 1, 2}, 2.0 / 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pctile(tt.val, tt.sample); math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("pctile(%v, %v) = %v, want %v", tt.val, tt.sample, got, tt.want)
			}
		})
	}
}

func TestVolGateScore(t *testing.T) {
	volBars := func(vols ...float64) []exchange.Kline {
		out := make([]exchange.Kline, len(vols))
		for i, v := range vols {
			out[i] = exchange.Kline{Volume: v}
		}
		return out
	}
	tests := []struct {
		name      string
		bars      []exchange.Kline
		window    int
		ratioBars int
		want      float64
	}{
		{"disabled: window<=0 → neutral", volBars(1, 2, 3, 4, 5), 0, 2, 0.5},
		{"disabled: ratioBars<=0 → neutral", volBars(1, 2, 3, 4, 5), 3, 0, 0.5},
		{"not enough history (n<window) → neutral", volBars(1, 2), 5, 2, 0.5},
		{"not enough history (n<=ratioBars) → neutral", volBars(1, 2, 3), 3, 3, 0.5},
		// window=4, ratioBars=2: last bar (10) is the max of the window [1,2,3,10] → vol_hi=1.0 (all 4 <= 10).
		{"last bar is max of window → high vol_hi", volBars(1, 2, 3, 10), 4, 2, -1}, // -1 = "just check >0.5" below
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := volGateScore(tt.bars, tt.window, tt.ratioBars)
			if tt.want == -1 {
				if got <= 0.5 {
					t.Errorf("volGateScore(...) = %v, want > 0.5 (spike bar)", got)
				}
				return
			}
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("volGateScore(...) = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGridAgeSizeScale(t *testing.T) {
	tests := []struct {
		name      string
		regimeAge int
		decayRate float64
		floor     float64
		want      float64
	}{
		{"disabled: decayRate<=0 always full size", 10, 0, 0.2, 1.0},
		{"disabled: decayRate negative always full size", 10, -0.1, 0.2, 1.0},
		{"age 1 (fresh regime) → full size", 1, 0.2, 0.2, 1.0},
		{"age 2 → one step down", 2, 0.2, 0.2, 0.8},
		{"age 3 → two steps down", 3, 0.2, 0.2, 0.6},
		{"age 4 → three steps down", 4, 0.2, 0.2, 0.4},
		{"deep age clamps at floor, never below", 100, 0.2, 0.2, 0.2},
		{"age 0 (shouldn't happen, but doesn't exceed 1.0)", 0, 0.2, 0.2, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gridAgeSizeScale(tt.regimeAge, tt.decayRate, tt.floor)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("gridAgeSizeScale(%d, %.2f, %.2f) = %v, want %v", tt.regimeAge, tt.decayRate, tt.floor, got, tt.want)
			}
		})
	}
}

func TestMomentumDecayed(t *testing.T) {
	tests := []struct {
		name       string
		bars       []exchange.Kline
		lookback   int
		decayRatio float64
		want       bool
	}{
		{"disabled: lookback<=0 never blocks", closesBars(100, 110, 120, 121, 122, 123), 0, 0.5, false},
		{"not enough history → don't block", closesBars(100, 110, 120), 3, 0.5, false},
		// prior window (bars 0-2): 100->120 = +20. recent window (bars 3-5): 120->123 = +3.
		// ratio = 3/20 = 0.15 < 0.5 decayRatio → decayed → blocked.
		{"momentum weakened below ratio → blocked", closesBars(100, 110, 120, 120.5, 121, 123), 3, 0.5, true},
		// prior: 100->120=+20, recent: 120->140=+20. ratio=1.0, not < 0.5 → not blocked.
		{"momentum sustained → allowed", closesBars(100, 110, 120, 130, 135, 140), 3, 0.5, false},
		// prior move is 0 → ratio undefined, don't block.
		{"prior move is zero → don't block", closesBars(100, 100, 100, 105, 108, 110), 3, 0.5, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := momentumDecayed(tt.bars, tt.lookback, tt.decayRatio); got != tt.want {
				t.Errorf("momentumDecayed(...) = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVolatilityElevated(t *testing.T) {
	tests := []struct {
		name     string
		shortATR float64
		longATR  float64
		multiple float64
		want     bool
	}{
		{"disabled: multiple<=0 never blocks", 10, 5, 0, false},
		{"disabled: longATR<=0 never blocks", 10, 0, 1.5, false},
		{"short ATR far above long → blocked", 10, 5, 1.5, true},
		{"short ATR exactly at multiple → not blocked (strict >)", 7.5, 5, 1.5, false},
		{"short ATR below multiple → allowed", 6, 5, 1.5, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := volatilityElevated(tt.shortATR, tt.longATR, tt.multiple); got != tt.want {
				t.Errorf("volatilityElevated(%.2f, %.2f, %.2f) = %v, want %v",
					tt.shortATR, tt.longATR, tt.multiple, got, tt.want)
			}
		})
	}
}

func TestResolveInterval(t *testing.T) {
	tests := []struct {
		name string
		iv   string
		want string
	}{
		{"empty defaults to 15m", "", "15m"},
		{"explicit 15m passes through", "15m", "15m"},
		{"explicit 4h passes through unchanged", "4h", "4h"},
		{"explicit 1h passes through unchanged", "1h", "1h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveInterval(tt.iv); got != tt.want {
				t.Errorf("resolveInterval(%q) = %q, want %q", tt.iv, got, tt.want)
			}
		})
	}
}

func TestShouldWarnMissingInterval(t *testing.T) {
	tests := []struct {
		name       string
		iv         string
		barsLoaded int
		want       bool
	}{
		{"default 15m with no bars → no warn (early warmup, not misconfig)", "15m", 0, false},
		{"default 15m with bars → no warn", "15m", 100, false},
		{"non-default configured, data missing → warn", "4h", 0, true},
		{"non-default configured, data present → no warn", "4h", 50, false},
		{"non-default 1h configured, data missing → warn", "1h", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldWarnMissingInterval(tt.iv, tt.barsLoaded); got != tt.want {
				t.Errorf("shouldWarnMissingInterval(%q, %d) = %v, want %v", tt.iv, tt.barsLoaded, got, tt.want)
			}
		})
	}
}

func TestHTFMisaligned(t *testing.T) {
	tests := []struct {
		name   string
		side   string
		htfDir int
		want   bool
	}{
		{"LONG with confirmed downtrend → blocked", "LONG", -1, true},
		{"LONG with confirmed uptrend → allowed", "LONG", 1, false},
		{"LONG with neutral htf → allowed", "LONG", 0, false},
		{"SHORT with confirmed uptrend → blocked", "SHORT", 1, true},
		{"SHORT with confirmed downtrend → allowed", "SHORT", -1, false},
		{"SHORT with neutral htf → allowed", "SHORT", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := htfMisaligned(tt.side, tt.htfDir); got != tt.want {
				t.Errorf("htfMisaligned(%s, %d) = %v, want %v", tt.side, tt.htfDir, got, tt.want)
			}
		})
	}
}

func TestAvgVolume(t *testing.T) {
	bars := func(vols ...float64) []exchange.Kline {
		out := make([]exchange.Kline, len(vols))
		for i, v := range vols {
			out[i] = exchange.Kline{Volume: v}
		}
		return out
	}
	tests := []struct {
		name string
		bars []exchange.Kline
		n    int
		want float64
	}{
		{"disabled: n<=0", bars(10, 20, 30), 0, 0},
		{"not enough history", bars(10, 20), 3, 0},
		// last bar (30) excluded from the average of the preceding 3 (10,20,15).
		{"averages preceding n bars, excludes last", bars(10, 20, 15, 30), 3, 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := avgVolume(tt.bars, tt.n); got != tt.want {
				t.Errorf("avgVolume(...) = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVolumeInsufficient(t *testing.T) {
	tests := []struct {
		name                         string
		currentVol, avgVol, multiple float64
		want                         bool
	}{
		{"disabled: multiple<=0 never blocks", 5, 10, 0, false},
		{"disabled: avgVol<=0 never blocks", 5, 0, 1.0, false},
		{"current below required multiple → blocked", 5, 10, 1.0, true},
		{"current exactly at required multiple → allowed", 10, 10, 1.0, false},
		{"current above required multiple → allowed", 15, 10, 1.0, false},
		{"multiple > 1 raises the bar", 12, 10, 1.5, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := volumeInsufficient(tt.currentVol, tt.avgVol, tt.multiple); got != tt.want {
				t.Errorf("volumeInsufficient(%.2f, %.2f, %.2f) = %v, want %v",
					tt.currentVol, tt.avgVol, tt.multiple, got, tt.want)
			}
		})
	}
}

func TestPriceExtended(t *testing.T) {
	tests := []struct {
		name                       string
		side                       string
		price, swingLow, swingHigh float64
		atr, maxATRDist            float64
		want                       bool
	}{
		{"disabled: maxATRDist<=0 never blocks", "LONG", 120, 100, 130, 5, 0, false},
		{"disabled: atr<=0 never blocks", "LONG", 120, 100, 130, 0, 3, false},
		{"LONG far above swing low → blocked", "LONG", 120, 100, 130, 5, 3, true},
		{"LONG close to swing low → allowed", "LONG", 105, 100, 130, 5, 3, false},
		{"SHORT far below swing high → blocked", "SHORT", 110, 90, 130, 5, 3, true},
		{"SHORT close to swing high → allowed", "SHORT", 125, 90, 130, 5, 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := priceExtended(tt.side, tt.price, tt.swingLow, tt.swingHigh, tt.atr, tt.maxATRDist); got != tt.want {
				t.Errorf("priceExtended(%s, ...) = %v, want %v", tt.side, got, tt.want)
			}
		})
	}
}
