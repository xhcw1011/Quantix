package aistrat_v4

import (
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy/registry"
)

func TestRegistryRegistration(t *testing.T) {
	log := zap.NewNop()
	params := map[string]any{
		"Symbol": "ETHUSDT",
	}
	s, err := registry.Create("ai_v4", params, log)
	if err != nil {
		t.Fatalf("registry.Create returned error: %v", err)
	}
	if s == nil {
		t.Fatal("registry.Create returned nil strategy")
	}
	if name := s.Name(); name == "" {
		t.Fatalf("Name() returned empty")
	}
}

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.Lookback != 20 {
		t.Errorf("Lookback default = %d, want 20", c.Lookback)
	}
	if c.EntryZScore != 2.5 {
		t.Errorf("EntryZScore default = %f, want 2.5", c.EntryZScore)
	}
	if c.StopZScore != 3.5 {
		t.Errorf("StopZScore default = %f, want 3.5", c.StopZScore)
	}
	if c.TimeStopBars != 12 {
		t.Errorf("TimeStopBars default = %d, want 12", c.TimeStopBars)
	}
	if c.CooldownBars != 3 {
		t.Errorf("CooldownBars default = %d, want 3", c.CooldownBars)
	}
	if c.MinATRPct != 0.003 {
		t.Errorf("MinATRPct default = %f, want 0.003", c.MinATRPct)
	}
	if c.RiskPerTrade != 0.005 {
		t.Errorf("RiskPerTrade default = %f, want 0.005", c.RiskPerTrade)
	}
	if c.Leverage != 2 {
		t.Errorf("Leverage default = %f, want 2", c.Leverage)
	}
}
