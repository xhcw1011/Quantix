package composite

import (
	"testing"

	"github.com/Quantix/quantix/internal/alpha/baseline"
)

func TestStrategy_Name(t *testing.T) {
	s := New([]Alpha{baseline.NewBreakout()}, Config{Symbol: "ETHUSDT"})
	if s.Name() != "composite" {
		t.Fatalf("Name=%q want composite", s.Name())
	}
}

func TestStrategy_NeedsAtLeastOneAlpha(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic for empty alpha list")
		}
	}()
	_ = New(nil, Config{Symbol: "ETHUSDT"})
}
