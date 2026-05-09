package composite

import (
	"testing"

	"github.com/Quantix/quantix/internal/alpha"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
)

func TestSetupReadsContextExtras(t *testing.T) {
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})

	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, &fakeBroker{}, zap.NewNop())
	ctx.Extra["user_id"] = 42
	ctx.Extra["engine_id"] = "test-engine"

	for _, b := range makeBars(70, 2300) {
		s.OnBar(ctx, b)
	}

	if s.userID != 42 {
		t.Fatalf("userID=%d want 42 (setup didn't read ctx.Extra)", s.userID)
	}
	if s.engineID != "test-engine" {
		t.Fatalf("engineID=%q want test-engine", s.engineID)
	}
}

func TestSetupRunsOnceOnly(t *testing.T) {
	// Subsequent ctx.Extra changes after first bar should NOT mutate s.userID/engineID.
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})

	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, &fakeBroker{}, zap.NewNop())
	ctx.Extra["user_id"] = 42
	ctx.Extra["engine_id"] = "first"

	bars := makeBars(70, 2300)
	s.OnBar(ctx, bars[0]) // first bar — setup runs

	// Mutate Extra; subsequent OnBar should NOT re-read
	ctx.Extra["engine_id"] = "second"
	for _, b := range bars[1:] {
		s.OnBar(ctx, b)
	}

	if s.engineID != "first" {
		t.Fatalf("setup re-ran: engineID=%q want first", s.engineID)
	}
}

func TestSetupBacktestEmptyExtraOK(t *testing.T) {
	// Backtest contexts have empty Extra. Setup should not panic; userID/engineID stay zero.
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})

	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, &fakeBroker{}, zap.NewNop())
	// ctx.Extra is empty by default

	for _, b := range makeBars(70, 2300) {
		s.OnBar(ctx, b)
	}

	if s.userID != 0 || s.engineID != "" {
		t.Fatalf("backtest setup leaked: userID=%d engineID=%q", s.userID, s.engineID)
	}
}
