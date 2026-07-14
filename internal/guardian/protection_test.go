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
