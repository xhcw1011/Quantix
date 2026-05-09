package composite

import (
	"math"
	"testing"
	"time"

	"github.com/Quantix/quantix/internal/alpha"
	"github.com/Quantix/quantix/internal/alpha/baseline"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
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

// fakeAlpha lets tests inject deterministic Signal outputs.
type fakeAlpha struct {
	name string
	out  alpha.Signal
}

func (f *fakeAlpha) Name() string                          { return f.name }
func (f *fakeAlpha) Predict(_ alpha.Features) alpha.Signal { return f.out }

func makeBars(n int, base float64) []exchange.Kline {
	// Anchor bars so the LAST bar's CloseTime is ~now, ensuring the
	// live-mode gate opens by the final bar (existing tests pre-date the
	// gate and assume orders fire). Earlier bars are stale but get caught
	// up by warmup; gate flips open on the first within-2min CloseTime.
	bars := make([]exchange.Kline, n)
	end := time.Now()
	for i := range bars {
		px := base + float64(i)*0.5
		open := end.Add(time.Duration(-(n - i)) * 5 * time.Minute)
		bars[i] = exchange.Kline{
			OpenTime:  open,
			CloseTime: open.Add(5 * time.Minute),
			Open:      px, High: px + 0.5, Low: px - 0.5, Close: px,
			Volume: 1,
		}
	}
	return bars
}

func TestStrategy_PicksStrongestSignalAndPlacesOrder(t *testing.T) {
	weak := &fakeAlpha{name: "weak", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.4}}
	strong := &fakeAlpha{name: "strong", out: alpha.Signal{Direction: alpha.DirShort, Strength: 0.8}}
	s := New([]Alpha{weak, strong}, Config{Symbol: "ETHUSDT"})

	broker := &fakeBroker{}
	pv := &fakePortfolio{cash: 10000}
	ctx := strategy.NewContext(pv, broker, zap.NewNop())

	for _, b := range makeBars(70, 2300) {
		s.OnBar(ctx, b)
	}
	if len(broker.orders) == 0 {
		t.Fatalf("expected at least one order")
	}
	last := broker.orders[len(broker.orders)-1]
	if last.Side != strategy.SideSell {
		t.Fatalf("expected SELL (strong=-1) got %s", last.Side)
	}
}

func TestStrategy_HoldsBelowMinScore(t *testing.T) {
	a := &fakeAlpha{name: "weak", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.1}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT", MinSignalScore: 0.3})

	broker := &fakeBroker{}
	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, broker, zap.NewNop())

	for _, b := range makeBars(70, 2300) {
		s.OnBar(ctx, b)
	}
	if len(broker.orders) != 0 {
		t.Fatalf("expected no orders, got %d", len(broker.orders))
	}
}

func TestStrategy_WaitsForWarmup(t *testing.T) {
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT", WarmupBars: 50})

	broker := &fakeBroker{}
	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, broker, zap.NewNop())

	for _, b := range makeBars(40, 2300) {
		s.OnBar(ctx, b)
	}
	if len(broker.orders) != 0 {
		t.Fatalf("orders placed before warmup: %d", len(broker.orders))
	}
}

// Smoke test that real Breakout alpha integrates with composite. We feed a
// rising-then-spike fixture so the final bar's close clears the 10-bar high
// (exclusive of current bar) by a wide ATR-scaled margin — this is what the
// Breakout alpha needs to fire a long signal once warmup completes.
func TestStrategy_IntegratesWithBreakoutAlpha(t *testing.T) {
	s := New([]Alpha{baseline.NewBreakout()}, Config{
		Symbol:         "ETHUSDT",
		MinSignalScore: 0.0,
	})
	broker := &fakeBroker{}
	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, broker, zap.NewNop())

	// 79 baseline bars (ranging ±0.5 around 2300) build ATR + Donchian, then
	// one upside-spike bar that clears the 10-bar high decisively.
	bars := makeBars(79, 2300)
	last := bars[len(bars)-1]
	bars = append(bars, exchange.Kline{
		OpenTime: last.OpenTime.Add(5 * time.Minute),
		Open:     last.Close,
		High:     last.Close + 30,
		Low:      last.Close,
		Close:    last.Close + 25, // well above any High10 from the +0.5/bar series
		Volume:   1,
	})
	for _, b := range bars {
		s.OnBar(ctx, b)
	}
	if len(broker.orders) == 0 {
		t.Fatalf("expected at least one order from Breakout on spike bar")
	}
	// First order on upside-spike should be a buy (long breakout).
	if broker.orders[0].Side != strategy.SideBuy {
		t.Fatalf("expected first order Side=BUY, got %s", broker.orders[0].Side)
	}
	// StopLoss should be populated (Fix 2)
	if broker.orders[0].StopLoss == 0 {
		t.Fatalf("expected StopLoss to be populated on order")
	}
	// For long, SL must be below entry (entry approximated by bar close).
	// market orders set Price=0; guard avoids false positives.
	if broker.orders[0].StopLoss >= broker.orders[0].Price && broker.orders[0].Price != 0 {
		t.Fatalf("long order StopLoss=%f should be below entry=%f", broker.orders[0].StopLoss, broker.orders[0].Price)
	}
}

// Pin the position-alignment gate even though Task 7 hasn't wired posQty
// updates yet. Setting posQty directly via the unexported field is OK in
// internal-package tests; this freezes the contract Task 7 must preserve.
func TestStrategy_SkipsWhenAlreadyAlignedLong(t *testing.T) {
	a := &fakeAlpha{name: "long", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})
	s.posQty = 1.0 // simulate existing long position
	broker := &fakeBroker{}
	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, broker, zap.NewNop())
	for _, b := range makeBars(70, 2300) {
		s.OnBar(ctx, b)
	}
	if len(broker.orders) != 0 {
		t.Fatalf("expected no orders when already aligned long, got %d", len(broker.orders))
	}
}

func TestStrategy_SkipsWhenAlreadyAlignedShort(t *testing.T) {
	a := &fakeAlpha{name: "short", out: alpha.Signal{Direction: alpha.DirShort, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})
	s.posQty = -1.0 // simulate existing short position
	broker := &fakeBroker{}
	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, broker, zap.NewNop())
	for _, b := range makeBars(70, 2300) {
		s.OnBar(ctx, b)
	}
	if len(broker.orders) != 0 {
		t.Fatalf("expected no orders when already aligned short, got %d", len(broker.orders))
	}
}

func TestStrategy_PositionTrackedAfterFill(t *testing.T) {
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})
	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, &fakeBroker{}, zap.NewNop())

	s.OnFill(ctx, strategy.Fill{
		Symbol: "ETHUSDT", Side: strategy.SideBuy, Qty: 0.5, Price: 2300,
	})
	if s.posQty != 0.5 {
		t.Fatalf("posQty=%f want 0.5", s.posQty)
	}

	s.OnFill(ctx, strategy.Fill{
		Symbol: "ETHUSDT", Side: strategy.SideSell, Qty: 0.5, Price: 2310,
	})
	if s.posQty != 0 {
		t.Fatalf("posQty=%f want 0 after closing", s.posQty)
	}
}

func TestStrategy_OnFillIgnoresOtherSymbols(t *testing.T) {
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})
	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, &fakeBroker{}, zap.NewNop())

	s.OnFill(ctx, strategy.Fill{
		Symbol: "BTCUSDT", Side: strategy.SideBuy, Qty: 0.5, Price: 60000,
	})
	if s.posQty != 0 {
		t.Fatalf("BTCUSDT fill should not affect ETHUSDT posQty, got %f", s.posQty)
	}
}

func TestStrategy_SkipsStaleBackfillBars(t *testing.T) {
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT", WarmupBars: 5})

	broker := &fakeBroker{}
	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, broker, zap.NewNop())

	// Build 60 bars where ALL CloseTime are >2 min in past (stale backfill).
	// Even with WarmupBars satisfied, no orders should fire.
	// Anchor far enough back that 60×5min stays entirely in the past.
	bars := make([]exchange.Kline, 60)
	staleAnchor := time.Now().Add(-10 * time.Hour)
	for i := range bars {
		px := 2300.0 + float64(i)*0.5
		bars[i] = exchange.Kline{
			OpenTime:  staleAnchor.Add(time.Duration(i) * 5 * time.Minute),
			CloseTime: staleAnchor.Add(time.Duration(i+1) * 5 * time.Minute),
			Open:      px, High: px + 0.5, Low: px - 0.5, Close: px,
			Volume:    1,
		}
	}
	for _, b := range bars {
		s.OnBar(ctx, b)
	}
	if len(broker.orders) != 0 {
		t.Fatalf("expected 0 orders during backfill replay, got %d", len(broker.orders))
	}
}

func TestStrategy_TradesAfterFirstLiveBar(t *testing.T) {
	// 60 stale backfill bars (no orders), then 1 live bar (close within 2min) → orders allowed.
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT", WarmupBars: 5})

	broker := &fakeBroker{}
	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, broker, zap.NewNop())

	// Anchor far enough back that 60×5min stays entirely in the past.
	staleAnchor := time.Now().Add(-10 * time.Hour)
	for i := 0; i < 60; i++ {
		px := 2300.0 + float64(i)*0.5
		s.OnBar(ctx, exchange.Kline{
			OpenTime:  staleAnchor.Add(time.Duration(i) * 5 * time.Minute),
			CloseTime: staleAnchor.Add(time.Duration(i+1) * 5 * time.Minute),
			Open:      px, High: px + 0.5, Low: px - 0.5, Close: px,
			Volume:    1,
		})
	}
	if len(broker.orders) != 0 {
		t.Fatalf("backfill placed orders: %d", len(broker.orders))
	}

	// Live bar — CloseTime ~now
	live := exchange.Kline{
		OpenTime:  time.Now().Add(-5 * time.Minute),
		CloseTime: time.Now().Add(-30 * time.Second),
		Open:      2330, High: 2331, Low: 2329, Close: 2330.5,
		Volume:    1,
	}
	s.OnBar(ctx, live)
	if len(broker.orders) == 0 {
		t.Fatalf("expected order on first live bar, got 0")
	}
}

func TestStrategy_BacktestReplayBypassesLiveGate(t *testing.T) {
	// In backtest mode, set ctx.Extra["backtest_replay"]=true; stale bars trade.
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT", WarmupBars: 5})

	broker := &fakeBroker{}
	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, broker, zap.NewNop())
	ctx.Extra["backtest_replay"] = true

	staleAnchor := time.Now().Add(-30 * 24 * time.Hour) // ancient bars
	for i := 0; i < 60; i++ {
		px := 2300.0 + float64(i)*0.5
		s.OnBar(ctx, exchange.Kline{
			OpenTime:  staleAnchor.Add(time.Duration(i) * 5 * time.Minute),
			CloseTime: staleAnchor.Add(time.Duration(i+1) * 5 * time.Minute),
			Open:      px, High: px + 0.5, Low: px - 0.5, Close: px,
			Volume:    1,
		})
	}
	if len(broker.orders) == 0 {
		t.Fatalf("backtest_replay=true should bypass live-gate, got 0 orders")
	}
}

func TestStrategy_StopLossRoundedToTickSize(t *testing.T) {
	// SL price must be rounded to 0.01 (ETHUSDT tick) — Binance rejects -1111 otherwise.
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT", WarmupBars: 5, SLATR: 1.5})

	broker := &fakeBroker{}
	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, broker, zap.NewNop())

	// Build bars with ATR producing fractional SL distance. Use live timestamps.
	now := time.Now()
	for i := 0; i < 80; i++ {
		// Sin wave to give ATR > 0
		px := 2300.0 + float64(i)*0.5
		s.OnBar(ctx, exchange.Kline{
			OpenTime:  now.Add(time.Duration(-80+i) * 5 * time.Minute),
			CloseTime: now.Add(time.Duration(-80+i)*5*time.Minute + 5*time.Minute),
			Open:      px, High: px + 0.7, Low: px - 0.6, Close: px + 0.3,
			Volume:    1,
		})
	}

	if len(broker.orders) == 0 {
		t.Fatalf("expected at least 1 order")
	}
	for i, o := range broker.orders {
		if o.StopLoss == 0 {
			continue // some orders may have 0 SL if direction was 0; skip
		}
		// Multiply by 100, must equal an integer (no sub-cent precision).
		x := o.StopLoss * 100
		if math.Abs(x-math.Round(x)) > 1e-6 {
			t.Fatalf("order[%d] StopLoss=%f not tick-rounded (×100=%f, residual=%f)",
				i, o.StopLoss, x, math.Abs(x-math.Round(x)))
		}
	}
}
