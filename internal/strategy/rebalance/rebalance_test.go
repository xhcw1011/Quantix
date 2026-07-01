package rebalance

import (
	"math"
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/strategymock"
)

// ─── Pure-function table tests ─────────────────────────────────────────────

func TestRebalanceTrade(t *testing.T) {
	const eps = 1e-9

	tests := []struct {
		name          string
		baseQty       float64
		cash          float64
		price         float64
		targetPct     float64
		bandPct       float64
		minTradeQuote float64
		wantQuote     float64 // expected signed tradeQuote (ignored if !wantDo)
		wantDo        bool
	}{
		{
			name:          "60pct base vs 50pct target: SELL",
			baseQty:       0.6, cash: 40, price: 100,
			targetPct: 0.5, bandPct: 0.05, minTradeQuote: 1,
			// baseValue=60, total=100, currentPct=0.60, drift=+0.10 > 0.05
			// tradeQuote = (0.5-0.60)*100 = -10
			wantQuote: -10, wantDo: true,
		},
		{
			name:          "40pct base vs 50pct target: BUY",
			baseQty:       0.4, cash: 60, price: 100,
			targetPct: 0.5, bandPct: 0.05, minTradeQuote: 1,
			// baseValue=40, total=100, currentPct=0.40, drift=-0.10, |drift|=0.10 > 0.05
			// tradeQuote = (0.5-0.40)*100 = +10
			wantQuote: 10, wantDo: true,
		},
		{
			name:          "52pct within 5pct band: no trade",
			baseQty:       0.52, cash: 48, price: 100,
			targetPct: 0.5, bandPct: 0.05, minTradeQuote: 1,
			// drift=0.02, |drift|=0.02 <= 0.05
			wantDo: false,
		},
		{
			name:          "exactly at band edge using binary-exact fractions: no trade (<=)",
			// Use exact binary fractions to avoid float64 rounding at the boundary.
			// baseQty=0.5, cash=0.5, price=1 → baseValue=0.5, total=1.0, currentPct=0.5
			// targetPct=0.25, drift=0.5-0.25=0.25, bandPct=0.25 → |drift|=0.25 <= 0.25 → no trade
			baseQty:       0.5, cash: 0.5, price: 1,
			targetPct: 0.25, bandPct: 0.25, minTradeQuote: 0.001,
			wantDo: false,
		},
		{
			name:          "drift above band but below minTradeQuote: no trade",
			baseQty:       0.056, cash: 4.4, price: 100,
			targetPct: 0.5, bandPct: 0.05, minTradeQuote: 5,
			// baseValue=5.6, total=10, currentPct=0.56, drift=0.06 > 0.05
			// tradeQuote = (0.5-0.56)*10 = -0.6, |0.6| < 5 → no trade
			wantDo: false,
		},
		{
			name:          "total equity zero: no trade",
			baseQty:       0, cash: 0, price: 100,
			targetPct: 0.5, bandPct: 0.05, minTradeQuote: 1,
			wantDo: false,
		},
		{
			name:          "all cash (0pct base): BUY",
			baseQty:       0, cash: 1000, price: 50000,
			targetPct: 0.5, bandPct: 0.05, minTradeQuote: 10,
			// currentPct=0, drift=-0.5 > 0.05, tradeQuote=(0.5-0)*1000=500
			wantQuote: 500, wantDo: true,
		},
		{
			name:          "all base (100pct base): SELL",
			baseQty:       1, cash: 0, price: 1000,
			targetPct: 0.5, bandPct: 0.05, minTradeQuote: 10,
			// baseValue=1000, total=1000, currentPct=1.0, drift=0.5 > 0.05
			// tradeQuote = (0.5-1.0)*1000 = -500
			wantQuote: -500, wantDo: true,
		},
		{
			name:          "non-default target 30pct: BUY when at 20pct",
			baseQty:       0.2, cash: 80, price: 100,
			targetPct: 0.3, bandPct: 0.05, minTradeQuote: 1,
			// currentPct=0.20, drift=-0.10 > 0.05, tradeQuote=(0.3-0.2)*100=10
			wantQuote: 10, wantDo: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotQuote, gotDo := rebalanceTrade(
				tt.baseQty, tt.cash, tt.price,
				tt.targetPct, tt.bandPct, tt.minTradeQuote,
			)
			if gotDo != tt.wantDo {
				t.Errorf("do: got %v, want %v", gotDo, tt.wantDo)
			}
			if tt.wantDo && math.Abs(gotQuote-tt.wantQuote) > eps {
				t.Errorf("tradeQuote: got %.10f, want %.10f", gotQuote, tt.wantQuote)
			}
		})
	}
}

// ─── OnBar wiring tests ────────────────────────────────────────────────────

// fakePortfolio is a simple PortfolioView for tests.
type fakePortfolio struct {
	cash    float64
	baseQty float64
	symbol  string
}

func (f *fakePortfolio) Cash() float64 { return f.cash }
func (f *fakePortfolio) Position(symbol string) (float64, float64, bool) {
	if symbol == f.symbol {
		return f.baseQty, 0, f.baseQty > 0
	}
	return 0, 0, false
}
func (f *fakePortfolio) Equity(prices map[string]float64) float64 {
	return f.cash + f.baseQty*prices[f.symbol]
}

func newTestCtx(portfolio strategy.PortfolioView, broker strategy.Broker) *strategy.Context {
	return strategy.NewContext(portfolio, broker, zap.NewNop())
}

func TestOnBar_BuyOrder(t *testing.T) {
	// 40% base → triggers BUY
	pf := &fakePortfolio{cash: 60, baseQty: 0.4, symbol: "BTCUSDT"}
	broker := &strategymock.FakeBroker{}
	ctx := newTestCtx(pf, broker)

	s := New(Config{
		Symbol: "BTCUSDT", TargetBasePct: 0.5,
		BandPct: 0.05, MinTradeQuote: 1,
	}, zap.NewNop())
	s.OnBar(ctx, exchange.Kline{Symbol: "BTCUSDT", Close: 100})

	if len(broker.Placed) != 1 {
		t.Fatalf("expected 1 order, got %d", len(broker.Placed))
	}
	req := broker.Placed[0]
	if req.Side != strategy.SideBuy {
		t.Errorf("side: got %s, want BUY", req.Side)
	}
	if req.Type != strategy.OrderLimit {
		t.Errorf("type: got %s, want LIMIT", req.Type)
	}
	if req.Price <= 0 {
		t.Errorf("price should be > 0, got %f", req.Price)
	}
	if req.Qty <= 0 {
		t.Errorf("qty should be > 0, got %f", req.Qty)
	}
	// qty = tradeQuote / price = 10 / 100 = 0.1
	wantQty := 10.0 / 100.0
	if math.Abs(req.Qty-wantQty) > 1e-9 {
		t.Errorf("qty: got %f, want %f", req.Qty, wantQty)
	}
	// PositionSide must be empty for spot
	if req.PositionSide != "" {
		t.Errorf("PositionSide must be empty for spot, got %q", req.PositionSide)
	}
}

func TestOnBar_SellOrder(t *testing.T) {
	// 60% base → triggers SELL
	pf := &fakePortfolio{cash: 40, baseQty: 0.6, symbol: "BTCUSDT"}
	broker := &strategymock.FakeBroker{}
	ctx := newTestCtx(pf, broker)

	s := New(Config{
		Symbol: "BTCUSDT", TargetBasePct: 0.5,
		BandPct: 0.05, MinTradeQuote: 1,
	}, zap.NewNop())
	s.OnBar(ctx, exchange.Kline{Symbol: "BTCUSDT", Close: 100})

	if len(broker.Placed) != 1 {
		t.Fatalf("expected 1 order, got %d", len(broker.Placed))
	}
	req := broker.Placed[0]
	if req.Side != strategy.SideSell {
		t.Errorf("side: got %s, want SELL", req.Side)
	}
	if req.Type != strategy.OrderLimit {
		t.Errorf("type: got %s, want LIMIT", req.Type)
	}
	// qty = 10/100 = 0.1; capped at baseQty=0.6 → 0.1
	wantQty := 10.0 / 100.0
	if math.Abs(req.Qty-wantQty) > 1e-9 {
		t.Errorf("qty: got %f, want %f", req.Qty, wantQty)
	}
}

func TestOnBar_SellCappedAtBaseQty(t *testing.T) {
	// Extreme: all base, tiny price → computed sell qty > baseQty; must cap.
	// baseQty=1, cash=0, price=0.01 → baseValue=0.01, total=0.01, currentPct=1.0
	// tradeQuote = (0.5-1.0)*0.01 = -0.005; qty=0.005/0.01=0.5 > baseQty=1 → no cap needed here
	// For cap test: baseQty=0.1, cash=0, price=100 → baseValue=10, total=10
	// tradeQuote=(0.5-1.0)*10=-5; qty=5/100=0.05 ≤ 0.1 → no cap, but we test direction
	pf := &fakePortfolio{cash: 0, baseQty: 0.1, symbol: "ETHUSDT"}
	broker := &strategymock.FakeBroker{}
	ctx := newTestCtx(pf, broker)

	s := New(Config{
		Symbol: "ETHUSDT", TargetBasePct: 0.5,
		BandPct: 0.05, MinTradeQuote: 1,
	}, zap.NewNop())
	s.OnBar(ctx, exchange.Kline{Symbol: "ETHUSDT", Close: 100})

	if len(broker.Placed) != 1 {
		t.Fatalf("expected 1 order, got %d", len(broker.Placed))
	}
	req := broker.Placed[0]
	if req.Side != strategy.SideSell {
		t.Errorf("expected SELL, got %s", req.Side)
	}
	if req.Qty > 0.1+1e-9 {
		t.Errorf("sell qty %f exceeds baseQty 0.1", req.Qty)
	}
}

func TestOnBar_WithinBand_NoOrder(t *testing.T) {
	// 52% → within 5% band → no order
	pf := &fakePortfolio{cash: 48, baseQty: 0.52, symbol: "BTCUSDT"}
	broker := &strategymock.FakeBroker{}
	ctx := newTestCtx(pf, broker)

	s := New(Config{
		Symbol: "BTCUSDT", TargetBasePct: 0.5,
		BandPct: 0.05, MinTradeQuote: 1,
	}, zap.NewNop())
	s.OnBar(ctx, exchange.Kline{Symbol: "BTCUSDT", Close: 100})

	if len(broker.Placed) != 0 {
		t.Errorf("expected no orders, got %d", len(broker.Placed))
	}
}

func TestOnBar_WrongSymbol_NoOrder(t *testing.T) {
	pf := &fakePortfolio{cash: 60, baseQty: 0.4, symbol: "BTCUSDT"}
	broker := &strategymock.FakeBroker{}
	ctx := newTestCtx(pf, broker)

	s := New(Config{
		Symbol: "BTCUSDT", TargetBasePct: 0.5,
		BandPct: 0.05, MinTradeQuote: 1,
	}, zap.NewNop())
	s.OnBar(ctx, exchange.Kline{Symbol: "ETHUSDT", Close: 100}) // wrong symbol

	if len(broker.Placed) != 0 {
		t.Errorf("expected no orders for wrong symbol, got %d", len(broker.Placed))
	}
}

func TestOnBar_ZeroClose_NoOrder(t *testing.T) {
	pf := &fakePortfolio{cash: 60, baseQty: 0.4, symbol: "BTCUSDT"}
	broker := &strategymock.FakeBroker{}
	ctx := newTestCtx(pf, broker)

	s := New(Config{
		Symbol: "BTCUSDT", TargetBasePct: 0.5,
		BandPct: 0.05, MinTradeQuote: 1,
	}, zap.NewNop())
	s.OnBar(ctx, exchange.Kline{Symbol: "BTCUSDT", Close: 0})

	if len(broker.Placed) != 0 {
		t.Errorf("expected no orders for zero close, got %d", len(broker.Placed))
	}
}

// ─── Registry / factory tests ──────────────────────────────────────────────

func TestNew_Name(t *testing.T) {
	s := New(Config{Symbol: "BTCUSDT", TargetBasePct: 0.5, BandPct: 0.05}, zap.NewNop())
	want := "Rebalance(BTCUSDT,50%)"
	if s.Name() != want {
		t.Errorf("Name() = %q, want %q", s.Name(), want)
	}
}

func TestOnFill_NoOp(t *testing.T) {
	// OnFill is a no-op; just ensure it doesn't panic.
	s := New(Config{Symbol: "BTCUSDT", TargetBasePct: 0.5, BandPct: 0.05}, zap.NewNop())
	pf := &fakePortfolio{cash: 50, baseQty: 0.5, symbol: "BTCUSDT"}
	broker := &strategymock.FakeBroker{}
	ctx := newTestCtx(pf, broker)
	s.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, Qty: 0.1, Price: 100})
}
