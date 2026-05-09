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
