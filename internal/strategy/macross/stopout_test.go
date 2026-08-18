package macross

import (
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

func stopOutBar(close float64) exchange.Kline {
	return exchange.Kline{
		Symbol: "BTCUSDT", Interval: "5m",
		Close: close, High: close + 0.1, Low: close - 0.1,
		IsClosed: true,
	}
}

// TestMACross_StopOut_FiresBothLevelsInSequence covers the core laddered
// stop-loss mechanism: the loss-side mirror of ScaleOut. Two independent
// trigger%/frac% levels, each firing at most once on an ADVERSE move of that
// magnitude, using the qty remaining at the moment each fires (same
// convention as reducePosition/ReduceFrac/ScaleOut).
func TestMACross_StopOut_FiresBothLevelsInSequence(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		StopOut1TriggerPct: 0.005, StopOut1Frac: 0.3,
		StopOut2TriggerPct: 0.01, StopOut2Frac: 0.3,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)

	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	// Bar 1: -0.3% -- below both triggers (adverse move not big enough yet).
	m.manageStopOut(ctx, stopOutBar(99.7))
	if len(broker.reqs) != 0 {
		t.Fatalf("expected no stop-out below trigger 1, got %+v", broker.reqs)
	}

	// Bar 2: -0.6% -- crosses level 1 (0.5% adverse) only.
	m.manageStopOut(ctx, stopOutBar(99.4))
	if len(broker.reqs) != 1 {
		t.Fatalf("expected exactly 1 stop-out order after crossing level 1, got %d: %+v", len(broker.reqs), broker.reqs)
	}
	if broker.reqs[0].Reason != "stop_out_1" {
		t.Fatalf("expected reason stop_out_1, got %+v", broker.reqs[0])
	}
	if broker.reqs[0].Qty != 0.3 {
		t.Fatalf("expected qty 0.3 (30%% of posQty=1.0), got %v", broker.reqs[0].Qty)
	}
	if broker.reqs[0].Side != strategy.SideSell || broker.reqs[0].PositionSide != strategy.PositionSideLong {
		t.Fatalf("expected a SELL LONG reduce order, got %+v", broker.reqs[0])
	}

	// Bar 3: still -0.6% -- level 1 already fired, must not fire twice.
	m.manageStopOut(ctx, stopOutBar(99.4))
	if len(broker.reqs) != 1 {
		t.Fatalf("level 1 must not fire twice, got %d orders: %+v", len(broker.reqs), broker.reqs)
	}

	// Bar 4: -1.2% -- crosses level 2 (1.0% adverse). posQty is still 1.0
	// in-memory (fills are applied async via OnFill, which this test never
	// re-invokes for the level-1 order), so level 2's qty is also 1.0*0.3=0.3
	// -- matches ScaleOut's existing same-bar convention, not a bug.
	m.manageStopOut(ctx, stopOutBar(98.8))
	if len(broker.reqs) != 2 {
		t.Fatalf("expected level 2 to fire, got %d orders: %+v", len(broker.reqs), broker.reqs)
	}
	if broker.reqs[1].Reason != "stop_out_2" {
		t.Fatalf("expected reason stop_out_2, got %+v", broker.reqs[1])
	}
	if broker.reqs[1].Qty != 0.3 {
		t.Fatalf("expected qty 0.3, got %v", broker.reqs[1].Qty)
	}

	// Bar 5: well past both triggers -- neither fires again.
	m.manageStopOut(ctx, stopOutBar(90))
	if len(broker.reqs) != 2 {
		t.Fatalf("neither level should fire a third time, got %d orders: %+v", len(broker.reqs), broker.reqs)
	}
}

// TestMACross_StopOut_DoesNotFireWhileProfitable is the control: a position
// that stays profitable (or merely flat) the whole time must never trigger a
// stop-out level, no matter how long it's held.
func TestMACross_StopOut_DoesNotFireWhileProfitable(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		StopOut1TriggerPct: 0.005, StopOut1Frac: 0.3,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	m.manageStopOut(ctx, stopOutBar(100))  // flat
	m.manageStopOut(ctx, stopOutBar(105))  // +5%
	m.manageStopOut(ctx, stopOutBar(99.8)) // -0.2%, still below the 0.5% trigger
	if len(broker.reqs) != 0 {
		t.Fatalf("expected no stop-out while the adverse move never reaches the trigger, got %+v", broker.reqs)
	}
}

// TestMACross_StopOut_DisabledByDefault confirms this is strictly additive:
// an engine that never sets StopOut1TriggerPct/StopOut2TriggerPct (every
// existing config, including tonight's live 5m/15m macross engines) must see
// zero behavior change, no matter how far floating loss runs.
func TestMACross_StopOut_DisabledByDefault(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	m.manageStopOut(ctx, stopOutBar(80)) // -20% floating loss
	if len(broker.reqs) != 0 {
		t.Fatalf("stop-out must be disabled by default (TriggerPct=0 for both levels), got %+v", broker.reqs)
	}
}

// TestMACross_StopOut_OnlyLevel1Configured confirms a level with
// TriggerPct<=0 stays permanently disabled even while the other level fires
// normally -- the two levels are independent, not both-or-nothing.
func TestMACross_StopOut_OnlyLevel1Configured(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		StopOut1TriggerPct: 0.005, StopOut1Frac: 0.5,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	m.manageStopOut(ctx, stopOutBar(80)) // -20%, would clear any reasonable level-2 trigger too
	if len(broker.reqs) != 1 {
		t.Fatalf("expected exactly 1 order (level 1 only, level 2 unset), got %d: %+v", len(broker.reqs), broker.reqs)
	}
	if broker.reqs[0].Reason != "stop_out_1" {
		t.Fatalf("expected stop_out_1, got %+v", broker.reqs[0])
	}
}

// TestMACross_StopOut_ResetsOnNewLeg confirms stopOut1Fired/stopOut2Fired are
// per-leg, not permanent -- a fresh open (even on the opposite side) must
// re-arm both levels, mirroring resetPosState's existing per-leg fields.
func TestMACross_StopOut_ResetsOnNewLeg(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		StopOut1TriggerPct: 0.005, StopOut1Frac: 0.5,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)

	// First leg: open, fire level 1, then fully close.
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})
	m.manageStopOut(ctx, stopOutBar(90))
	if len(broker.reqs) != 1 {
		t.Fatalf("expected level 1 to fire on the first leg, got %+v", broker.reqs)
	}
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideSell, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 90})
	broker.reqs = nil

	// Second leg: fresh open -- level 1 must be able to fire again.
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 200})
	m.manageStopOut(ctx, stopOutBar(198))
	if len(broker.reqs) != 1 {
		t.Fatalf("expected level 1 to re-fire on a fresh leg, got %d orders: %+v", len(broker.reqs), broker.reqs)
	}
}

// TestMACross_StopOut_WorksWithoutAsymmetricExit is the integration-level
// wiring test: onBarHedge must call the stop-out mechanism even when
// AsymmetricExit=false (the base config this was built for -- see the
// PF-1.01 30/90 finding tonight; the existing ReduceTriggerPct mechanism
// stays gated behind AsymmetricExit=true and is untouched by this feature).
func TestMACross_StopOut_WorksWithoutAsymmetricExit(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		AsymmetricExit:     false,
		StopOut1TriggerPct: 0.005, StopOut1Frac: 0.5,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	// Flat fast/slow arrays (no cross) so onBarHedge's switch never fires --
	// isolates this test to the stop-out wiring, not cross detection.
	flat := []float64{100, 100, 100}
	m.onBarHedge(ctx, stopOutBar(90), flat, flat)

	if len(broker.reqs) != 1 || broker.reqs[0].Reason != "stop_out_1" {
		t.Fatalf("expected stop_out_1 to fire via onBarHedge with AsymmetricExit=false, got %+v", broker.reqs)
	}
}

// TestMACross_StopOut_IgnoresWarmupBars mirrors 74efa1e's !bar.Warmup guard:
// a warmup-replayed bar must not be able to permanently consume a stop-out
// level for a leg it has nothing to do with.
func TestMACross_StopOut_IgnoresWarmupBars(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		StopOut1TriggerPct: 0.005, StopOut1Frac: 0.5,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	flat := []float64{100, 100, 100}
	warmupBar := stopOutBar(90)
	warmupBar.Warmup = true
	m.onBarHedge(ctx, warmupBar, flat, flat)

	if len(broker.reqs) != 0 {
		t.Fatalf("a warmup bar must not trigger stop-out, got %+v", broker.reqs)
	}
	if m.stopOut1Fired {
		t.Fatal("a warmup bar must not consume/arm stopOut1Fired")
	}
}

// TestMACross_StopOut_ComposesWithScaleOut covers the case the user actually
// asked to test tonight: a leg that runs profitable enough to trigger a
// ScaleOut level, and LATER reverses enough to trigger a StopOut level on the
// SAME leg. Both must fire correctly, each operating on whatever qty remains
// at the moment it fires.
func TestMACross_StopOut_ComposesWithScaleOut(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		AsymmetricExit:      false,
		ScaleOut1TriggerPct: 0.02, ScaleOut1Frac: 0.4,
		StopOut1TriggerPct: 0.01, StopOut1Frac: 0.5,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	flat := []float64{100, 100, 100}

	// Runs up to +2% -- scale_out_1 fires (posQty still 1.0 in-memory, no
	// OnFill re-invoked for the partial, matching ScaleOut's own same-bar
	// convention), reducing 40% of the (in-memory) qty.
	m.onBarHedge(ctx, stopOutBar(102), flat, flat)
	if len(broker.reqs) != 1 || broker.reqs[0].Reason != "scale_out_1" {
		t.Fatalf("expected scale_out_1 to fire first, got %+v", broker.reqs)
	}

	// Then reverses to -1% floating (relative to the original entry) --
	// stop_out_1 fires too, independent of scale-out having already fired.
	m.onBarHedge(ctx, stopOutBar(99), flat, flat)
	if len(broker.reqs) != 2 || broker.reqs[1].Reason != "stop_out_1" {
		t.Fatalf("expected stop_out_1 to fire after reversing into a loss, got %+v", broker.reqs)
	}
}
