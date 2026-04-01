package paper

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/risk"
	"github.com/Quantix/quantix/internal/strategy"
)

// newTestBroker creates a paper broker with permissive risk settings.
func newTestBroker(initialCash float64) (*Broker, *oms.OMS) {
	log := zap.NewNop()
	o := oms.New(oms.ModePaper, log)
	rm := risk.New(risk.Config{
		MaxPositionPct:   1.0,
		MaxDrawdownPct:   1.0,
		MaxSingleLossPct: 1.0,
	}, initialCash, log)
	pm := oms.NewPositionManager()
	b := NewBroker(o, rm, pm, "test-strat", initialCash, 0.001, 0.0005, 1, log)
	return b, o
}

// drainFill reads one fill from the OMS fills channel with a 1s timeout.
func drainFill(t *testing.T, o *oms.OMS) oms.FillEvent {
	t.Helper()
	select {
	case fe := <-o.Fills():
		return fe
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for fill event")
		return oms.FillEvent{}
	}
}

func TestPaperBroker_MarketBuyAndSell(t *testing.T) {
	b, o := newTestBroker(10000)
	b.SetLastPrice(100)

	// Market buy 1.0 BTC
	buyReq := strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Qty:    1.0,
	}
	id := b.PlaceOrder(buyReq)
	require.NotEmpty(t, id, "PlaceOrder should return a non-empty order ID")

	fe := drainFill(t, o)
	assert.Equal(t, strategy.SideBuy, fe.Fill.Side)
	assert.InDelta(t, 1.0, fe.Fill.Qty, 1e-9)
	// Slippage: exec price should be > 100 for a buy
	assert.Greater(t, fe.Fill.Price, 100.0)
	// Cash should have decreased
	assert.Less(t, b.Cash(), 10000.0)

	// Apply fill to position manager so sell can find the position
	b.positions.ApplyFill(fe.Fill)

	// Market sell to close
	sellReq := strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideSell,
		Qty:    1.0,
	}
	sellID := b.PlaceOrder(sellReq)
	require.NotEmpty(t, sellID)

	sfe := drainFill(t, o)
	assert.Equal(t, strategy.SideSell, sfe.Fill.Side)
	assert.InDelta(t, 1.0, sfe.Fill.Qty, 1e-9)
	// Sell price should be < 100 (slippage downward)
	assert.Less(t, sfe.Fill.Price, 100.0)
}

func TestPaperBroker_LimitOrderTriggersOnBar(t *testing.T) {
	b, o := newTestBroker(10000)
	b.SetLastPrice(100)

	// Place limit buy at 95
	req := strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderLimit,
		Qty:    1.0,
		Price:  95,
	}
	id := b.PlaceOrder(req)
	require.NotEmpty(t, id)

	// Bar 1: high=102, low=96, close=100 — low(96) > 95 → no fill
	b.ProcessBar(102, 96, 100)
	select {
	case <-o.Fills():
		t.Fatal("should not have filled on first bar")
	case <-time.After(50 * time.Millisecond):
		// expected: no fill
	}

	// Bar 2: high=100, low=94, close=97 — low(94) <= 95 → fills
	b.ProcessBar(100, 94, 97)
	fe := drainFill(t, o)
	assert.Equal(t, strategy.SideBuy, fe.Fill.Side)
	assert.InDelta(t, 1.0, fe.Fill.Qty, 1e-9)
	// Fill price should be near 95 * (1 + 0.0005)
	assert.InDelta(t, 95*1.0005, fe.Fill.Price, 0.01)
}

func TestPaperBroker_StopMarketTriggersOnBar(t *testing.T) {
	b, o := newTestBroker(10000)
	b.SetLastPrice(100)

	// Buy first to have a position
	buyReq := strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Qty:    1.0,
	}
	buyID := b.PlaceOrder(buyReq)
	require.NotEmpty(t, buyID)
	buyFill := drainFill(t, o)
	b.positions.ApplyFill(buyFill.Fill)

	// Place stop-sell at 90
	stopReq := strategy.OrderRequest{
		Symbol:    "BTCUSDT",
		Side:      strategy.SideSell,
		Type:      strategy.OrderStopMarket,
		Qty:       1.0,
		StopPrice: 90,
	}
	stopID := b.PlaceOrder(stopReq)
	require.NotEmpty(t, stopID)

	// Bar 1: high=100, low=91, close=95 — low(91) > 90 → no fill
	b.ProcessBar(100, 91, 95)
	select {
	case <-o.Fills():
		t.Fatal("should not have filled on first bar")
	case <-time.After(50 * time.Millisecond):
		// expected
	}

	// Bar 2: high=95, low=89, close=92 — low(89) <= 90 → fills
	b.ProcessBar(95, 89, 92)
	fe := drainFill(t, o)
	assert.Equal(t, strategy.SideSell, fe.Fill.Side)
	assert.InDelta(t, 1.0, fe.Fill.Qty, 1e-9)
	// Fill price should be near 90 * (1 - 0.0005)
	assert.InDelta(t, 90*0.9995, fe.Fill.Price, 0.01)
}

func TestPaperBroker_ShortMarginAccounting(t *testing.T) {
	log := zap.NewNop()
	o := oms.New(oms.ModePaper, log)
	rm := risk.New(risk.Config{
		MaxPositionPct:   1.0,
		MaxDrawdownPct:   1.0,
		MaxSingleLossPct: 1.0,
	}, 10000, log)
	pm := oms.NewPositionManager()
	// 10x leverage broker
	b := NewBroker(o, rm, pm, "test-strat", 10000, 0.001, 0.0005, 10, log)
	b.SetLastPrice(100)

	// Open short 10 BTC @ ~100
	req := strategy.OrderRequest{
		Symbol:       "BTCUSDT",
		Side:         strategy.SideSell,
		PositionSide: strategy.PositionSideShort,
		Qty:          10,
	}
	id := b.PlaceOrder(req)
	require.NotEmpty(t, id)

	fe := drainFill(t, o)
	assert.Equal(t, strategy.SideSell, fe.Fill.Side)
	assert.InDelta(t, 10.0, fe.Fill.Qty, 1e-9)

	// margin = qty * price * (1/leverage) = 10 * ~99.95 * 0.1 ≈ 99.95
	// fee = 10 * ~99.95 * 0.001 ≈ ~1.0
	// cash ≈ 10000 - 99.95 - 1.0 ≈ 9899
	cash := b.Cash()
	assert.InDelta(t, 9900, cash, 5.0, "cash should be ~9900 after short margin deduction")
}

func TestPaperBroker_ZeroPriceRejectsOrder(t *testing.T) {
	b, _ := newTestBroker(10000)
	// Do NOT set lastPrice → it defaults to 0.0

	req := strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Qty:    1.0,
	}
	id := b.PlaceOrder(req)
	assert.Empty(t, id, "PlaceOrder should return empty string when price is zero")
}

func TestPaperBroker_CashEquitySetters(t *testing.T) {
	b, _ := newTestBroker(10000)

	b.SetCashEquity(5000, 6000)
	assert.InDelta(t, 5000, b.Cash(), 1e-9)
	assert.InDelta(t, 6000, b.Equity(), 1e-9)
}

func TestPaperBroker_ShortCloseRealizedPnL(t *testing.T) {
	log := zap.NewNop()
	o := oms.New(oms.ModePaper, log)
	o.SetContext(context.Background())
	rm := risk.New(risk.Config{
		MaxPositionPct:   1.0,
		MaxDrawdownPct:   1.0,
		MaxSingleLossPct: 1.0,
	}, 10000, log)
	pm := oms.NewPositionManager()
	// 10x leverage, 0 fees, 0 slippage for clean math
	b := NewBroker(o, rm, pm, "test-short-pnl", 10000, 0.0, 0.0, 10, log)

	// Step 1: Set last price to 50000
	b.SetLastPrice(50000)

	// Step 2: Open short — Sell 0.1 BTC with PositionSide=SHORT
	openReq := strategy.OrderRequest{
		Symbol:       "BTCUSDT",
		Side:         strategy.SideSell,
		PositionSide: strategy.PositionSideShort,
		Qty:          0.1,
	}
	openID := b.PlaceOrder(openReq)
	require.NotEmpty(t, openID, "open short PlaceOrder should return a non-empty order ID")

	openFill := drainFill(t, o)
	assert.Equal(t, strategy.SideSell, openFill.Fill.Side)
	assert.InDelta(t, 0.1, openFill.Fill.Qty, 1e-9)

	// Step 3: Assert cash after open ≈ 9500
	// margin locked = 0.1 * 50000 * (1/10) = 500; no fees
	assert.InDelta(t, 9500, b.Cash(), 1.0, "cash after short open should be ~9500")

	// Step 4: Apply fill to position manager — realized should be 0 on open
	realized := pm.ApplyFill(openFill.Fill)
	assert.InDelta(t, 0.0, realized, 1e-9, "realized PnL on open should be 0")

	// Step 5: Set last price to 48000 (profitable short)
	b.SetLastPrice(48000)

	// Step 6: Close short — Buy 0.1 BTC with PositionSide=SHORT
	closeReq := strategy.OrderRequest{
		Symbol:       "BTCUSDT",
		Side:         strategy.SideBuy,
		PositionSide: strategy.PositionSideShort,
		Qty:          0.1,
	}
	closeID := b.PlaceOrder(closeReq)
	require.NotEmpty(t, closeID, "close short PlaceOrder should return a non-empty order ID")

	closeFill := drainFill(t, o)
	assert.Equal(t, strategy.SideBuy, closeFill.Fill.Side)
	assert.InDelta(t, 0.1, closeFill.Fill.Qty, 1e-9)

	// Step 7: Apply close fill to PM — realized should be ≈ 200
	// realized = (50000 - 48000) * 0.1 - 0 fee = 200
	realized = pm.ApplyFill(closeFill.Fill)
	assert.InDelta(t, 200.0, realized, 1.0, "realized PnL on close should be ~200")

	// Step 8 & 9: broker's applyCashForFill returns margin but NOT realized PnL
	// cash after close = 9500 + (0.1 * 48000 * 0.1) = 9500 + 480 = 9980
	// total = cash + realized = 9980 + 200 = 10180
	assert.InDelta(t, 10180, b.Cash()+realized, 1.0, "cash + realized should be ~10180")
}
