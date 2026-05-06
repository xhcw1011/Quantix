package aistrat_v4

import (
	"math"
	"testing"
)

func TestZScoreInsufficientBars(t *testing.T) {
	closes := []float64{100, 101, 102}
	z := zScore(closes, 20)
	if z != 0 {
		t.Errorf("zScore with insufficient bars = %f, want 0", z)
	}
}

func TestZScoreFlatPrices(t *testing.T) {
	closes := make([]float64, 25)
	for i := range closes {
		closes[i] = 100.0
	}
	z := zScore(closes, 20)
	if z != 0 {
		t.Errorf("zScore with flat prices = %f, want 0 (std=0 case)", z)
	}
}

func TestZScoreKnownInput(t *testing.T) {
	// 21 prices: 20 baseline (100..119), then 130 = clearly above mean
	// SMA(20) = 109.5; std(20) ≈ 5.766; z ≈ 3.55
	closes := make([]float64, 21)
	for i := 0; i < 20; i++ {
		closes[i] = float64(100 + i)
	}
	closes[20] = 130
	z := zScore(closes, 20)
	want := 3.55
	if math.Abs(z-want) > 0.05 {
		t.Errorf("zScore = %f, want approx %f", z, want)
	}
}

func TestZScoreNegative(t *testing.T) {
	closes := make([]float64, 21)
	for i := 0; i < 20; i++ {
		closes[i] = float64(100 + i)
	}
	closes[20] = 90
	z := zScore(closes, 20)
	if z >= 0 {
		t.Errorf("zScore for low price = %f, want negative", z)
	}
}

func TestATRInsufficientBars(t *testing.T) {
	highs := []float64{105, 106}
	lows := []float64{95, 96}
	closes := []float64{100, 101}
	a := atr(highs, lows, closes, 14)
	if a != 0 {
		t.Errorf("atr with insufficient bars = %f, want 0", a)
	}
}

func TestATRKnownInput(t *testing.T) {
	// 15 bars where each has range 10 and consecutive closes flat at 100.
	// True range = high - low = 10 for every bar (no gap from prevClose).
	// ATR = 10.
	highs := make([]float64, 15)
	lows := make([]float64, 15)
	closes := make([]float64, 15)
	for i := range closes {
		highs[i] = 105
		lows[i] = 95
		closes[i] = 100
	}
	a := atr(highs, lows, closes, 14)
	if math.Abs(a-10) > 0.001 {
		t.Errorf("atr = %f, want 10.0", a)
	}
}

func TestATRWithGaps(t *testing.T) {
	// Bar 0: close 100. Bars 1-14: high 110 low 105 close 108.
	// First TR (bar 1): max(5, |110-100|, |105-100|) = max(5,10,5) = 10
	// Subsequent TRs (bars 2-14): max(5, |110-108|, |105-108|) = max(5,2,3) = 5
	// ATR over last 14 bars = (10 + 13*5) / 14 = 75/14 ≈ 5.357
	highs := []float64{100}
	lows := []float64{100}
	closes := []float64{100}
	for i := 1; i < 15; i++ {
		highs = append(highs, 110)
		lows = append(lows, 105)
		closes = append(closes, 108)
	}
	a := atr(highs, lows, closes, 14)
	if math.Abs(a-5.357) > 0.05 {
		t.Errorf("atr with gap = %f, want approx 5.357", a)
	}
}

func TestZScoreUsesLastNBars(t *testing.T) {
	// 26 prices total. First 5 are outlier values (50-54) that should be IGNORED by lookback=20.
	// Next 20 alternate 95/105 → window mean=100, std=5.
	// Current = 130 → z = (130-100)/5 = 6.0 (clearly positive).
	closes := []float64{}
	for i := 0; i < 5; i++ {
		closes = append(closes, float64(50+i)) // ignored noise
	}
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			closes = append(closes, 95.0)
		} else {
			closes = append(closes, 105.0)
		}
	}
	closes = append(closes, 130.0)
	z := zScore(closes, 20)
	if z <= 5.5 || z >= 6.5 {
		t.Errorf("zScore = %f, want approx 6.0 (window-only sees alternating 95/105)", z)
	}
}
