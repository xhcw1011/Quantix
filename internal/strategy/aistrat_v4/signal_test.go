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
