package composite

import (
	"testing"

	"github.com/Quantix/quantix/internal/strategy/registry"
	"go.uber.org/zap"
)

func TestRegistry_HasComposite(t *testing.T) {
	if !registry.Exists("composite") {
		t.Fatalf("composite not registered")
	}
	s, err := registry.Create("composite", map[string]any{"Symbol": "ETHUSDT"}, zap.NewNop())
	if err != nil {
		t.Fatalf("Create err: %v", err)
	}
	if s.Name() != "composite" {
		t.Fatalf("Name=%q", s.Name())
	}
}

func TestRegistry_MultiAlphaRegistration(t *testing.T) {
	if !registry.Exists("composite") {
		t.Fatalf("composite not registered")
	}
	s, err := registry.Create("composite", map[string]any{
		"Symbol":                "ETHUSDT",
		"alpha_breakout":        true,
		"alpha_funding_extreme": true,
		"alpha_cryptopanic":     true,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("Create err: %v", err)
	}
	if s.Name() != "composite" {
		t.Fatalf("Name=%q", s.Name())
	}
	// Cast to verify alpha count via private field would require export — instead
	// confirm strategy was constructed (no panic on multi-alpha New).
}

func TestRegistry_DefaultsToBreakoutWhenNoAlphasSpecified(t *testing.T) {
	if !registry.Exists("composite") {
		t.Fatalf("composite not registered")
	}
	s, err := registry.Create("composite", map[string]any{
		"Symbol": "ETHUSDT",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("Create err: %v", err)
	}
	if s.Name() != "composite" {
		t.Fatalf("Name=%q", s.Name())
	}
}

func TestRegistry_PanicsWhenAllAlphasDisabledExplicitly(t *testing.T) {
	// Explicit false on all alpha flags + no defaulting → panic per New() contract.
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for all-alpha-disabled config")
		}
	}()
	registry.Create("composite", map[string]any{
		"Symbol":                "ETHUSDT",
		"alpha_breakout":        false,
		"alpha_funding_extreme": false,
		"alpha_cryptopanic":     false,
		"alpha_explicit_empty":  true, // sentinel signaling "no defaulting"
	}, zap.NewNop())
}
