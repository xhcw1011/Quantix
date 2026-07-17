package guardian

import "testing"

func TestParseProtection_TrailActivate(t *testing.T) {
	// 5% activate on a 5% stop → 1R, trailing on.
	p := parseProtection(map[string]any{"StopValue": 0.05, "TrailActivatePct": 0.05})
	if !p.TrailEnabled || p.ActivateR != 1 {
		t.Fatalf("want trailing on at 1R, got enabled=%v activateR=%v", p.TrailEnabled, p.ActivateR)
	}
	// 0 → trailing off (fixed stop).
	p = parseProtection(map[string]any{"StopValue": 0.05, "TrailActivatePct": 0.0})
	if p.TrailEnabled {
		t.Fatalf("want trailing off at 0, got enabled")
	}
	// absent → default on (1R).
	p = parseProtection(map[string]any{"StopValue": 0.05})
	if !p.TrailEnabled || p.ActivateR != 1 {
		t.Fatalf("want default trailing on at 1R, got enabled=%v activateR=%v", p.TrailEnabled, p.ActivateR)
	}
}
