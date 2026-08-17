package live

import (
	"context"
	"testing"
	"time"

	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
)

// TestSyncFillToPositionSyncer_ClosingFillClearsStaleGrossExposure reproduces
// a 2026-08-17 real-money incident: a golden cross closed a SHORT and, on the
// same bar, immediately tried to open a LONG (a "flip"). The exposure guard
// (internal/live/broker.go) reads gross exposure from the position syncer,
// not from the OMS's own fill-derived belief — and the syncer only otherwise
// updates via a SEPARATE, async exchange account-update WS message. That
// message hadn't arrived yet when the LONG open was attempted moments later,
// so the syncer still reported the just-closed SHORT's qty as open exposure,
// and the exposure guard incorrectly blocked a legitimate flip — leaving the
// account unexpectedly flat, which is what let the next cross reopen a fresh
// SHORT (the "closed a short then immediately opened another" symptom).
//
// The fix: sync OUR OWN fill-confirmed position into the syncer synchronously
// (in addition to the existing async exchange-truth path, which still
// protects against a desync we DIDN'T cause) so a same-bar flip's exposure
// check sees the real, current gross exposure instead of a stale snapshot.
func TestSyncFillToPositionSyncer_ClosingFillClearsStaleGrossExposure(t *testing.T) {
	e, syncer, pm := newDivergenceTestEngine(t)
	ctx := context.Background()

	// A real SHORT position exists, known to both the OMS and the syncer.
	pm.SeedPosition("BTCUSDT", "SHORT", 0.01, 62974.1)
	syncer.UpdatePosition(ctx, &position.StrategyPosition{
		ExchangePosition: position.ExchangePosition{Symbol: "BTCUSDT", Side: "SHORT", Qty: 0.01, EntryPrice: 62974.1},
		Filled:           true,
	})

	// The closing fill (BUY SHORT, fully covers it) — mirrors what
	// processFills does: apply to the OMS first, exactly as it already does.
	closeFill := strategy.Fill{
		Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideShort,
		Qty: 0.01, Price: 62900.1, Timestamp: time.Now(),
	}
	pm.ApplyFill(closeFill)

	e.syncFillToPositionSyncer(ctx, closeFill)

	if sp := syncer.GetShort(); sp != nil {
		t.Fatalf("expected the syncer's SHORT to clear synchronously once our own closing fill confirmed it, still got qty=%v", sp.Qty)
	}
}

// TestSyncFillToPositionSyncer_PartialCloseUpdatesRemainingQty confirms a
// partial reduce (not a full close) updates the syncer's qty rather than
// clearing the position entirely.
func TestSyncFillToPositionSyncer_PartialCloseUpdatesRemainingQty(t *testing.T) {
	e, syncer, pm := newDivergenceTestEngine(t)
	ctx := context.Background()

	pm.SeedPosition("BTCUSDT", "SHORT", 0.01, 62974.1)
	syncer.UpdatePosition(ctx, &position.StrategyPosition{
		ExchangePosition: position.ExchangePosition{Symbol: "BTCUSDT", Side: "SHORT", Qty: 0.01, EntryPrice: 62974.1},
		Filled:           true,
	})

	reduceFill := strategy.Fill{
		Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideShort,
		Qty: 0.004, Price: 62900.1, Timestamp: time.Now(),
	}
	pm.ApplyFill(reduceFill)

	e.syncFillToPositionSyncer(ctx, reduceFill)

	sp := syncer.GetShort()
	if sp == nil {
		t.Fatal("expected the SHORT to still exist after a partial reduce, got nil")
	}
	if sp.Qty < 0.0059 || sp.Qty > 0.0061 {
		t.Fatalf("expected remaining qty ~0.006, got %v", sp.Qty)
	}
}

// TestSyncFillToPositionSyncer_OpeningFillSeedsNewPosition confirms an
// opening fill (e.g. the LONG half of a flip) is reflected in the syncer too,
// not just a closing one.
func TestSyncFillToPositionSyncer_OpeningFillSeedsNewPosition(t *testing.T) {
	e, syncer, pm := newDivergenceTestEngine(t)
	ctx := context.Background()

	openFill := strategy.Fill{
		Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong,
		Qty: 0.004, Price: 62900.1, Timestamp: time.Now(),
	}
	pm.ApplyFill(openFill)

	e.syncFillToPositionSyncer(ctx, openFill)

	lp := syncer.GetLong()
	if lp == nil {
		t.Fatal("expected the syncer to reflect the newly opened LONG, got nil")
	}
	if lp.Qty != 0.004 || lp.EntryPrice != 62900.1 {
		t.Fatalf("expected qty=0.004 entry=62900.1, got qty=%v entry=%v", lp.Qty, lp.EntryPrice)
	}
}

// TestSyncFillToPositionSyncer_NilSyncerIsNoOp guards against a nil-pointer
// panic when no syncer is configured (e.g. paper mode).
func TestSyncFillToPositionSyncer_NilSyncerIsNoOp(t *testing.T) {
	e, _, pm := newDivergenceTestEngine(t)
	e.posSyncer = nil

	fill := strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 0.004, Price: 100}
	pm.ApplyFill(fill)

	e.syncFillToPositionSyncer(context.Background(), fill) // must not panic
}
