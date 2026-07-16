package guardian

import "testing"

// TestProtection_UpdateConfig covers the live-edit stop rules.
func TestProtection_UpdateConfig(t *testing.T) {
	pctCfg := func(stop float64) ProtectionConfig {
		return ProtectionConfig{StopMode: StopPct, StopValue: stop, TrailEnabled: true, ActivateR: 1, TrailMode: TrailR, TrailValue: 1}
	}

	t.Run("tighten before activation moves stop up (long)", func(t *testing.T) {
		p := NewProtection(SideLong, 100, 1, pctCfg(0.03), 0) // stop 97, R 3
		p.UpdateConfig(pctCfg(0.02), 0)                       // → 2%: stop 98
		if p.Stop != 98 {
			t.Fatalf("stop = %v, want 98", p.Stop)
		}
		if p.R != 2 {
			t.Fatalf("R = %v, want 2", p.R)
		}
	})

	t.Run("widen before activation is allowed (long)", func(t *testing.T) {
		p := NewProtection(SideLong, 100, 1, pctCfg(0.03), 0) // stop 97
		p.UpdateConfig(pctCfg(0.05), 0)                       // → 5%: stop 95
		if p.Stop != 95 {
			t.Fatalf("stop = %v, want 95 (free to widen before trailing)", p.Stop)
		}
	})

	t.Run("once trailing, edit only tightens — never gives back profit (long)", func(t *testing.T) {
		p := NewProtection(SideLong, 100, 1, pctCfg(0.03), 0)
		p.UpdateStop(110, 0) // activate + trail: stop ratchets up toward 110-R
		trailed := p.Stop
		if trailed <= 97 {
			t.Fatalf("expected trailed stop above initial, got %v", trailed)
		}
		p.UpdateConfig(pctCfg(0.05), 0) // widen base to 95 — must NOT loosen the trailed stop
		if p.Stop != trailed {
			t.Fatalf("stop = %v, want %v (trailing edit must not loosen)", p.Stop, trailed)
		}
	})

	t.Run("take-profit updates immediately", func(t *testing.T) {
		cfg := pctCfg(0.03)
		p := NewProtection(SideLong, 100, 1, cfg, 0)
		cfg.TPMode = TPPct
		cfg.TPValue = 0.10
		p.UpdateConfig(cfg, 0)
		if p.TPPrice() != 110 {
			t.Fatalf("tp = %v, want 110", p.TPPrice())
		}
	})
}
