package live

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/position"
)

// newDivergenceTestEngine builds a minimal *Engine wired with a real
// (in-memory, no Redis/DB) position.Syncer and oms.PositionManager -- enough
// to exercise Engine.positionsDiverge/checkPositionDivergence without the
// rest of the live-engine machinery.
func newDivergenceTestEngine(t *testing.T) (*Engine, *position.Syncer, *oms.PositionManager) {
	t.Helper()
	log := zap.NewNop()
	syncer := position.NewSyncer(position.SyncerConfig{Symbol: "BTCUSDT", Log: log})
	pm := oms.NewPositionManager()
	e := &Engine{
		cfg:       EngineConfig{Symbol: "BTCUSDT", StrategyID: "BTCUSDT-5m-guardian"},
		positions: pm,
		posSyncer: syncer,
		log:       log,
	}
	return e, syncer, pm
}

// TestEngine_PositionsDiverge_TrueWhenExchangeHasAPositionOMSDoesNot
// reproduces the 2026-08-06 finding: a fill that changed the real exchange
// position without ever being processed by this engine (e.g. one that landed
// while the process was fully down, so it never got a DB row for
// recoverFromDB/claimOrphanOrders to find) leaves the OMS's fill-derived
// belief stale while the position syncer (which syncs from the exchange
// directly) reflects the truth. This must be detectable generically, for any
// strategy -- not just guardian.
func TestEngine_PositionsDiverge_TrueWhenExchangeHasAPositionOMSDoesNot(t *testing.T) {
	e, syncer, _ := newDivergenceTestEngine(t)
	syncer.UpdatePosition(context.Background(), &position.StrategyPosition{
		ExchangePosition: position.ExchangePosition{Symbol: "BTCUSDT", Side: "LONG", Qty: 1},
	})
	if !e.positionsDiverge() {
		t.Fatal("must detect divergence when the exchange has a position OMS never learned about")
	}
}

func TestEngine_PositionsDiverge_FalseWhenInAgreement(t *testing.T) {
	e, syncer, pm := newDivergenceTestEngine(t)
	syncer.UpdatePosition(context.Background(), &position.StrategyPosition{
		ExchangePosition: position.ExchangePosition{Symbol: "BTCUSDT", Side: "LONG", Qty: 1},
	})
	pm.SeedPosition("BTCUSDT", "LONG", 1, 100)
	if e.positionsDiverge() {
		t.Fatal("must not report divergence when syncer and OMS agree")
	}
}

func TestEngine_PositionsDiverge_FalseWithoutSyncer(t *testing.T) {
	e, _, _ := newDivergenceTestEngine(t)
	e.posSyncer = nil
	if e.positionsDiverge() {
		t.Fatal("without a syncer there is nothing to compare against -- must not false-positive")
	}
}

// TestEngine_CheckPositionDivergence_LatchesAlertOnceUntilRecovered mirrors
// the existing staleAlerted convention (internal/live/engine_status.go): fire
// once when the condition starts, don't spam every tick, clear once resolved.
func TestEngine_CheckPositionDivergence_LatchesAlertOnceUntilRecovered(t *testing.T) {
	e, syncer, _ := newDivergenceTestEngine(t)
	syncer.UpdatePosition(context.Background(), &position.StrategyPosition{
		ExchangePosition: position.ExchangePosition{Symbol: "BTCUSDT", Side: "LONG", Qty: 1},
	})

	e.checkPositionDivergence()
	if !e.divergenceAlerted {
		t.Fatal("must latch the alert once divergence is detected")
	}

	syncer.RemovePosition(context.Background(), "LONG")
	e.checkPositionDivergence()
	if e.divergenceAlerted {
		t.Fatal("must clear the latch once the divergence resolves")
	}
}
