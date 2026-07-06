package pool

import (
	"math"
	"testing"
)

func ms(realized, unrealized, longN, shortN float64) MemberState {
	return MemberState{Realized: realized, Unrealized: unrealized, LongNotional: longN, ShortNotional: shortN}
}

func TestPoolEquitySplitAndDirectionalExposure(t *testing.T) {
	p := New(Config{Name: "Growth", NotionalCap: 10000, MaxLongExp: 1.0, MaxShortExp: 0.5})
	s := p.Update([]MemberState{
		ms(100, -50, 3000, 1000),
		ms(0, 200, 2000, 0),
	})
	// realized 100, unrealized 150 → equity 10250
	if s.Realized != 100 || s.Unrealized != 150 {
		t.Fatalf("realized/unrealized split wrong: %+v", s)
	}
	if s.Equity != 10250 {
		t.Fatalf("equity = %v want 10250", s.Equity)
	}
	// directional, NOT net: long 5000, short 1000 each / equity
	if math.Abs(s.LongExp-5000.0/10250) > 1e-9 || math.Abs(s.ShortExp-1000.0/10250) > 1e-9 {
		t.Fatalf("directional exposure wrong: long %v short %v", s.LongExp, s.ShortExp)
	}
	if s.MaxLongExp != 1.0 || s.MaxShortExp != 0.5 {
		t.Fatalf("caps not published: %+v", s)
	}
}

func TestPoolHaltAndRecoveryHysteresis(t *testing.T) {
	p := New(Config{Name: "Growth", NotionalCap: 10000, MaxDrawdown: 0.10, RecoverDrawdown: 0.05, RecoverBars: 2})
	if p.Update(nil).Status != Active {
		t.Fatal("should start ACTIVE")
	}
	if p.Update([]MemberState{ms(0, -1200, 0, 0)}).Status != Halted {
		t.Fatal("12% DD ≥ 10% → HALT")
	}
	if p.Update([]MemberState{ms(0, -600, 0, 0)}).Status != Halted {
		t.Fatal("6% DD is in the dead band (5–10%) → stay HALTED")
	}
	if p.Update([]MemberState{ms(0, -400, 0, 0)}).Status != Halted {
		t.Fatal("4% DD bar 1 of RecoverBars=2 → still HALTED (persistence)")
	}
	if p.Update([]MemberState{ms(0, -400, 0, 0)}).Status != Active {
		t.Fatal("4% DD held RecoverBars → un-halt to ACTIVE")
	}
}

func TestPoolNoMarkBounceRecovery(t *testing.T) {
	p := New(Config{NotionalCap: 10000, MaxDrawdown: 0.10, RecoverDrawdown: 0.05, RecoverBars: 2})
	p.Update([]MemberState{ms(0, -1200, 0, 0)}) // HALT
	p.Update([]MemberState{ms(0, -400, 0, 0)})  // recovery bar 1
	if p.Update([]MemberState{ms(0, -800, 0, 0)}).Status != Halted {
		t.Fatal("an 8% mark bounce resets the streak → stay HALTED")
	}
	p.Update([]MemberState{ms(0, -400, 0, 0)}) // fresh bar 1
	if p.Update([]MemberState{ms(0, -400, 0, 0)}).Status != Active {
		t.Fatal("un-halt only after a fresh durable RecoverBars")
	}
}
