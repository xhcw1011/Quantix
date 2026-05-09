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
	if s.TargetR != 1.5 {
		t.Fatalf("expected TargetR=1.5 (default), got %f", s.TargetR)
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

func TestBreakout_StrengthCappedAtOne(t *testing.T) {
	a := NewBreakout()
	// Extreme breakout: 100 points above High10 with ATR=3 → raw strength = 33.3, must cap to 1.0
	s := a.Predict(alpha.Features{Close: 2400, High10: 2300, Low10: 2290, ATR: 3.0})
	if s.Direction != 1 {
		t.Fatalf("expected long, got %d", s.Direction)
	}
	if s.Strength != 1.0 {
		t.Fatalf("expected Strength capped at 1.0, got %f", s.Strength)
	}
}

func TestBreakout_RejectZeroATR(t *testing.T) {
	a := NewBreakout()
	// Zero ATR — first guard, before low_atr ratio check
	s := a.Predict(alpha.Features{Close: 2300, High10: 2308, Low10: 2290, ATR: 0})
	if s.Direction != 0 {
		t.Fatalf("expected hold on zero ATR, got %d", s.Direction)
	}
	if s.Reason != "no_atr_or_price" {
		t.Fatalf("expected reason='no_atr_or_price', got %q", s.Reason)
	}
}

func TestBreakout_RejectZeroPrice(t *testing.T) {
	a := NewBreakout()
	s := a.Predict(alpha.Features{Close: 0, High10: 2308, Low10: 2290, ATR: 3.0})
	if s.Direction != 0 || s.Reason != "no_atr_or_price" {
		t.Fatalf("expected zero-price rejection, got Direction=%d Reason=%q", s.Direction, s.Reason)
	}
}
