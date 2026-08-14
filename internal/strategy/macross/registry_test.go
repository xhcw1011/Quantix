package macross

import (
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy/registry"
)

func TestRegistryDefaults_AsymmetricExitOnTrendFilterOff(t *testing.T) {
	log, _ := zap.NewDevelopment()
	s, err := registry.Create("macross", map[string]any{
		"Symbol": "BTCUSDT", "EnableShort": true,
	}, log)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, ok := s.(*MACross)
	if !ok {
		t.Fatalf("expected *MACross, got %T", s)
	}
	if !m.cfg.AsymmetricExit {
		t.Error("expected AsymmetricExit to default to true")
	}
	if m.cfg.TrendFilterMin != 0 {
		t.Errorf("expected TrendFilterMin to default to 0 (off), got %v", m.cfg.TrendFilterMin)
	}
	if m.cfg.ReduceTriggerPct != 0.01 || m.cfg.ReduceConfirmBars != 2 || m.cfg.ReduceFrac != 0.5 {
		t.Errorf("unexpected reduce defaults: trigger=%v confirmBars=%v frac=%v",
			m.cfg.ReduceTriggerPct, m.cfg.ReduceConfirmBars, m.cfg.ReduceFrac)
	}
	if m.cfg.TrailActivatePct != 0.05 || m.cfg.TrailGivebackFrac != 0.35 {
		t.Errorf("unexpected trail defaults: activate=%v giveback=%v",
			m.cfg.TrailActivatePct, m.cfg.TrailGivebackFrac)
	}
}

func TestRegistryDefaults_ExplicitOverridesRespected(t *testing.T) {
	log, _ := zap.NewDevelopment()
	s, err := registry.Create("macross", map[string]any{
		"Symbol": "BTCUSDT", "EnableShort": true,
		"AsymmetricExit": false, "TrendFilterMin": 0.30,
	}, log)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := s.(*MACross)
	if m.cfg.AsymmetricExit {
		t.Error("explicit AsymmetricExit=false should be respected, not overridden by the default")
	}
	if m.cfg.TrendFilterMin != 0.30 {
		t.Errorf("explicit TrendFilterMin=0.30 should be respected, got %v", m.cfg.TrendFilterMin)
	}
}
