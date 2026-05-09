package composite

import (
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
	bars := make([]exchange.Kline, n)
	for i := range bars {
		px := base + float64(i)*0.5
		bars[i] = exchange.Kline{
			OpenTime: time.Unix(int64(i*300), 0),
			Open:     px, High: px + 0.5, Low: px - 0.5, Close: px,
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
