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
