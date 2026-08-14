package macross

import (
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// stubBroker records every order placed, without simulating a matching engine.
// Tests drive fills manually via m.OnFill so the exact fill price/qty is under
// the test's control (no dependency on backtest.Engine's bar-close fill timing).
type stubBroker struct {
	orders  []strategy.OrderRequest
	cancels []string
	// rejectNextID, when true, makes the next PlaceOrder call return "" (an
	// instant reject, e.g. a POST_ONLY limit that would have crossed the book)
	// instead of a real order ID. Consumed (reset to false) after firing once.
	rejectNextID bool
}

func (b *stubBroker) PlaceOrder(req strategy.OrderRequest) string {
	b.orders = append(b.orders, req)
	if b.rejectNextID {
		b.rejectNextID = false
		return ""
	}
	return "test-order"
}
func (b *stubBroker) CancelOrder(orderID string) error {
	b.cancels = append(b.cancels, orderID)
	return nil
}

type stubPortfolio struct{}

func (stubPortfolio) Cash() float64                                           { return 10_000 }
func (stubPortfolio) Position(symbol string) (qty, avgPrice float64, ok bool) { return 0, 0, false }
func (stubPortfolio) Equity(prices map[string]float64) float64                { return 10_000 }

func hedgeBar(symbol string, i int, close float64) exchange.Kline {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	return exchange.Kline{
		Symbol: symbol, Interval: "1h",
		OpenTime: t0.Add(time.Duration(i) * time.Hour), CloseTime: t0.Add(time.Duration(i+1) * time.Hour),
		Open: close, High: close, Low: close, Close: close, IsClosed: true,
	}
}

// feedBars drives m.OnBar for each close, and for any order the stub broker
// records during that bar, immediately synthesizes a same-price fill back into
// m.OnFill (a fixed-price, fees-free fill -- deterministic and sufficient since
// these tests assert on ORDER PLACEMENT decisions, not fill/PnL accounting).
func feedBars(m *MACross, ctx *strategy.Context, broker *stubBroker, symbol string, closes []float64) {
	for i, c := range closes {
		before := len(broker.orders)
		m.OnBar(ctx, hedgeBar(symbol, i, c))
		for _, req := range broker.orders[before:] {
			qty := req.Qty
			if qty == 0 {
				qty = 1 // "close all" sentinel -- exact qty doesn't matter for these tests
			}
			m.OnFill(ctx, strategy.Fill{
				Symbol: symbol, Side: req.Side, PositionSide: req.PositionSide,
				Qty: qty, Price: c, Timestamp: hedgeBar(symbol, i, c).CloseTime,
			})
		}
	}
}

// downtrendThenSharpRecovery: a death cross opens a SHORT around bar 7 (entry
// price 90), then price craters to 50 before snapping back sharply -- so by the
// time the SMAs golden-cross again (around bar 13), price has already recovered
// PAST the short's entry (90) and the short is meaningfully underwater, not
// profitable. This is the scenario that distinguishes "flip on every reverse
// cross" (old behavior) from "hold underwater positions, only flip when
// profitable" (AsymmetricExit).
var downtrendThenSharpRecovery = []float64{
	100, 101, 102, 103, 104, 105, // flat warmup
	100, 90, 80, 70, 60, 50, // sharp downtrend -- death cross opens SHORT @90
	95, 100, 105, 108, 110, 112, // sharp recovery PAST 90 -- golden cross while short is underwater
}

func newHedgeMACross(cfg Config) (*MACross, *strategy.Context, *stubBroker) {
	cfg.Symbol = "BTCUSDT"
	cfg.EnableShort = true
	if cfg.FastPeriod == 0 {
		cfg.FastPeriod = 3
	}
	if cfg.SlowPeriod == 0 {
		cfg.SlowPeriod = 6
	}
	log, _ := zap.NewDevelopment()
	broker := &stubBroker{}
	ctx := strategy.NewContext(stubPortfolio{}, broker, log)
	return New(cfg), ctx, broker
}

func TestAsymmetricExit_HoldsUnderwaterShortThroughReverseCross(t *testing.T) {
	m, ctx, broker := newHedgeMACross(Config{
		AsymmetricExit: true,
		StopLossPct:    0, // isolate reverse-cross behavior from the hard stop
		TrendFilterMin: 0, // isolate from the trend filter
	})

	feedBars(m, ctx, broker, "BTCUSDT", downtrendThenSharpRecovery)

	if !m.hasShort {
		t.Fatal("expected the underwater short to still be open (held, not flipped)")
	}
	for _, o := range broker.orders {
		if o.PositionSide == strategy.PositionSideShort && o.Side == strategy.SideBuy {
			t.Fatalf("short should not have been closed while underwater, got close order: %+v", o)
		}
	}
}

func TestAsymmetricExit_ClosesProfitableShortOnReverseCross(t *testing.T) {
	m, ctx, broker := newHedgeMACross(Config{
		AsymmetricExit: true,
		StopLossPct:    0,
		TrendFilterMin: 0,
	})

	// Death cross opens SHORT around bar 7 @90, then a GENTLE recovery that
	// stays below the entry price the whole time -- profitable short, and the
	// eventual golden cross should close it (take-profit-on-reversal), same as
	// the pre-AsymmetricExit behavior.
	prices := []float64{
		100, 101, 102, 103, 104, 105,
		100, 90, 80, 70, 60, 50,
		52, 55, 60, 65, 70, 78,
	}
	feedBars(m, ctx, broker, "BTCUSDT", prices)

	closed := false
	for _, o := range broker.orders {
		if o.PositionSide == strategy.PositionSideShort && o.Side == strategy.SideBuy {
			closed = true
		}
	}
	if !closed {
		t.Fatal("expected the profitable short to be closed on the reverse cross")
	}
}

func TestAsymmetricExit_ReducesOnConfirmedAdverseMove(t *testing.T) {
	// FastPeriod=3/SlowPeriod=10: a death cross opens SHORT @90 at bar 11, then
	// a small bump to 92/93 holds >=1% floating loss for 2 consecutive bars
	// (bars 16-17) WITHOUT yet causing a reverse cross (that only happens if a
	// 3rd elevated bar, 94, is fed -- deliberately left out so this test
	// isolates the reduce path from the cross-hold path).
	m, ctx, broker := newHedgeMACross(Config{
		FastPeriod: 3, SlowPeriod: 10,
		AsymmetricExit:    true,
		StopLossPct:       0, // isolate from the hard stop
		TrendFilterMin:    0,
		ReduceTriggerPct:  0.01,
		ReduceConfirmBars: 2,
		ReduceFrac:        0.5,
	})
	prices := []float64{
		100, 101, 102, 103, 104, 105, 106, 107, 108, 109,
		100, 90, 80, 70, 60, 50,
		92, 93,
	}
	feedBars(m, ctx, broker, "BTCUSDT", prices)

	var reduces []strategy.OrderRequest
	for _, o := range broker.orders {
		if o.Reason == "asymmetric_reduce" {
			reduces = append(reduces, o)
		}
	}
	if len(reduces) != 1 {
		t.Fatalf("expected exactly 1 reduce order, got %d: %+v", len(reduces), reduces)
	}
	r := reduces[0]
	if r.PositionSide != strategy.PositionSideShort || r.Side != strategy.SideBuy {
		t.Fatalf("reduce order should buy back part of the short, got %+v", r)
	}
	if r.Qty <= 0 || r.Qty >= 1 {
		t.Fatalf("reduce qty should be a PARTIAL fraction of the 1.0 position (expected ~0.5), got %v", r.Qty)
	}
	if !m.hasShort {
		t.Fatal("position should still be open (partially reduced, not fully closed)")
	}
}

func TestAsymmetricExit_TrailingTPClosesLargeWinnerOnGiveback(t *testing.T) {
	// Death cross opens SHORT @90 at bar 11 (FastPeriod=3/SlowPeriod=10, same
	// warmup as the reduce test). Price then falls hard to 40 (peak floating
	// profit ~55%, well past the 5% activation) before bouncing back up to 60 --
	// a 33% retrace of the peak, past the 25% giveback threshold -- which should
	// trigger a full trailing-TP close well before any reverse cross forms.
	m, ctx, broker := newHedgeMACross(Config{
		FastPeriod: 3, SlowPeriod: 10,
		AsymmetricExit:    true,
		StopLossPct:       0,
		TrendFilterMin:    0,
		TrailActivatePct:  0.05,
		TrailGivebackFrac: 0.25,
	})
	prices := []float64{
		100, 101, 102, 103, 104, 105, 106, 107, 108, 109,
		100, 90, 80, 70, 60, 50, 40, // peak floating profit here: (90-40)/90 = 55.6%
		60, // retrace to (90-60)/90 = 33.3% -- more than 25% given back from 55.6% peak
	}
	feedBars(m, ctx, broker, "BTCUSDT", prices)

	found := false
	for _, o := range broker.orders {
		if o.PositionSide == strategy.PositionSideShort && o.Side == strategy.SideBuy && o.Qty == 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a full close order (trailing TP) after the large winner retraced past the giveback threshold")
	}
}
