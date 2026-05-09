package composite

import (
	"math"
	"testing"

	"github.com/Quantix/quantix/internal/alpha"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
)

func TestPositionSize_RiskBased(t *testing.T) {
	feat := alpha.Features{Close: 2300, ATR: 3.0}
	cfg := Config{RiskPerTrade: 0.01, SLATR: 1.5}.withDefaults()
	pv := &fakePortfolio{cash: 10000}
	ctx := strategy.NewContext(pv, &fakeBroker{}, zap.NewNop())

	qty := positionSize(ctx, feat, cfg)

	// risk_usd = 10000 * 0.01 = 100
	// sl_dist  = 3.0 * 1.5 = 4.5
	// raw_qty  = 100 / 4.5 = 22.222...
	// rounded down to 0.001 step
	want := math.Floor(22.222*1000) / 1000
	if math.Abs(qty-want) > 1e-6 {
		t.Fatalf("qty = %f want %f", qty, want)
	}
}

func TestPositionSize_ZeroATRReturnsZero(t *testing.T) {
	feat := alpha.Features{Close: 2300, ATR: 0}
	cfg := Config{}.withDefaults()
	pv := &fakePortfolio{cash: 10000}
	ctx := strategy.NewContext(pv, &fakeBroker{}, zap.NewNop())
	if positionSize(ctx, feat, cfg) != 0 {
		t.Fatalf("zero ATR must return zero qty")
	}
}

func TestPositionSize_ZeroEquityReturnsZero(t *testing.T) {
	feat := alpha.Features{Close: 2300, ATR: 3.0}
	cfg := Config{}.withDefaults()
	pv := &fakePortfolio{cash: 0}
	ctx := strategy.NewContext(pv, &fakeBroker{}, zap.NewNop())
	if positionSize(ctx, feat, cfg) != 0 {
		t.Fatalf("zero equity must return zero qty")
	}
}

// fakePortfolio + fakeBroker stubs (used here AND in upcoming Task 5 OnBar tests).
type fakePortfolio struct{ cash float64 }

func (p *fakePortfolio) Cash() float64 { return p.cash }
func (p *fakePortfolio) Position(symbol string) (float64, float64, bool) {
	return 0, 0, false
}
func (p *fakePortfolio) Equity(_ map[string]float64) float64 { return p.cash }

type fakeBroker struct {
	orders []strategy.OrderRequest
}

func (b *fakeBroker) PlaceOrder(req strategy.OrderRequest) string {
	b.orders = append(b.orders, req)
	return "order-1"
}
func (b *fakeBroker) CancelOrder(id string) error { return nil }
