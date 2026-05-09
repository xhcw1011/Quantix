package baseline

import (
	"testing"

	"github.com/Quantix/quantix/internal/alpha"
)

func TestBreakout_LongOnNewHigh(t *testing.T) {
	a := NewBreakout()
	f := alpha.Features{
		Symbol: "ETHUSDT",
		Close:  2310.5,
		High10: 2308.0,
		Low10:  2290.0,
		ATR:    3.0,
	}
	s := a.Predict(f)
	if s.Direction != 1 {
		t.Fatalf("expected Direction=1 (long) got %d", s.Direction)
	}
	if s.Strength <= 0 {
		t.Fatalf("expected Strength>0 got %f", s.Strength)
	}
}

func TestBreakout_ShortOnNewLow(t *testing.T) {
	a := NewBreakout()
	f := alpha.Features{
		Symbol: "ETHUSDT",
		Close:  2289.0,
		High10: 2308.0,
		Low10:  2290.0,
		ATR:    3.0,
	}
	s := a.Predict(f)
	if s.Direction != -1 {
		t.Fatalf("expected Direction=-1 (short) got %d", s.Direction)
	}
}

func TestBreakout_HoldInsideRange(t *testing.T) {
	a := NewBreakout()
	f := alpha.Features{
		Close:  2300.0,
		High10: 2308.0,
		Low10:  2290.0,
		ATR:    3.0,
	}
	s := a.Predict(f)
	if s.Direction != 0 {
		t.Fatalf("expected hold inside range, got %d", s.Direction)
	}
}

func TestBreakout_RejectLowATR(t *testing.T) {
	a := NewBreakout()
	f := alpha.Features{
		Close:  2310.5,
		High10: 2308.0,
		Low10:  2290.0,
		ATR:    0.5, // 0.022% of price < 0.1% gate
	}
	s := a.Predict(f)
	if s.Direction != 0 {
		t.Fatalf("low ATR should yield hold, got %d (reason=%s)", s.Direction, s.Reason)
	}
}

func TestBreakout_StrengthScalesWithMagnitude(t *testing.T) {
	a := NewBreakout()
	weak := a.Predict(alpha.Features{Close: 2308.5, High10: 2308.0, Low10: 2290.0, ATR: 3.0})
	strong := a.Predict(alpha.Features{Close: 2316.0, High10: 2308.0, Low10: 2290.0, ATR: 3.0})
	if strong.Strength <= weak.Strength {
		t.Fatalf("strong (%f) should exceed weak (%f)", strong.Strength, weak.Strength)
	}
}
