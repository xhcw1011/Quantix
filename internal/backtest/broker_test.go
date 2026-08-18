package backtest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

func newTestPortfolio(capital float64) *Portfolio { return NewPortfolio(capital) }
func devLog() *zap.Logger                         { l, _ := zap.NewDevelopment(); return l }

func makeBar(sym string, close float64, t time.Time) exchange.Kline {
	return exchange.Kline{
		Symbol:    sym,
		Interval:  "1h",
		OpenTime:  t,
		CloseTime: t.Add(time.Hour),
		Open:      close,
		High:      close * 1.01,
		Low:       close * 0.99,
		Close:     close,
		Volume:    100,
		IsClosed:  true,
	}
}

func TestBuy_AllIn(t *testing.T) {
	p := newTestPortfolio(10_000)
	b := NewSimBroker(0.001, 0, p, devLog())

	b.PlaceOrder(strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy})
	bar := makeBar("BTCUSDT", 50_000, time.Now())
	fills := b.Process(bar)

	require.Len(t, fills, 1)
	fill := fills[0]
	assert.Equal(t, strategy.SideBuy, fill.Side)
	assert.Greater(t, fill.Qty, 0.0)
	total := fill.Qty*fill.Price + fill.Fee
	// Broker uses 99% of cash, so total cost ≤ initial capital
	assert.LessOrEqual(t, total, 10_000.0)
	// And at least 98% should be deployed
	assert.Greater(t, total, 9_800.0)
}

func TestSell_ClosesPosition(t *testing.T) {
	p := newTestPortfolio(10_000)
	b := NewSimBroker(0.001, 0, p, devLog())
	t0 := time.Now()

	// Buy
	b.PlaceOrder(strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy})
	b.Process(makeBar("BTCUSDT", 50_000, t0))

	// Sell
	b.PlaceOrder(strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideSell})
	fills := b.Process(makeBar("BTCUSDT", 55_000, t0.Add(time.Hour)))

	require.Len(t, fills, 1)
	assert.Equal(t, strategy.SideSell, fills[0].Side)
	require.Len(t, p.Trades, 1)
	assert.Greater(t, p.Trades[0].NetPnL, 0.0, "trade should be profitable")
}

func TestSell_WithoutPosition_Rejected(t *testing.T) {
	p := newTestPortfolio(10_000)
	b := NewSimBroker(0.001, 0, p, devLog())

	b.PlaceOrder(strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideSell})
	fills := b.Process(makeBar("BTCUSDT", 50_000, time.Now()))
	assert.Empty(t, fills, "sell without position should be rejected")
}

func TestSlippage(t *testing.T) {
	p := newTestPortfolio(10_000)
	b := NewSimBroker(0, 0.01, p, devLog()) // 1% slippage, no fee

	b.PlaceOrder(strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy})
	fills := b.Process(makeBar("BTCUSDT", 50_000, time.Now()))

	require.Len(t, fills, 1)
	// Buy price should be 1% above close
	assert.InDelta(t, 50_500, fills[0].Price, 1.0)
}

func TestRoundTrip_PnL(t *testing.T) {
	p := newTestPortfolio(10_000)
	b := NewSimBroker(0, 0, p, devLog()) // no fees for clean math
	t0 := time.Now()

	b.PlaceOrder(strategy.OrderRequest{Symbol: "X", Side: strategy.SideBuy})
	b.Process(makeBar("X", 100, t0))

	b.PlaceOrder(strategy.OrderRequest{Symbol: "X", Side: strategy.SideSell})
	b.Process(makeBar("X", 110, t0.Add(time.Hour)))

	require.Len(t, p.Trades, 1)
	trade := p.Trades[0]
	assert.InDelta(t, 10.0, trade.PnLPct, 0.01, "10% gain expected")
	assert.Greater(t, p.Cash(), 10_000.0, "cash should grow after profitable trade")
}

func TestSimBroker_PlaceOrder_ReturnsID(t *testing.T) {
	p := newTestPortfolio(10_000)
	b := NewSimBroker(0.001, 0, p, devLog())

	id := b.PlaceOrder(strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy})
	assert.NotEmpty(t, id, "PlaceOrder should return a non-empty order ID")
}

func TestSimBroker_CancelOrder_RemovesFromQueue(t *testing.T) {
	p := newTestPortfolio(10_000)
	b := NewSimBroker(0.001, 0, p, devLog())

	id := b.PlaceOrder(strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy})
	require.NotEmpty(t, id)

	err := b.CancelOrder(id)
	require.NoError(t, err, "CancelOrder should succeed for a submitted order")

	fills := b.Process(makeBar("BTCUSDT", 50_000, time.Now()))
	assert.Empty(t, fills, "cancelled order should produce no fill")
}

func TestSimBroker_CancelOrder_NotFound(t *testing.T) {
	p := newTestPortfolio(10_000)
	b := NewSimBroker(0.001, 0, p, devLog())

	err := b.CancelOrder("nonexistent-id")
	assert.Error(t, err, "CancelOrder on unknown ID should return an error")
}

func TestSimBroker_StopLoss_LongHit(t *testing.T) {
	pf := newTestPortfolio(10000)
	b := NewSimBroker(0.001, 0.0005, pf, devLog())

	// Open long with SL at 95
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETH", Side: strategy.SideBuy, Qty: 1.0, StopLoss: 95.0,
	})
	// Bar 1: close at 100, no stop hit (Low > 95)
	bar1 := exchange.Kline{Symbol: "ETH", High: 101, Low: 99, Close: 100, CloseTime: time.Unix(0, 0)}
	fills1 := b.Process(bar1)
	if len(fills1) != 1 || fills1[0].Side != strategy.SideBuy {
		t.Fatalf("expected one BUY entry fill, got %+v", fills1)
	}

	// Bar 2: Low dips to 94 (crosses stop) — should emit a SELL close fill at 95
	bar2 := exchange.Kline{Symbol: "ETH", High: 99, Low: 94, Close: 96, CloseTime: time.Unix(300, 0)}
	fills2 := b.Process(bar2)
	if len(fills2) != 1 {
		t.Fatalf("expected one stop-close fill, got %d: %+v", len(fills2), fills2)
	}
	if fills2[0].Side != strategy.SideSell {
		t.Fatalf("expected SideSell on stop close, got %s", fills2[0].Side)
	}
	if fills2[0].Price > 95.0 {
		t.Fatalf("expected close at SL price (95) or worse with slippage, got %f", fills2[0].Price)
	}
}

func TestSimBroker_StopLoss_LongNotHit(t *testing.T) {
	pf := newTestPortfolio(10000)
	b := NewSimBroker(0.001, 0.0005, pf, devLog())

	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETH", Side: strategy.SideBuy, Qty: 1.0, StopLoss: 95.0,
	})
	bar1 := exchange.Kline{Symbol: "ETH", High: 101, Low: 99, Close: 100, CloseTime: time.Unix(0, 0)}
	b.Process(bar1)

	// Bar with Low=96 — does NOT cross 95
	bar2 := exchange.Kline{Symbol: "ETH", High: 102, Low: 96, Close: 100, CloseTime: time.Unix(300, 0)}
	fills := b.Process(bar2)
	if len(fills) != 0 {
		t.Fatalf("expected no fills (stop not hit), got %d", len(fills))
	}
}

func TestSimBroker_StopLoss_ShortHit(t *testing.T) {
	pf := newTestPortfolio(10000)
	b := NewSimBroker(0.001, 0.0005, pf, devLog())

	// Open short with SL at 105
	b.PlaceOrder(strategy.OrderRequest{
		Symbol:       "ETH",
		Side:         strategy.SideSell,
		PositionSide: strategy.PositionSideShort,
		Qty:          1.0,
		StopLoss:     105.0,
	})
	bar1 := exchange.Kline{Symbol: "ETH", High: 101, Low: 99, Close: 100, CloseTime: time.Unix(0, 0)}
	b.Process(bar1)

	// Bar 2: High=106 (crosses 105 stop)
	bar2 := exchange.Kline{Symbol: "ETH", High: 106, Low: 100, Close: 102, CloseTime: time.Unix(300, 0)}
	fills := b.Process(bar2)
	if len(fills) != 1 {
		t.Fatalf("expected one stop-close fill, got %d", len(fills))
	}
	if fills[0].Side != strategy.SideBuy || fills[0].PositionSide != strategy.PositionSideShort {
		t.Fatalf("expected SideBuy + PositionSideShort, got Side=%s PosSide=%s", fills[0].Side, fills[0].PositionSide)
	}
	if fills[0].Price < 105.0 {
		t.Fatalf("expected close at SL price (105) or worse, got %f", fills[0].Price)
	}
}

func TestSimBroker_StopLoss_ClosingFillClearsStop(t *testing.T) {
	pf := newTestPortfolio(10000)
	b := NewSimBroker(0.001, 0.0005, pf, devLog())

	// Open long with SL
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETH", Side: strategy.SideBuy, Qty: 1.0, StopLoss: 95.0,
	})
	bar1 := exchange.Kline{Symbol: "ETH", High: 101, Low: 99, Close: 100, CloseTime: time.Unix(0, 0)}
	b.Process(bar1)

	// Manual close: SELL the long
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETH", Side: strategy.SideSell, Qty: 1.0,
	})
	bar2 := exchange.Kline{Symbol: "ETH", High: 102, Low: 100, Close: 101, CloseTime: time.Unix(300, 0)}
	b.Process(bar2)

	// Bar 3: Low dives to 90 — NO stop fill should emit (already closed)
	bar3 := exchange.Kline{Symbol: "ETH", High: 95, Low: 90, Close: 92, CloseTime: time.Unix(600, 0)}
	fills := b.Process(bar3)
	if len(fills) != 0 {
		t.Fatalf("expected no stop fill after manual close, got %d: %+v", len(fills), fills)
	}
}

// TestSimBroker_StopLoss_SurvivesPartialReduce reproduces a 2026-08-18
// backtest finding: macross's AsymmetricExit issues a partial reduce (e.g.
// ReduceFrac=0.5) while a position is underwater, expecting the STOP-LOSS
// registered at entry to keep protecting the remaining half. Process()
// treated ANY closing-direction fill as a full close and deleted the active
// stop outright — leaving the remainder with zero protection for the rest
// of that leg's life (real backtest MAE on affected legs reached -23.9%
// against a configured StopLossPct of 3%). This mirrors the already-fixed
// live/broker.go bug (2026-08-17: "a partial reduce must not cancel the
// remaining position's stop-loss") — the fix was never ported to the
// backtest simulator, silently making every AsymmetricExit backtest since
// then measure a harsher, unrealistic exit dynamic than live actually has.
func TestSimBroker_StopLoss_SurvivesPartialReduce(t *testing.T) {
	pf := newTestPortfolio(10000)
	b := NewSimBroker(0.001, 0.0005, pf, devLog())

	// Open long 1.0 ETH with a 95 stop.
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETH", Side: strategy.SideBuy, Qty: 1.0, StopLoss: 95.0,
	})
	bar1 := exchange.Kline{Symbol: "ETH", High: 101, Low: 99, Close: 100, CloseTime: time.Unix(0, 0)}
	b.Process(bar1)

	// Partial reduce: sell HALF (0.5), leaving 0.5 still open — not a full close.
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETH", Side: strategy.SideSell, Qty: 0.5, Reason: "asymmetric_reduce",
	})
	bar2 := exchange.Kline{Symbol: "ETH", High: 101, Low: 99, Close: 100, CloseTime: time.Unix(300, 0)}
	b.Process(bar2)

	// Bar 3: Low dives through the original 95 stop — the remaining 0.5 must
	// still be protected and close via the stop, not ride unprotected.
	bar3 := exchange.Kline{Symbol: "ETH", High: 96, Low: 90, Close: 92, CloseTime: time.Unix(600, 0)}
	fills := b.Process(bar3)
	if len(fills) != 1 {
		t.Fatalf("expected the remaining half to still be stop-protected, got %d fills: %+v", len(fills), fills)
	}
	if fills[0].Reason != "stop_loss" {
		t.Fatalf("expected a stop_loss fill, got reason %q", fills[0].Reason)
	}
	assert.InDelta(t, 0.5, fills[0].Qty, 1e-9, "the stop must close only the remaining half, not the original full qty")
}
