package indicator

import (
	"math"
	"testing"
)

func TestATR_KnownInputs(t *testing.T) {
	// 5 bars, period=3. TR is constant=2 across all bars (H-L=2, prev close
	// always in [L,H] so |H-prevClose|<=2 and |L-prevClose|<=2). Wilder of
	// constant TR = TR, so ATR[3] = ATR[4] = 2 exactly.
	highs := []float64{12, 13, 14, 15, 14}
	lows := []float64{10, 11, 12, 13, 12}
	closes := []float64{11, 12, 13, 14, 13}
	out := ATR(highs, lows, closes, 3)
	want := []float64{0, 0, 0, 2.0, 2.0}
	if len(out) != len(want) {
		t.Fatalf("len=%d want %d", len(out), len(want))
	}
	for i, w := range want {
		if math.Abs(out[i]-w) > 1e-9 {
			t.Fatalf("out[%d]=%v want %v", i, out[i], w)
		}
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

func TestATR_WilderRecurrence(t *testing.T) {
	// 5 bars, period=3. Bars 0-3 give TR=2 each, bar 4 gives TR=10
	// (sharp spike). Wilder: ATR[4] = (ATR[3]*(3-1) + 10)/3 = (2*2+10)/3 = 14/3 ≈ 4.6667
	// Rolling SMA over last 3 TRs would give: (2+2+10)/3 = 14/3 ≈ 4.6667 — coincidentally
	// equal here. So we extend to 6 bars: bars 0-3 TR=2, bar 4 TR=10, bar 5 TR=2.
	// Wilder: ATR[5] = (ATR[4]*(3-1) + 2)/3 = (4.6667*2 + 2)/3 ≈ 3.7778
	// Rolling SMA over last 3 TRs: (2+10+2)/3 ≈ 4.6667
	// These DIFFER → test pins Wilder behavior.
	highs := []float64{12, 13, 14, 15, 24, 14}
	lows := []float64{10, 11, 12, 13, 14, 12}
	closes := []float64{11, 12, 13, 14, 14, 13}
	out := ATR(highs, lows, closes, 3)
	wantBar4 := (2.0*2 + 10.0) / 3.0
	wantBar5 := (wantBar4*2 + 2.0) / 3.0
	if math.Abs(out[4]-wantBar4) > 1e-9 {
		t.Fatalf("out[4]=%v want %v (Wilder seed handling broken)", out[4], wantBar4)
	}
	if math.Abs(out[5]-wantBar5) > 1e-9 {
		t.Fatalf("out[5]=%v want %v (Wilder recurrence broken)", out[5], wantBar5)
	}
}
