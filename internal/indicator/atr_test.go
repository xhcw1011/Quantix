package indicator

import (
	"math"
	"testing"
)

func TestATR_KnownInputs(t *testing.T) {
	// 5 bars, period=3. TR_i = max(H-L, |H-prevClose|, |L-prevClose|)
	highs := []float64{12, 13, 14, 15, 14}
	lows := []float64{10, 11, 12, 13, 12}
	closes := []float64{11, 12, 13, 14, 13}
	out := ATR(highs, lows, closes, 3)
	if len(out) != len(highs) {
		t.Fatalf("len=%d want %d", len(out), len(highs))
	}
	if math.IsNaN(out[len(out)-1]) || out[len(out)-1] <= 0 {
		t.Fatalf("last ATR invalid: %v", out)
	}
}

func TestATR_TooFewBars(t *testing.T) {
	out := ATR([]float64{1, 2}, []float64{0, 1}, []float64{1, 2}, 14)
	for _, v := range out {
		if v != 0 {
			t.Fatalf("expected zeros for insufficient bars, got %v", out)
		}
	}
}
