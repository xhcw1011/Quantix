package alpha

import (
	"testing"
	"time"
)

func TestSignal_ZeroValueIsHold(t *testing.T) {
	var s Signal
	if s.Direction != 0 {
		t.Fatalf("zero Signal should have Direction=0, got %d", s.Direction)
	}
	if s.Strength != 0 {
		t.Fatalf("zero Signal should have Strength=0, got %f", s.Strength)
	}
}

func TestFeatures_HasRequiredFields(t *testing.T) {
	f := Features{
		Now:      time.Now(),
		Close:    2300.0,
		ATR:      3.5,
		High10:   2310.0,
		Low10:    2290.0,
		BBUpper:  2315.0,
		BBLower:  2285.0,
		BBMiddle: 2300.0,
		RSI:      55.0,
		LastBars: []float64{2295, 2298, 2301, 2300, 2299},
	}
	if f.Close != 2300 {
		t.Fatalf("Close not set: %f", f.Close)
	}
}
