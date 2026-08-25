package guardian

import "testing"

func TestProtection_InitialStopModes(t *testing.T) {
	cases := []struct {
		name     string
		side     string
		entry    float64
		cfg      ProtectionConfig
		atr      float64
		wantStop float64
		wantR    float64
	}{
		{"long pct", SideLong, 100, ProtectionConfig{StopMode: StopPct, StopValue: 0.05}, 0, 95, 5},
		{"short pct", SideShort, 100, ProtectionConfig{StopMode: StopPct, StopValue: 0.05}, 0, 105, 5},
		{"long price", SideLong, 100, ProtectionConfig{StopMode: StopPrice, StopValue: 92}, 0, 92, 8},
		{"short price", SideShort, 100, ProtectionConfig{StopMode: StopPrice, StopValue: 108}, 0, 108, 8},
		{"long atr", SideLong, 100, ProtectionConfig{StopMode: StopATR, StopValue: 2}, 3, 94, 6},
		{"short atr", SideShort, 100, ProtectionConfig{StopMode: StopATR, StopValue: 2}, 3, 106, 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := NewProtection(c.side, c.entry, 1, c.cfg, c.atr)
			if !approx(p.Stop, c.wantStop) {
				t.Fatalf("stop=%v want %v", p.Stop, c.wantStop)
			}
			if !approx(p.R, c.wantR) {
				t.Fatalf("R=%v want %v", p.R, c.wantR)
			}
		})
	}
}

func TestProtection_PnlR(t *testing.T) {
	pl := NewProtection(SideLong, 100, 1, ProtectionConfig{StopMode: StopPct, StopValue: 0.05}, 0)
	if !approx(pl.PnlR(105), 1) || !approx(pl.PnlR(95), -1) {
		t.Fatalf("long pnlR wrong: %v %v", pl.PnlR(105), pl.PnlR(95))
	}
	ps := NewProtection(SideShort, 100, 1, ProtectionConfig{StopMode: StopPct, StopValue: 0.05}, 0)
	if !approx(ps.PnlR(95), 1) || !approx(ps.PnlR(105), -1) {
		t.Fatalf("short pnlR wrong: %v %v", ps.PnlR(95), ps.PnlR(105))
	}
}

func TestProtection_TrailRatchetsAndNeverLoosens_Long(t *testing.T) {
	cfg := ProtectionConfig{
		StopMode: StopPct, StopValue: 0.05,
		TrailEnabled: true, ActivateR: 1, TrailMode: TrailR, TrailValue: 1,
	}
	p := NewProtection(SideLong, 100, 1, cfg, 0) // stop=95, R=5

	p.UpdateStop(104, 0) // pnlR 0.8 < 1: not activated
	if p.activated || !approx(p.Stop, 95) {
		t.Fatalf("should not activate yet: activated=%v stop=%v", p.activated, p.Stop)
	}
	p.UpdateStop(106, 0) // pnlR 1.2: activate, stop -> 106-1*5=101
	if !p.activated || !approx(p.Stop, 101) {
		t.Fatalf("activate+trail: activated=%v stop=%v want 101", p.activated, p.Stop)
	}
	p.UpdateStop(110, 0) // stop -> 105
	if !approx(p.Stop, 105) {
		t.Fatalf("ratchet up: stop=%v want 105", p.Stop)
	}
	p.UpdateStop(107, 0) // candidate 102 < 105: must NOT loosen
	if !approx(p.Stop, 105) {
		t.Fatalf("never loosen: stop=%v want 105", p.Stop)
	}
	if !p.StopHit(104) {
		t.Fatalf("104 <= trailed stop 105 should be a hit")
	}
}

func TestProtection_TrailRatchetsAndNeverLoosens_Short(t *testing.T) {
	cfg := ProtectionConfig{
		StopMode: StopPct, StopValue: 0.05,
		TrailEnabled: true, ActivateR: 1, TrailMode: TrailR, TrailValue: 1,
	}
	p := NewProtection(SideShort, 100, 1, cfg, 0) // stop=105, R=5

	p.UpdateStop(94, 0) // pnlR 1.2: activate, stop -> 94+5=99
	if !p.activated || !approx(p.Stop, 99) {
		t.Fatalf("short activate: stop=%v want 99", p.Stop)
	}
	p.UpdateStop(90, 0) // stop -> 95
	p.UpdateStop(93, 0) // candidate 98 > 95: must NOT loosen
	if !approx(p.Stop, 95) {
		t.Fatalf("short never loosen: stop=%v want 95", p.Stop)
	}
	if !p.StopHit(96) {
		t.Fatalf("96 >= trailed stop 95 should be a hit")
	}
}

func TestProtection_BreakEvenMovesStopToEntry(t *testing.T) {
	cfg := ProtectionConfig{StopMode: StopPct, StopValue: 0.05, BreakEvenAtR: 1} // trailing OFF
	p := NewProtection(SideLong, 100, 1, cfg, 0)                                 // stop 95, R 5

	p.UpdateStop(104, 0) // pnlR 0.8 < 1: no break-even yet
	if !approx(p.Stop, 95) {
		t.Fatalf("before break-even stop=%v want 95", p.Stop)
	}
	p.UpdateStop(106, 0) // pnlR 1.2 >= 1: move stop to entry (break-even)
	if !approx(p.Stop, 100) {
		t.Fatalf("break-even should move stop to entry 100, got %v", p.Stop)
	}
	p.UpdateStop(103, 0) // pnlR 0.6: stop must not loosen back below entry
	if !approx(p.Stop, 100) {
		t.Fatalf("break-even stop must not loosen, got %v", p.Stop)
	}
}

func TestProtection_BreakEvenShort(t *testing.T) {
	cfg := ProtectionConfig{StopMode: StopPct, StopValue: 0.05, BreakEvenAtR: 1}
	p := NewProtection(SideShort, 100, 1, cfg, 0) // stop 105, R 5
	p.UpdateStop(94, 0)                           // pnlR 1.2 -> break-even to entry
	if !approx(p.Stop, 100) {
		t.Fatalf("short break-even should move stop to entry 100, got %v", p.Stop)
	}
}

// TestProtection_PeakProfitLockFrac_Long reproduces the 2026-08-24 real-money
// shape: a modest peak (0.61R, matching the observed ~$11.7 peak on a $2400 R)
// where a fixed 0.5R trail distance would consume most of that peak (leaving
// only ~0.11R, ~18%) before triggering. PeakProfitLockFrac=0.7 should instead
// lock in 70% of whatever peak was reached and trigger sooner, on the way
// down, rather than waiting for the wider fixed-distance trail.
func TestProtection_PeakProfitLockFrac_Long(t *testing.T) {
	cfg := ProtectionConfig{
		StopMode: StopPct, StopValue: 0.03, // R = 3 on entry 100
		TrailEnabled: true, ActivateR: 0.3, TrailMode: TrailR, TrailValue: 0.5,
		PeakProfitLockFrac: 0.7,
	}
	p := NewProtection(SideLong, 100, 1, cfg, 0) // stop=97, R=3

	p.UpdateStop(101.83, 0) // pnlR 0.61 (peak) — lock candidate (101.281) beats trail candidate (100.33)
	if !approx(p.Stop, 101.281) {
		t.Fatalf("peak-lock should govern at the peak: stop=%v want 101.281", p.Stop)
	}

	p.UpdateStop(100.5, 0) // price pulls back; PeakR unchanged, stop must not move
	if !approx(p.Stop, 101.281) {
		t.Fatalf("stop must hold at the peak-lock level through a pullback: stop=%v want 101.281", p.Stop)
	}
	if !p.StopHit(100.5) {
		t.Fatalf("100.5 <= peak-lock stop 101.281 should be a hit — this is the giveback the fix closes")
	}
	// Without the lock, the plain 0.5R trail would only have reached 100.33
	// (0.11R of the 0.61R peak, ~18%) and would NOT have triggered yet at 100.5.
}

func TestProtection_PeakProfitLockFrac_Short(t *testing.T) {
	cfg := ProtectionConfig{
		StopMode: StopPct, StopValue: 0.03, // R = 3 on entry 100
		TrailEnabled: true, ActivateR: 0.3, TrailMode: TrailR, TrailValue: 0.5,
		PeakProfitLockFrac: 0.7,
	}
	p := NewProtection(SideShort, 100, 1, cfg, 0) // stop=103, R=3

	p.UpdateStop(98.17, 0) // pnlR 0.61 (peak) — mirrors the long case
	if !approx(p.Stop, 98.719) {
		t.Fatalf("peak-lock should govern at the peak: stop=%v want 98.719", p.Stop)
	}
	p.UpdateStop(99.5, 0) // pulls back against the short; stop must hold
	if !approx(p.Stop, 98.719) {
		t.Fatalf("stop must hold through a pullback: stop=%v want 98.719", p.Stop)
	}
	if !p.StopHit(99.5) {
		t.Fatalf("99.5 >= peak-lock stop 98.719 should be a hit")
	}
}

// TestProtection_PeakProfitLockFrac_DisabledMatchesOldBehaviour confirms the
// zero-value (unset in a literal ProtectionConfig, as every pre-existing test
// in this file constructs it) leaves the plain distance-based trail as the
// sole mechanism — no behaviour change for callers that don't opt in.
func TestProtection_PeakProfitLockFrac_DisabledMatchesOldBehaviour(t *testing.T) {
	cfg := ProtectionConfig{
		StopMode: StopPct, StopValue: 0.03,
		TrailEnabled: true, ActivateR: 0.3, TrailMode: TrailR, TrailValue: 0.5,
		// PeakProfitLockFrac left at zero value.
	}
	p := NewProtection(SideLong, 100, 1, cfg, 0)
	p.UpdateStop(101.83, 0) // pnlR 0.61 — with the lock off, plain trail governs
	if !approx(p.Stop, 100.33) {
		t.Fatalf("stop=%v want 100.33 (plain 0.5R trail, lock disabled)", p.Stop)
	}
}

func TestProtection_TakeProfit(t *testing.T) {
	// R-multiple TP, long: entry 100, R 5, TP 3R -> 115
	p := NewProtection(SideLong, 100, 1, ProtectionConfig{
		StopMode: StopPct, StopValue: 0.05, TPMode: TPR, TPValue: 3,
	}, 0)
	if !p.TPHit(115) || p.TPHit(114.99) {
		t.Fatalf("long TPR hit wrong: %v %v", p.TPHit(115), p.TPHit(114.99))
	}
	// pct TP, short: entry 100, 10% -> 90
	ps := NewProtection(SideShort, 100, 1, ProtectionConfig{
		StopMode: StopPct, StopValue: 0.05, TPMode: TPPct, TPValue: 0.10,
	}, 0)
	if !ps.TPHit(90) || ps.TPHit(90.01) {
		t.Fatalf("short TPPct hit wrong")
	}
	// no TP configured never hits
	pn := NewProtection(SideLong, 100, 1, ProtectionConfig{StopMode: StopPct, StopValue: 0.05}, 0)
	if pn.TPHit(1e9) {
		t.Fatalf("TPNone should never hit")
	}
}
