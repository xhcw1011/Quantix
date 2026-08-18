package macross

import (
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

func scaleOutBar(close float64) exchange.Kline {
	return exchange.Kline{
		Symbol: "BTCUSDT", Interval: "5m",
		Close: close, High: close + 0.1, Low: close - 0.1,
		IsClosed: true,
	}
}

// TestMACross_ScaleOut_FiresBothLevelsInSequence covers the core laddered
// profit-taking mechanism: two independent trigger%/frac% levels, each firing
// at most once, using the qty remaining at the moment each fires (same
// convention as reducePosition/ReduceFrac).
func TestMACross_ScaleOut_FiresBothLevelsInSequence(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		ScaleOut1TriggerPct: 0.005, ScaleOut1Frac: 0.3,
		ScaleOut2TriggerPct: 0.01, ScaleOut2Frac: 0.3,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)

	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	// Bar 1: +0.3% -- below both triggers.
	m.manageScaleOut(ctx, scaleOutBar(100.3))
	if len(broker.reqs) != 0 {
		t.Fatalf("expected no scale-out below trigger 1, got %+v", broker.reqs)
	}

	// Bar 2: +0.6% -- crosses level 1 (0.5%) only.
	m.manageScaleOut(ctx, scaleOutBar(100.6))
	if len(broker.reqs) != 1 {
		t.Fatalf("expected exactly 1 scale-out order after crossing level 1, got %d: %+v", len(broker.reqs), broker.reqs)
	}
	if broker.reqs[0].Reason != "scale_out_1" {
		t.Fatalf("expected reason scale_out_1, got %+v", broker.reqs[0])
	}
	if broker.reqs[0].Qty != 0.3 {
		t.Fatalf("expected qty 0.3 (30%% of posQty=1.0), got %v", broker.reqs[0].Qty)
	}
	if broker.reqs[0].Side != strategy.SideSell || broker.reqs[0].PositionSide != strategy.PositionSideLong {
		t.Fatalf("expected a SELL LONG reduce order, got %+v", broker.reqs[0])
	}

	// Bar 3: still +0.6% -- level 1 already fired, must not fire twice.
	m.manageScaleOut(ctx, scaleOutBar(100.6))
	if len(broker.reqs) != 1 {
		t.Fatalf("level 1 must not fire twice, got %d orders: %+v", len(broker.reqs), broker.reqs)
	}

	// Bar 4: +1.2% -- crosses level 2 (1.0%). posQty is still 1.0 in-memory
	// (fills are applied async via OnFill, which this test never re-invokes
	// for the level-1 order), so level 2's qty is also 1.0*0.3=0.3 -- this
	// matches manageAsymmetricExit's existing reduce/trail same-bar
	// convention (both read the same pre-fill state), not a bug.
	m.manageScaleOut(ctx, scaleOutBar(101.2))
	if len(broker.reqs) != 2 {
		t.Fatalf("expected level 2 to fire, got %d orders: %+v", len(broker.reqs), broker.reqs)
	}
	if broker.reqs[1].Reason != "scale_out_2" {
		t.Fatalf("expected reason scale_out_2, got %+v", broker.reqs[1])
	}
	if broker.reqs[1].Qty != 0.3 {
		t.Fatalf("expected qty 0.3, got %v", broker.reqs[1].Qty)
	}

	// Bar 5: well above both triggers -- neither fires again.
	m.manageScaleOut(ctx, scaleOutBar(105))
	if len(broker.reqs) != 2 {
		t.Fatalf("neither level should fire a third time, got %d orders: %+v", len(broker.reqs), broker.reqs)
	}
}

// TestMACross_ScaleOut_DisabledByDefault confirms this is strictly additive:
// an engine that never sets ScaleOut1TriggerPct/ScaleOut2TriggerPct (every
// existing config, including tonight's live 5m/15m macross engines) must see
// zero behavior change, no matter how far floating profit runs.
func TestMACross_ScaleOut_DisabledByDefault(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	m.manageScaleOut(ctx, scaleOutBar(110)) // +10% floating profit
	if len(broker.reqs) != 0 {
		t.Fatalf("scale-out must be disabled by default (TriggerPct=0 for both levels), got %+v", broker.reqs)
	}
}

// TestMACross_ScaleOut_OnlyLevel1Configured confirms a level with
// TriggerPct<=0 stays permanently disabled even while the other level fires
// normally -- the two levels are independent, not both-or-nothing.
func TestMACross_ScaleOut_OnlyLevel1Configured(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		ScaleOut1TriggerPct: 0.005, ScaleOut1Frac: 0.5,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	m.manageScaleOut(ctx, scaleOutBar(110)) // +10%, would clear any reasonable level-2 trigger too
	if len(broker.reqs) != 1 {
		t.Fatalf("expected exactly 1 order (level 1 only, level 2 unset), got %d: %+v", len(broker.reqs), broker.reqs)
	}
	if broker.reqs[0].Reason != "scale_out_1" {
		t.Fatalf("expected scale_out_1, got %+v", broker.reqs[0])
	}
}

// TestMACross_ScaleOut_ResetsOnNewLeg confirms scaleOut1Fired/scaleOut2Fired
// are per-leg, not permanent -- a fresh open (even on the opposite side) must
// re-arm both levels, mirroring resetPosState's existing per-leg fields
// (posReduced/posLossStreak/posPeakFP).
func TestMACross_ScaleOut_ResetsOnNewLeg(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		ScaleOut1TriggerPct: 0.005, ScaleOut1Frac: 0.5,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)

	// First leg: open, fire level 1, then fully close.
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})
	m.manageScaleOut(ctx, scaleOutBar(110))
	if len(broker.reqs) != 1 {
		t.Fatalf("expected level 1 to fire on the first leg, got %+v", broker.reqs)
	}
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideSell, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 110})
	broker.reqs = nil

	// Second leg: fresh open -- level 1 must be able to fire again.
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 200})
	m.manageScaleOut(ctx, scaleOutBar(202))
	if len(broker.reqs) != 1 {
		t.Fatalf("expected level 1 to re-fire on a fresh leg, got %d orders: %+v", len(broker.reqs), broker.reqs)
	}
}

// TestMACross_ScaleOut_WorksWithoutAsymmetricExit is the integration-level
// wiring test: onBarHedge must call the scale-out mechanism even when
// AsymmetricExit=false (the base config this was built for -- see the
// PF-1.01 30/90 finding tonight), unlike manageAsymmetricExit which stays
// gated behind AsymmetricExit.
func TestMACross_ScaleOut_WorksWithoutAsymmetricExit(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		AsymmetricExit:      false,
		ScaleOut1TriggerPct: 0.005, ScaleOut1Frac: 0.5,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	// Flat fast/slow arrays (no cross) so onBarHedge's switch never fires --
	// isolates this test to the scale-out wiring, not cross detection.
	flat := []float64{100, 100, 100}
	m.onBarHedge(ctx, scaleOutBar(110), flat, flat)

	if len(broker.reqs) != 1 || broker.reqs[0].Reason != "scale_out_1" {
		t.Fatalf("expected scale_out_1 to fire via onBarHedge with AsymmetricExit=false, got %+v", broker.reqs)
	}
}

// TestMACross_ScaleOut_IgnoresWarmupBars mirrors 74efa1e's !bar.Warmup guard
// for manageAsymmetricExit: a warmup-replayed bar must not be able to
// permanently consume a scale-out level for a leg it has nothing to do with.
func TestMACross_ScaleOut_IgnoresWarmupBars(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		ScaleOut1TriggerPct: 0.005, ScaleOut1Frac: 0.5,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	flat := []float64{100, 100, 100}
	warmupBar := scaleOutBar(110)
	warmupBar.Warmup = true
	m.onBarHedge(ctx, warmupBar, flat, flat)

	if len(broker.reqs) != 0 {
		t.Fatalf("a warmup bar must not trigger scale-out, got %+v", broker.reqs)
	}
	if m.scaleOut1Fired {
		t.Fatal("a warmup bar must not consume/arm scaleOut1Fired")
	}
}
