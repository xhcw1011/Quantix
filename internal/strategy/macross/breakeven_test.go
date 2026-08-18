package macross

import (
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
)

// TestMACross_BreakEven_ArmsAndFiresOnRoundTrip covers the core mechanism:
// once floating profit first reaches BreakEvenTriggerPct, the leg is armed;
// if profit later falls to BreakEvenBufferPct or below, the ENTIRE remaining
// position closes immediately -- a genuine winner can no longer round-trip
// all the way back into a real loss.
func TestMACross_BreakEven_ArmsAndFiresOnRoundTrip(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		BreakEvenTriggerPct: 0.01, BreakEvenBufferPct: 0,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	// Bar 1: +0.5% -- below trigger, not armed yet.
	m.manageBreakEven(ctx, scaleOutBar(100.5))
	if len(broker.reqs) != 0 {
		t.Fatalf("expected no close below trigger, got %+v", broker.reqs)
	}

	// Bar 2: +1.5% -- crosses trigger (1.0%), arms.
	m.manageBreakEven(ctx, scaleOutBar(101.5))
	if len(broker.reqs) != 0 {
		t.Fatalf("arming alone must not close the position, got %+v", broker.reqs)
	}

	// Bar 3: retraces to +0.3% -- still above the buffer (0), must not fire yet.
	m.manageBreakEven(ctx, scaleOutBar(100.3))
	if len(broker.reqs) != 0 {
		t.Fatalf("must not fire while still above the buffer, got %+v", broker.reqs)
	}

	// Bar 4: retraces to exactly breakeven (0%) -- fires a FULL close.
	m.manageBreakEven(ctx, scaleOutBar(100))
	if len(broker.reqs) != 1 {
		t.Fatalf("expected exactly 1 close order once profit fell to the buffer, got %d: %+v", len(broker.reqs), broker.reqs)
	}
	if broker.reqs[0].Reason != "breakeven_stop" {
		t.Fatalf("expected reason breakeven_stop, got %+v", broker.reqs[0])
	}
	if broker.reqs[0].Qty != 0 {
		t.Fatalf("expected a full close (qty=0 sentinel), got %+v", broker.reqs[0])
	}
	if broker.reqs[0].Side != strategy.SideSell || broker.reqs[0].PositionSide != strategy.PositionSideLong {
		t.Fatalf("expected a SELL LONG close order, got %+v", broker.reqs[0])
	}
}

// TestMACross_BreakEven_NeverArmedNeverFires confirms a leg whose floating
// profit never reaches BreakEvenTriggerPct is never touched by this
// mechanism, no matter how far it later falls (that's StopLossPct's job).
func TestMACross_BreakEven_NeverArmedNeverFires(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		BreakEvenTriggerPct: 0.01, BreakEvenBufferPct: 0,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	m.manageBreakEven(ctx, scaleOutBar(100.5)) // peaks at +0.5%, never reaches the 1% trigger
	m.manageBreakEven(ctx, scaleOutBar(98))    // now a real loss
	if len(broker.reqs) != 0 {
		t.Fatalf("a leg that never armed must never be closed by breakeven logic, got %+v", broker.reqs)
	}
}

// TestMACross_BreakEven_StaysProfitableNeverFires confirms armed-but-still-
// profitable legs are left alone -- this is a floor, not a profit cap.
func TestMACross_BreakEven_StaysProfitableNeverFires(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		BreakEvenTriggerPct: 0.01, BreakEvenBufferPct: 0,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	m.manageBreakEven(ctx, scaleOutBar(101.5)) // arms
	m.manageBreakEven(ctx, scaleOutBar(105))   // keeps running, well above buffer
	if len(broker.reqs) != 0 {
		t.Fatalf("an armed leg that stays well above the buffer must not be closed, got %+v", broker.reqs)
	}
}

// TestMACross_BreakEven_DisabledByDefault confirms this is strictly
// additive: an engine that never sets BreakEvenTriggerPct (every existing
// config, including tonight's live 5m/15m macross engines) sees zero
// behavior change no matter how a position's profit round-trips.
func TestMACross_BreakEven_DisabledByDefault(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	m.manageBreakEven(ctx, scaleOutBar(110)) // +10%
	m.manageBreakEven(ctx, scaleOutBar(90))  // round-trips to a real loss
	if len(broker.reqs) != 0 {
		t.Fatalf("breakeven stop must be disabled by default (TriggerPct=0), got %+v", broker.reqs)
	}
}

// TestMACross_BreakEven_ResetsOnNewLeg confirms breakEvenArmed is per-leg,
// mirroring resetPosState's existing per-leg fields.
func TestMACross_BreakEven_ResetsOnNewLeg(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		BreakEvenTriggerPct: 0.01, BreakEvenBufferPct: 0,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)

	// First leg: arm and fire.
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})
	m.manageBreakEven(ctx, scaleOutBar(101.5))
	m.manageBreakEven(ctx, scaleOutBar(100))
	if len(broker.reqs) != 1 {
		t.Fatalf("expected the first leg to fire once, got %+v", broker.reqs)
	}
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideSell, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})
	broker.reqs = nil

	// Second leg: fresh open -- must be able to arm and fire again.
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 200})
	m.manageBreakEven(ctx, scaleOutBar(203))
	m.manageBreakEven(ctx, scaleOutBar(200))
	if len(broker.reqs) != 1 {
		t.Fatalf("expected the second leg to arm and fire again, got %d orders: %+v", len(broker.reqs), broker.reqs)
	}
}

// TestMACross_BreakEven_WorksWithoutAsymmetricExit is the integration-level
// wiring test: onBarHedge must call this mechanism even when
// AsymmetricExit=false (the base config this was built for -- see the
// PF-1.01 30/90 finding tonight).
func TestMACross_BreakEven_WorksWithoutAsymmetricExit(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		AsymmetricExit:      false,
		BreakEvenTriggerPct: 0.01, BreakEvenBufferPct: 0,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	flat := []float64{100, 100, 100}
	m.onBarHedge(ctx, scaleOutBar(101.5), flat, flat) // arm
	m.onBarHedge(ctx, scaleOutBar(100), flat, flat)   // fire

	if len(broker.reqs) != 1 || broker.reqs[0].Reason != "breakeven_stop" {
		t.Fatalf("expected breakeven_stop to fire via onBarHedge with AsymmetricExit=false, got %+v", broker.reqs)
	}
}

// TestMACross_BreakEven_IgnoresWarmupBars mirrors 74efa1e's !bar.Warmup guard:
// a warmup-replayed bar must not be able to arm or fire this mechanism for a
// leg it has nothing to do with.
func TestMACross_BreakEven_IgnoresWarmupBars(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		BreakEvenTriggerPct: 0.01, BreakEvenBufferPct: 0,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	flat := []float64{100, 100, 100}
	warmupBar := scaleOutBar(101.5)
	warmupBar.Warmup = true
	m.onBarHedge(ctx, warmupBar, flat, flat)

	if len(broker.reqs) != 0 {
		t.Fatalf("a warmup bar must not trigger breakeven logic, got %+v", broker.reqs)
	}
	if m.breakEvenArmed {
		t.Fatal("a warmup bar must not arm breakEvenArmed")
	}
}
