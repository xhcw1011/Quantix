package grid

import (
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

type mockPortfolio struct {
	cash   float64
	qty    float64
	hasPos bool
}

func (m *mockPortfolio) Cash() float64 { return m.cash }
func (m *mockPortfolio) Position(sym string) (float64, float64, bool) {
	return m.qty, 0, m.hasPos
}
func (m *mockPortfolio) Equity(prices map[string]float64) float64 { return m.cash }

type mockBroker struct{ orders []strategy.OrderRequest }

func (b *mockBroker) PlaceOrder(req strategy.OrderRequest) string {
	b.orders = append(b.orders, req)
	return fmt.Sprintf("mock-%d", len(b.orders))
}
func (b *mockBroker) CancelOrder(_ string) error { return nil }

func makeBar(sym string, close_ float64) exchange.Kline {
	return exchange.Kline{
		Symbol: sym, Interval: "1h",
		OpenTime: time.Now(), Close: close_, Volume: 100,
	}
}

func TestGrid_Defaults(t *testing.T) {
	g := New(Config{Symbol: "BTCUSDT"})
	if g.cfg.GridLevels != 5 {
		t.Errorf("expected GridLevels=5, got %d", g.cfg.GridLevels)
	}
	if g.cfg.GridSpacing != 0.01 {
		t.Errorf("expected GridSpacing=0.01, got %f", g.cfg.GridSpacing)
	}
}

func TestGrid_Name(t *testing.T) {
	g := New(Config{Symbol: "BTCUSDT"})
	if g.Name() == "" {
		t.Error("Name() returned empty string")
	}
}

func TestGrid_FirstBarInitialisesBase(t *testing.T) {
	g := New(Config{Symbol: "BTCUSDT", GridLevels: 3, GridSpacing: 0.01})
	log, _ := zap.NewDevelopment()
	broker := &mockBroker{}
	ctx := strategy.NewContext(&mockPortfolio{cash: 10000}, broker, log)

	g.OnBar(ctx, makeBar("BTCUSDT", 50000))

	if g.basePrice != 50000 {
		t.Errorf("expected basePrice=50000, got %f", g.basePrice)
	}
	if len(g.levelPrice) != 3 {
		t.Errorf("expected 3 level prices, got %d", len(g.levelPrice))
	}
	// No orders on first bar (just initialise)
	if len(broker.orders) != 0 {
		t.Errorf("expected no orders on init bar, got %d", len(broker.orders))
	}
}

func TestGrid_BuyAtLevel(t *testing.T) {
	g := New(Config{Symbol: "BTCUSDT", GridLevels: 2, GridSpacing: 0.05, BaseQty: 0.01})
	log, _ := zap.NewDevelopment()
	portfolio := &mockPortfolio{cash: 10000}
	broker := &mockBroker{}
	ctx := strategy.NewContext(portfolio, broker, log)

	// Init at 50000
	g.OnBar(ctx, makeBar("BTCUSDT", 50000))
	// Level 0 buy price = 50000*(1-0.05) = 47500
	// Level 1 buy price = 50000*(1-0.10) = 45000

	// Price drops to 47000 (below level 0)
	g.OnBar(ctx, makeBar("BTCUSDT", 47000))

	buyOrders := 0
	for _, o := range broker.orders {
		if o.Side == strategy.SideBuy {
			buyOrders++
		}
	}
	if buyOrders == 0 {
		t.Error("expected at least one BUY order when price below level 0")
	}
}

func TestGrid_WrongSymbol(t *testing.T) {
	g := New(Config{Symbol: "BTCUSDT", GridLevels: 3, GridSpacing: 0.01, BaseQty: 0.01})
	log, _ := zap.NewDevelopment()
	broker := &mockBroker{}
	ctx := strategy.NewContext(&mockPortfolio{cash: 10000}, broker, log)

	for i := 0; i < 5; i++ {
		g.OnBar(ctx, makeBar("ETHUSDT", 3000))
	}
	if len(broker.orders) != 0 {
		t.Errorf("expected no orders for wrong symbol, got %d", len(broker.orders))
	}
}

func TestGrid_OnFill(t *testing.T) {
	g := New(Config{Symbol: "BTCUSDT"})
	log, _ := zap.NewDevelopment()
	ctx := strategy.NewContext(&mockPortfolio{}, &mockBroker{}, log)
	// Should not panic
	g.OnFill(ctx, strategy.Fill{})
}
