package live

import (
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// retiringStrategy is a minimal strategy.Strategy that also implements
// strategy.Retired, for exercising Engine.checkRetired without building a
// full live engine.
type retiringStrategy struct{ retired bool }

func (r *retiringStrategy) Name() string                            { return "retiring" }
func (r *retiringStrategy) OnBar(*strategy.Context, exchange.Kline) {}
func (r *retiringStrategy) OnFill(*strategy.Context, strategy.Fill) {}
func (r *retiringStrategy) Retired() bool                           { return r.retired }

// plainStrategy implements only strategy.Strategy (no Retired), representing
// the majority of strategies that never self-retire.
type plainStrategy struct{}

func (plainStrategy) Name() string                            { return "plain" }
func (plainStrategy) OnBar(*strategy.Context, exchange.Kline) {}
func (plainStrategy) OnFill(*strategy.Context, strategy.Fill) {}

// TestEngine_CheckRetired verifies the 2026-08-06 fix: the engine learns a
// strategy has permanently retired (see strategy.Retired) so it can stop
// itself instead of polling/subscribing forever for a strategy that will
// never act again.
func TestEngine_CheckRetired(t *testing.T) {
	strat := &retiringStrategy{}
	e := &Engine{strategy: strat, log: zap.NewNop()}

	if e.checkRetired() {
		t.Fatal("must not report retired before the strategy says so")
	}
	strat.retired = true
	if !e.checkRetired() {
		t.Fatal("must report retired once strategy.Retired() returns true")
	}
}

// TestEngine_CheckRetired_IgnoresStrategiesWithoutRetired guards against a
// panic/false-positive for the common case: a strategy that doesn't
// implement strategy.Retired at all.
func TestEngine_CheckRetired_IgnoresStrategiesWithoutRetired(t *testing.T) {
	e := &Engine{strategy: plainStrategy{}, log: zap.NewNop()}
	if e.checkRetired() {
		t.Fatal("a strategy that doesn't implement strategy.Retired must never trip the check")
	}
}
