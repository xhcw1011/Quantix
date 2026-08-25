package breakout

import (
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

type flatPV struct{}

func (flatPV) Cash() float64                            { return 10_000 }
func (flatPV) Position(string) (float64, float64, bool) { return 0, 0, false }
func (flatPV) Equity(map[string]float64) float64        { return 10_000 }

type captureBroker struct {
	reqs     []strategy.OrderRequest
	canceled []string
}

func (b *captureBroker) PlaceOrder(req strategy.OrderRequest) string {
	b.reqs = append(b.reqs, req)
	return "OID-1"
}
func (b *captureBroker) CancelOrder(id string) error { b.canceled = append(b.canceled, id); return nil }

func (b *captureBroker) byReason(reason string) []strategy.OrderRequest {
	var out []strategy.OrderRequest
	for _, r := range b.reqs {
		if r.Reason == reason {
			out = append(out, r)
		}
	}
	return out
}

// flatBars builds n bars that never breach any reasonable channel (small,
// tight oscillation around a constant), so the test can splice in a single
// deliberate breakout/breakdown bar without ambient noise triggering signals.
func flatBars(symbol string, n int, base float64) []exchange.Kline {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]exchange.Kline, n)
	for i := 0; i < n; i++ {
		bars[i] = exchange.Kline{
			Symbol: symbol, Interval: "15m",
			OpenTime: t0.Add(time.Duration(i) * 15 * time.Minute),
			Open:     base, High: base + 0.5, Low: base - 0.5, Close: base, IsClosed: true,
		}
	}
	return bars
}

func newCtx(pv strategy.PortfolioView, b strategy.Broker) *strategy.Context {
	return strategy.NewContext(pv, b, zap.NewNop())
}

func TestBreakout_EntryPeriodBreakoutOpensLong(t *testing.T) {
	strat := New(Config{Symbol: "BTCUSDT", EntryPeriod: 5, ExitPeriod: 20, EnableShort: true})
	broker := &captureBroker{}
	ctx := newCtx(flatPV{}, broker)

	bars := flatBars("BTCUSDT", 25, 100)
	for _, bar := range bars {
		strat.OnBar(ctx, bar)
	}
	if len(broker.reqs) != 0 {
		t.Fatalf("flat oscillation should never trigger an order, got %+v", broker.reqs)
	}

	// One more bar that closes clearly above the trailing 5-bar high (100.5).
	breakoutBar := exchange.Kline{Symbol: "BTCUSDT", Interval: "15m", Close: 105, High: 105, Low: 104, IsClosed: true}
	strat.OnBar(ctx, breakoutBar)

	entries := broker.byReason("breakout_entry")
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 entry order, got %d: %+v", len(entries), broker.reqs)
	}
	if entries[0].Side != strategy.SideBuy || entries[0].PositionSide != strategy.PositionSideLong {
		t.Fatalf("breakout above the high should open LONG, got %+v", entries[0])
	}
}

func TestBreakout_EntryPeriodBreakdownOpensShort(t *testing.T) {
	strat := New(Config{Symbol: "BTCUSDT", EntryPeriod: 5, ExitPeriod: 20, EnableShort: true})
	broker := &captureBroker{}
	ctx := newCtx(flatPV{}, broker)

	for _, bar := range flatBars("BTCUSDT", 25, 100) {
		strat.OnBar(ctx, bar)
	}
	breakdownBar := exchange.Kline{Symbol: "BTCUSDT", Interval: "15m", Close: 95, High: 96, Low: 95, IsClosed: true}
	strat.OnBar(ctx, breakdownBar)

	entries := broker.byReason("breakout_entry")
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 entry order, got %d: %+v", len(entries), broker.reqs)
	}
	if entries[0].Side != strategy.SideSell || entries[0].PositionSide != strategy.PositionSideShort {
		t.Fatalf("breakdown below the low should open SHORT, got %+v", entries[0])
	}
}

func TestBreakout_ShortDisabledWhenEnableShortFalse(t *testing.T) {
	strat := New(Config{Symbol: "BTCUSDT", EntryPeriod: 5, ExitPeriod: 20, EnableShort: false})
	broker := &captureBroker{}
	ctx := newCtx(flatPV{}, broker)

	for _, bar := range flatBars("BTCUSDT", 25, 100) {
		strat.OnBar(ctx, bar)
	}
	breakdownBar := exchange.Kline{Symbol: "BTCUSDT", Interval: "15m", Close: 95, High: 96, Low: 95, IsClosed: true}
	strat.OnBar(ctx, breakdownBar)

	if len(broker.byReason("breakout_entry")) != 0 {
		t.Fatalf("EnableShort=false must never open a short, got %+v", broker.reqs)
	}
}

func TestBreakout_WarmupBarsNeverTriggerOrders(t *testing.T) {
	strat := New(Config{Symbol: "BTCUSDT", EntryPeriod: 5, ExitPeriod: 20, EnableShort: true})
	broker := &captureBroker{}
	ctx := newCtx(flatPV{}, broker)

	bars := flatBars("BTCUSDT", 25, 100)
	breakoutBar := exchange.Kline{Symbol: "BTCUSDT", Interval: "15m", Close: 105, High: 105, Low: 104, IsClosed: true, Warmup: true}
	bars = append(bars, breakoutBar)
	for _, bar := range bars {
		strat.OnBar(ctx, bar)
	}
	if len(broker.reqs) != 0 {
		t.Fatalf("a warmup-replay breakout bar must never place a real order, got %+v", broker.reqs)
	}
}

func TestBreakout_NoImmediateReversal_OnlyWideExitCloses(t *testing.T) {
	// This is the load-bearing behaviour the backtest validated: while
	// positioned, an opposite EntryPeriod signal must be IGNORED — only the
	// much wider ExitPeriod channel can close the position.
	strat := New(Config{Symbol: "BTCUSDT", EntryPeriod: 5, ExitPeriod: 20, EnableShort: true})
	broker := &captureBroker{}
	ctx := newCtx(flatPV{}, broker)

	for _, bar := range flatBars("BTCUSDT", 25, 100) {
		strat.OnBar(ctx, bar)
	}
	strat.OnBar(ctx, exchange.Kline{Symbol: "BTCUSDT", Interval: "15m", Close: 105, High: 105, Low: 104, IsClosed: true})
	if len(broker.byReason("breakout_entry")) != 1 {
		t.Fatalf("setup: expected the long entry to have fired")
	}
	strat.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1, Price: 105})

	// A sharp pullback that breaks the tight 5-bar low (but NOT the wide
	// 20-bar low, which still sits back near 99.5) must NOT close/reverse.
	strat.OnBar(ctx, exchange.Kline{Symbol: "BTCUSDT", Interval: "15m", Close: 103, High: 104, Low: 103, IsClosed: true})
	if len(broker.byReason("breakout_exit")) != 0 {
		t.Fatalf("a pullback inside the wide exit channel must not close the position, got %+v", broker.reqs)
	}
	if !strat.hasLong {
		t.Fatalf("position should still be held through the ordinary pullback")
	}
}

func TestBreakout_WideExitChannelClosesLong(t *testing.T) {
	strat := New(Config{Symbol: "BTCUSDT", EntryPeriod: 5, ExitPeriod: 10, EnableShort: true})
	broker := &captureBroker{}
	ctx := newCtx(flatPV{}, broker)

	for _, bar := range flatBars("BTCUSDT", 15, 100) {
		strat.OnBar(ctx, bar)
	}
	strat.OnBar(ctx, exchange.Kline{Symbol: "BTCUSDT", Interval: "15m", Close: 105, High: 105, Low: 104, IsClosed: true})
	strat.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1, Price: 105})

	// Drive price down well below the trailing 10-bar low (~99.5) to trigger the wide exit.
	strat.OnBar(ctx, exchange.Kline{Symbol: "BTCUSDT", Interval: "15m", Close: 90, High: 91, Low: 90, IsClosed: true})

	exits := broker.byReason("breakout_exit")
	if len(exits) != 1 {
		t.Fatalf("expected exactly 1 exit order once the wide channel breaks, got %d: %+v", len(exits), broker.reqs)
	}
	if exits[0].Side != strategy.SideSell || exits[0].PositionSide != strategy.PositionSideLong {
		t.Fatalf("closing a long should be a SELL/LONG reduce, got %+v", exits[0])
	}
}

func TestBreakout_StopLossPctSetsProtectiveStopOnEntry(t *testing.T) {
	strat := New(Config{Symbol: "BTCUSDT", EntryPeriod: 5, ExitPeriod: 20, EnableShort: true, StopLossPct: 0.05})
	broker := &captureBroker{}
	ctx := newCtx(flatPV{}, broker)

	for _, bar := range flatBars("BTCUSDT", 25, 100) {
		strat.OnBar(ctx, bar)
	}
	strat.OnBar(ctx, exchange.Kline{Symbol: "BTCUSDT", Interval: "15m", Close: 105, High: 105, Low: 104, IsClosed: true})

	entries := broker.byReason("breakout_entry")
	if len(entries) != 1 {
		t.Fatalf("setup: expected 1 entry")
	}
	want := 105 * 0.95
	if entries[0].StopLoss != want {
		t.Fatalf("StopLoss = %v, want %v (5%% below entry)", entries[0].StopLoss, want)
	}
}

func TestBreakout_OnFillTracksPositionState(t *testing.T) {
	strat := New(Config{Symbol: "BTCUSDT", EntryPeriod: 5, ExitPeriod: 20, EnableShort: true})
	ctx := newCtx(flatPV{}, &captureBroker{})

	strat.OnFill(ctx, strategy.Fill{Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 2, Price: 100})
	if !strat.hasLong || strat.posQty != 2 {
		t.Fatalf("expected hasLong=true qty=2, got hasLong=%v qty=%v", strat.hasLong, strat.posQty)
	}
	strat.OnFill(ctx, strategy.Fill{Side: strategy.SideSell, PositionSide: strategy.PositionSideLong, Qty: 2, Price: 110})
	if strat.hasLong || strat.posQty != 0 {
		t.Fatalf("expected flat after the closing fill, got hasLong=%v qty=%v", strat.hasLong, strat.posQty)
	}
}

func TestBreakout_RegisteredInRegistry(t *testing.T) {
	if !registry.Exists("breakout") {
		t.Fatal("breakout must self-register in registry via init()")
	}
	s, err := registry.Create("breakout", map[string]any{"Symbol": "BTCUSDT"}, zap.NewNop())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.Name() == "" {
		t.Fatalf("expected a non-empty name")
	}
}

func TestParseConfig_RequiresSymbol(t *testing.T) {
	if _, err := registry.Create("breakout", map[string]any{}, zap.NewNop()); err == nil {
		t.Fatal("missing Symbol should error")
	}
}

func TestParseConfig_Defaults(t *testing.T) {
	s, err := registry.Create("breakout", map[string]any{"Symbol": "BTCUSDT"}, zap.NewNop())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	b := s.(*Breakout)
	if b.cfg.EntryPeriod != 20 || b.cfg.ExitPeriod != 96 {
		t.Fatalf("default periods wrong: entry=%d exit=%d, want 20/96", b.cfg.EntryPeriod, b.cfg.ExitPeriod)
	}
	if !b.cfg.EnableShort {
		t.Fatalf("default EnableShort should be true (matches the validated long+short backtest)")
	}
}
