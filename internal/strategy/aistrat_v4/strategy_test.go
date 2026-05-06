package aistrat_v4

import (
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// fakePortfolio implements strategy.PortfolioView with fixed equity.
type fakePortfolio struct{ equity float64 }

func (f *fakePortfolio) Cash() float64                          { return f.equity }
func (f *fakePortfolio) Equity(_ map[string]float64) float64    { return f.equity }
func (f *fakePortfolio) Position(_ string) (float64, float64, bool) {
	return 0, 0, false
}

// fakeBroker captures placed orders for verification.
type fakeBroker struct {
	orders []strategy.OrderRequest
	idCnt  int
}

func (b *fakeBroker) PlaceOrder(req strategy.OrderRequest) string {
	b.orders = append(b.orders, req)
	b.idCnt++
	return "OMS-FAKE"
}
func (b *fakeBroker) CancelOrder(_ string) error { return nil }

func TestOnBarOpenShortOnHighZ(t *testing.T) {
	log := zap.NewNop()
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	s := New(cfg, log)

	pv := &fakePortfolio{equity: 5000}
	br := &fakeBroker{}
	ctx := strategy.NewContext(pv, br, log)

	now := time.Now()
	// Feed 20 alternating bars (105/95) → window mean=100, std=5
	for i := 0; i < 20; i++ {
		c := 105.0
		if i%2 == 1 {
			c = 95.0
		}
		bar := exchange.Kline{
			Symbol: "ETHUSDT", OpenTime: now.Add(time.Duration(i) * 5 * time.Minute),
			Open: c, High: c + 0.5, Low: c - 0.5, Close: c, Volume: 1,
		}
		s.OnBar(ctx, bar)
	}
	// 21st bar at 115 → z = (115-100)/5 = 3.0 → SHORT signal
	signalBar := exchange.Kline{
		Symbol: "ETHUSDT", OpenTime: now.Add(20 * 5 * time.Minute),
		Open: 100, High: 116, Low: 99.5, Close: 115, Volume: 1,
	}
	s.OnBar(ctx, signalBar)

	if len(br.orders) == 0 {
		t.Fatal("no order placed despite high-z signal")
	}
	o := br.orders[0]
	if o.PositionSide != strategy.PositionSideShort {
		t.Errorf("PositionSide = %v, want SHORT", o.PositionSide)
	}
	if o.Side != strategy.SideSell {
		t.Errorf("Side = %v, want SELL (open SHORT)", o.Side)
	}
	if o.Type != strategy.OrderMarket {
		t.Errorf("Type = %v, want MARKET", o.Type)
	}
	if o.Qty <= 0 {
		t.Errorf("Qty = %f, want positive", o.Qty)
	}
}

func TestOnBarNoOrderBelowThreshold(t *testing.T) {
	log := zap.NewNop()
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	s := New(cfg, log)

	pv := &fakePortfolio{equity: 5000}
	br := &fakeBroker{}
	ctx := strategy.NewContext(pv, br, log)

	now := time.Now()
	for i := 0; i < 21; i++ {
		c := 100.0 + float64(i%2) // tiny variation, low z
		bar := exchange.Kline{
			Symbol: "ETHUSDT", OpenTime: now.Add(time.Duration(i) * 5 * time.Minute),
			Open: c, High: c + 0.5, Low: c - 0.5, Close: c, Volume: 1,
		}
		s.OnBar(ctx, bar)
	}

	if len(br.orders) > 0 {
		t.Errorf("got %d orders, want 0 (no signal)", len(br.orders))
	}
}
