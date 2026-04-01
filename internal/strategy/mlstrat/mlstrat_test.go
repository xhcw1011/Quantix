package mlstrat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/ml"
	"github.com/Quantix/quantix/internal/strategy"
)

// mockPortfolio implements strategy.PortfolioView for tests.
type mockPortfolio struct {
	cash     float64
	qty      float64
	hasPos   bool
}

func (m *mockPortfolio) Cash() float64 { return m.cash }
func (m *mockPortfolio) Position(symbol string) (float64, float64, bool) {
	return m.qty, 0, m.hasPos
}
func (m *mockPortfolio) Equity(prices map[string]float64) float64 { return m.cash }

// mockBroker records placed orders.
type mockBroker struct {
	orders []strategy.OrderRequest
}

func (b *mockBroker) PlaceOrder(req strategy.OrderRequest) string {
	b.orders = append(b.orders, req)
	return fmt.Sprintf("mock-%d", len(b.orders))
}
func (b *mockBroker) CancelOrder(_ string) error { return nil }

// writeTestModel writes a minimal model JSON to a temp file and returns the path.
// Uses zero coefficients + large positive intercept so p ≈ 1 always → buy signal.
func writeTestModel(t *testing.T) string {
	t.Helper()
	m := ml.Model{
		Coefficients: []float64{0, 0, 0, 0, 0, 0, 0},
		Intercept:    10.0, // sigmoid(10) ≈ 1.0 → always buy
		FeatureNames: []string{"rsi", "macd_hist", "bb_pos", "ret_5", "ret_20", "vol_20", "vol_ratio"},
		Scaler: ml.Scaler{
			Mean:  []float64{0, 0, 0, 0, 0, 0, 0},
			Scale: []float64{1, 1, 1, 1, 1, 1, 1},
		},
	}
	data, _ := json.Marshal(m)
	path := filepath.Join(t.TempDir(), "model.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write model: %v", err)
	}
	return path
}

func TestMLStrategy_BuySignal(t *testing.T) {
	modelPath := writeTestModel(t)
	strat, err := New(Config{
		Symbol:        "BTCUSDT",
		ModelPath:     modelPath,
		BuyThreshold:  0.6,
		SellThreshold: 0.4,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	log, _ := zap.NewDevelopment()
	portfolio := &mockPortfolio{cash: 10000}
	broker := &mockBroker{}
	ctx := strategy.NewContext(portfolio, broker, log)

	// Feed enough bars for features to compute (>= ~60 bars)
	bar := exchange.Kline{
		Symbol: "BTCUSDT",
		Close:  50000,
		Volume: 100,
	}
	for i := 0; i < 70; i++ {
		bar.Close = 50000 + float64(i)*10
		strat.OnBar(ctx, bar)
	}

	// With intercept=10, sigmoid always high → should have placed a BUY
	if len(broker.orders) == 0 {
		t.Error("expected at least one BUY order")
	}
	if broker.orders[0].Side != strategy.SideBuy {
		t.Errorf("expected BUY, got %s", broker.orders[0].Side)
	}
}

func TestMLStrategy_SellSignal(t *testing.T) {
	// Use model with zero coefficients and large negative intercept.
	// p = sigmoid(-10) ≈ 0.0000454 which is always <= SellThreshold(0.4).
	modelPath := func() string {
		m := ml.Model{
			Coefficients: []float64{0, 0, 0, 0, 0, 0, 0},
			Intercept:    -10.0,
			FeatureNames: []string{"rsi", "macd_hist", "bb_pos", "ret_5", "ret_20", "vol_20", "vol_ratio"},
			Scaler: ml.Scaler{
				Mean:  []float64{0, 0, 0, 0, 0, 0, 0},
				Scale: []float64{1, 1, 1, 1, 1, 1, 1},
			},
		}
		data, _ := json.Marshal(m)
		dir := t.TempDir()
		p := filepath.Join(dir, "model.json")
		os.WriteFile(p, data, 0o600)
		return p
	}()

	strat, err := New(Config{
		Symbol:        "BTCUSDT",
		ModelPath:     modelPath,
		BuyThreshold:  0.6,
		SellThreshold: 0.4,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	log, _ := zap.NewDevelopment()
	portfolio := &mockPortfolio{cash: 5000, qty: 1.0, hasPos: true}
	broker := &mockBroker{}
	ctx := strategy.NewContext(portfolio, broker, log)

	bar := exchange.Kline{Symbol: "BTCUSDT", Close: 50000, Volume: 100}
	for i := 0; i < 70; i++ {
		bar.Close = 50000 + float64(i)*10
		strat.OnBar(ctx, bar)
	}

	// Should have placed a SELL
	hasSell := false
	for _, o := range broker.orders {
		if o.Side == strategy.SideSell {
			hasSell = true
		}
	}
	if !hasSell {
		t.Error("expected at least one SELL order")
	}
}
