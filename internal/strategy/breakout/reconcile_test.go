package breakout

import (
	"testing"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/position"
)

// fakeSyncer satisfies the positionReporter interface Breakout reads from
// ctx.Extra["position_syncer"] — mirrors macross's identically-named fixture.
type fakeSyncer struct {
	long, short          bool
	longEntry, longQty   float64
	shortEntry, shortQty float64
}

func (f fakeSyncer) HasPosition(side string) bool {
	if side == "LONG" {
		return f.long
	}
	return f.short
}

func (f fakeSyncer) GetLong() *position.StrategyPosition {
	if !f.long {
		return nil
	}
	p := &position.StrategyPosition{}
	p.EntryPrice = f.longEntry
	p.Qty = f.longQty
	return p
}

func (f fakeSyncer) GetShort() *position.StrategyPosition {
	if !f.short {
		return nil
	}
	p := &position.StrategyPosition{}
	p.EntryPrice = f.shortEntry
	p.Qty = f.shortQty
	return p
}

// TestBreakout_RestartAdoptsInsteadOfReopening reproduces the restart-reopen
// bug class this package's reconcilePosition guards against (same as
// macross's TestMACross_HedgeSkipsPrimingWhenSyncerHasPosition): a fresh
// Breakout after a restart starts with hasLong/hasShort at their zero value,
// so a warmup replay ending on a bar that would otherwise look like flat
// must NOT open a duplicate position once the syncer reports one already
// exists.
func TestBreakout_RestartAdoptsInsteadOfReopening(t *testing.T) {
	strat := New(Config{Symbol: "BTCUSDT", EntryPeriod: 5, ExitPeriod: 20, EnableShort: true})
	broker := &captureBroker{}
	ctx := newCtx(flatPV{}, broker)
	ctx.Extra["position_syncer"] = fakeSyncer{short: true, shortEntry: 100, shortQty: 2}

	// Warmup replay (suppressed order execution in the real engine — here we
	// just confirm the strategy itself places nothing) then one live bar
	// that would otherwise read as a breakdown-from-flat signal.
	bars := flatBars("BTCUSDT", 25, 100)
	for i := range bars {
		bars[i].Warmup = true
		strat.OnBar(ctx, bars[i])
	}
	broker.reqs = nil
	strat.OnBar(ctx, exchange.Kline{Symbol: "BTCUSDT", Interval: "15m", Close: 95, High: 96, Low: 95, IsClosed: true})

	if len(broker.reqs) != 0 {
		t.Fatalf("must NOT re-open when the syncer already reports a SHORT, got %+v", broker.reqs)
	}
	if !strat.hasShort || strat.posQty != 2 {
		t.Fatalf("expected hasShort=true qty=2 seeded from the syncer, got hasShort=%v qty=%v", strat.hasShort, strat.posQty)
	}
}

// TestBreakout_ReconciledPositionStillExitsOnWideChannel confirms the
// adopted (restart-seeded) position is still governed by the normal exit
// channel afterwards — reconciliation isn't a one-way trapdoor that leaves
// the position unmanaged forever.
func TestBreakout_ReconciledPositionStillExitsOnWideChannel(t *testing.T) {
	strat := New(Config{Symbol: "BTCUSDT", EntryPeriod: 5, ExitPeriod: 10, EnableShort: true})
	broker := &captureBroker{}
	ctx := newCtx(flatPV{}, broker)
	ctx.Extra["position_syncer"] = fakeSyncer{long: true, longEntry: 100, longQty: 3}

	bars := flatBars("BTCUSDT", 15, 100)
	for i := range bars {
		bars[i].Warmup = true
		strat.OnBar(ctx, bars[i])
	}
	broker.reqs = nil

	// Live bar that breaks the wide 10-bar low decisively.
	strat.OnBar(ctx, exchange.Kline{Symbol: "BTCUSDT", Interval: "15m", Close: 90, High: 91, Low: 90, IsClosed: true})

	exits := broker.byReason("breakout_exit")
	if len(exits) != 1 {
		t.Fatalf("expected the adopted position to exit on the wide channel break, got %d: %+v", len(exits), broker.reqs)
	}
	if exits[0].Qty != 3 {
		t.Fatalf("exit qty should match the reconciled position size (3), got %v", exits[0].Qty)
	}
}

// TestBreakout_GenuinelyFlatStillOpensNormally guards the intended behaviour:
// with no existing position (no syncer / empty syncer), a genuine breakout
// still opens — reconciliation must not accidentally suppress real signals.
func TestBreakout_GenuinelyFlatStillOpensNormally(t *testing.T) {
	for _, tc := range []struct {
		name   string
		syncer any
	}{
		{"no syncer (e.g. backtest)", nil},
		{"empty syncer", fakeSyncer{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			strat := New(Config{Symbol: "BTCUSDT", EntryPeriod: 5, ExitPeriod: 20, EnableShort: true})
			broker := &captureBroker{}
			ctx := newCtx(flatPV{}, broker)
			if tc.syncer != nil {
				ctx.Extra["position_syncer"] = tc.syncer
			}
			for _, bar := range flatBars("BTCUSDT", 25, 100) {
				strat.OnBar(ctx, bar)
			}
			strat.OnBar(ctx, exchange.Kline{Symbol: "BTCUSDT", Interval: "15m", Close: 105, High: 105, Low: 104, IsClosed: true})

			if len(broker.byReason("breakout_entry")) != 1 {
				t.Fatalf("expected exactly 1 entry order when genuinely flat, got %d: %+v", len(broker.byReason("breakout_entry")), broker.reqs)
			}
		})
	}
}
