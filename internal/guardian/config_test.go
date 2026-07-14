package guardian

import (
	"testing"

	"github.com/Quantix/quantix/internal/strategy/registry"
	"go.uber.org/zap"
)

func TestParseConfig_Defaults(t *testing.T) {
	gc, err := parseGuardianConfig(map[string]any{"Symbol": "ETHUSDT"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if gc.Symbol != "ETHUSDT" || !gc.Adopt {
		t.Fatalf("symbol/adopt defaults wrong: %+v", gc)
	}
	if gc.Prot.StopMode != StopPct || gc.Prot.StopValue != 0.03 {
		t.Fatalf("default stop should be plain 3%%, got %+v", gc.Prot)
	}
	if !gc.Prot.TrailEnabled || gc.Prot.ActivateR != 1 || gc.Prot.TrailMode != TrailR {
		t.Fatalf("default trail wrong: %+v", gc.Prot)
	}
	if gc.Prot.TPMode != TPNone {
		t.Fatalf("default TP should be off, got %v", gc.Prot.TPMode)
	}
	if gc.AlertMilestoneStep != 1 || gc.AlertStopProxR != 0.3 {
		t.Fatalf("default alerts should be on: %+v", gc)
	}
	if gc.ATRWindow != 14 {
		t.Fatalf("default ATRWindow = %d", gc.ATRWindow)
	}
}

func TestParseConfig_Explicit(t *testing.T) {
	gc, err := parseGuardianConfig(map[string]any{
		"Symbol": "ETHUSDT", "Adopt": false,
		"Side": "short", "Entry": 2400.0, "Qty": 2.0,
		"StopMode": "pct", "StopValue": 0.03,
		"TrailMode": "r", "TrailValue": 1.5,
		"TPMode": "r", "TPValue": 3.0,
		"ATRWindow": 20.0, "MAPeriod": 50.0,
		"AlertProfitMilestone": 1.0, "AlertLevels": []any{2300.0, 2500.0},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if gc.Adopt || gc.Side != SideShort || gc.Entry != 2400 || gc.Qty != 2 {
		t.Fatalf("explicit position wrong: %+v", gc)
	}
	if gc.Prot.StopMode != StopPct || gc.Prot.StopValue != 0.03 {
		t.Fatalf("stop parse wrong: %+v", gc.Prot)
	}
	if gc.Prot.TPMode != TPR || gc.Prot.TPValue != 3 {
		t.Fatalf("tp parse wrong: %+v", gc.Prot)
	}
	if gc.MAPeriod != 50 || gc.AlertMilestoneStep != 1 || len(gc.AlertLevels) != 2 {
		t.Fatalf("alert parse wrong: %+v", gc)
	}
}

func TestParseConfig_RequiresSymbol(t *testing.T) {
	if _, err := parseGuardianConfig(map[string]any{}); err == nil {
		t.Fatal("missing Symbol should error")
	}
}

func TestParseConfig_ExplicitNeedsSideEntryQty(t *testing.T) {
	if _, err := parseGuardianConfig(map[string]any{"Symbol": "ETHUSDT", "Adopt": false}); err == nil {
		t.Fatal("non-adopt without side/entry/qty should error")
	}
}

func TestFactory_BuildsGuardian(t *testing.T) {
	s, err := Factory(map[string]any{"Symbol": "ETHUSDT"}, zap.NewNop())
	if err != nil {
		t.Fatalf("factory err: %v", err)
	}
	if s.Name() != "guardian" {
		t.Fatalf("name = %q", s.Name())
	}
}

func TestGuardian_RegisteredInRegistry(t *testing.T) {
	if !registry.Exists("guardian") {
		t.Fatal("guardian must self-register in registry via init()")
	}
}
