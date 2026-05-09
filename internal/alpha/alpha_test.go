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
	}
	if f.Close != 2300 {
		t.Fatalf("Close not set: %f", f.Close)
	}
}

// Verify the Alpha interface is implementable and Signal returns by value (no aliasing).
type fakeAlpha struct {
	name string
	out  Signal
}

func (f *fakeAlpha) Name() string              { return f.name }
func (f *fakeAlpha) Predict(_ Features) Signal { return f.out }

func TestAlphaInterface_BasicContract(t *testing.T) {
	a := &fakeAlpha{name: "fake", out: Signal{Direction: DirLong, Strength: 0.5, TargetR: 1.5, Reason: "test"}}
	if a.Name() != "fake" {
		t.Fatalf("Name=%q", a.Name())
	}
	got := a.Predict(Features{Close: 2300})
	if got.Direction != DirLong || got.Strength != 0.5 || got.TargetR != 1.5 || got.Reason != "test" {
		t.Fatalf("Predict returned wrong Signal: %+v", got)
	}

	// Mutating the returned Signal must not affect the source — value semantics.
	got.Direction = DirShort
	got2 := a.Predict(Features{})
	if got2.Direction != DirLong {
		t.Fatalf("Signal aliasing detected: source mutated to %d", got2.Direction)
	}
}

func TestDirection_Constants(t *testing.T) {
	// Pin the wire encoding so accidental refactors break loudly.
	if DirShort != -1 || DirHold != 0 || DirLong != 1 {
		t.Fatalf("Direction constants drifted: short=%d hold=%d long=%d", DirShort, DirHold, DirLong)
	}
}
