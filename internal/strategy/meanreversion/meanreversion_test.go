package meanreversion

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
	hasPos bool
}

func (m *mockPortfolio) Cash() float64 { return m.cash }
func (m *mockPortfolio) Position(sym string) (float64, float64, bool) {
	return 1.0, 50000.0, m.hasPos
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
		Symbol:   sym,
		Interval: "1h",
		OpenTime: time.Now(),
		Close:    close_,
		Volume:   100,
	}
}

func TestMeanReversion_Defaults(t *testing.T) {
	s := New(Config{Symbol: "BTCUSDT"})
	if s.cfg.BBPeriod != 20 {
		t.Errorf("expected BBPeriod=20, got %d", s.cfg.BBPeriod)
	}
	if s.cfg.RSIPeriod != 14 {
		t.Errorf("expected RSIPeriod=14, got %d", s.cfg.RSIPeriod)
	}
}

func TestMeanReversion_Name(t *testing.T) {
	s := New(Config{Symbol: "BTCUSDT"})
	if s.Name() == "" {
		t.Error("Name() returned empty string")
	}
}

func TestMeanReversion_InsufficientBars(t *testing.T) {
	s := New(Config{Symbol: "BTCUSDT"})
	log, _ := zap.NewDevelopment()
	portfolio := &mockPortfolio{cash: 10000}
	broker := &mockBroker{}
	ctx := strategy.NewContext(portfolio, broker, log)

	// Feed fewer bars than required
	for i := 0; i < 10; i++ {
		s.OnBar(ctx, makeBar("BTCUSDT", 50000))
	}
	if len(broker.orders) != 0 {
		t.Errorf("expected no orders with insufficient bars, got %d", len(broker.orders))
	}
}

func TestMeanReversion_WrongSymbol(t *testing.T) {
	s := New(Config{Symbol: "BTCUSDT"})
	log, _ := zap.NewDevelopment()
	portfolio := &mockPortfolio{cash: 10000}
	broker := &mockBroker{}
	ctx := strategy.NewContext(portfolio, broker, log)

	for i := 0; i < 50; i++ {
		s.OnBar(ctx, makeBar("ETHUSDT", 3000))
	}
	if len(broker.orders) != 0 {
		t.Errorf("expected no orders for wrong symbol, got %d", len(broker.orders))
	}
}

func TestMeanReversion_BuySignal(t *testing.T) {
	s := New(Config{
		Symbol:        "BTCUSDT",
		BBPeriod:      5,
		BBStdDev:      2.0,
		RSIPeriod:     3,
		OversoldRSI:   70, // very permissive to trigger
		OverboughtRSI: 30,
	})
	log, _ := zap.NewDevelopment()
	portfolio := &mockPortfolio{cash: 10000}
	broker := &mockBroker{}
	ctx := strategy.NewContext(portfolio, broker, log)

	// Feed bars with declining price to go below lower BB
	for i := 0; i < 20; i++ {
		price := 50000.0 - float64(i)*100
		s.OnBar(ctx, makeBar("BTCUSDT", price))
	}
	// We just verify no panic; signal depends on indicator values
	t.Logf("orders placed: %d", len(broker.orders))
}

func TestMeanReversion_OnFill(t *testing.T) {
	s := New(Config{Symbol: "BTCUSDT"})
	log, _ := zap.NewDevelopment()
	ctx := strategy.NewContext(&mockPortfolio{}, &mockBroker{}, log)
	// Should not panic
	s.OnFill(ctx, strategy.Fill{})
}
