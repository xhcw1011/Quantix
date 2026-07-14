package guardian

import (
	"testing"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
)

type gPortfolio struct{ qty, avg float64 }

func (p *gPortfolio) Cash() float64 { return 0 }
func (p *gPortfolio) Position(string) (float64, float64, bool) {
	return p.qty, p.avg, p.qty != 0
}
func (p *gPortfolio) Equity(map[string]float64) float64 { return 0 }

type gBroker struct{ orders []strategy.OrderRequest }

func (b *gBroker) PlaceOrder(req strategy.OrderRequest) string {
	b.orders = append(b.orders, req)
	return "oid"
}
func (b *gBroker) CancelOrder(string) error { return nil }

func newLongGuardian(cfg ProtectionConfig) (*Guardian, *gBroker, *strategy.Context) {
	g := NewGuardian("ETHUSDT", NewProtection(SideLong, 100, 1, cfg, 0), 14, zap.NewNop())
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{qty: 1, avg: 100}, b, zap.NewNop())
	return g, b, ctx
}

func trailCfg() ProtectionConfig {
	return ProtectionConfig{
		StopMode: StopPct, StopValue: 0.05,
		TrailEnabled: true, ActivateR: 1, TrailMode: TrailR, TrailValue: 1,
	}
}

func TestGuardian_TrailThenStopClosesExactlyOnce(t *testing.T) {
	g, b, ctx := newLongGuardian(trailCfg())
	g.OnTick(ctx, 106) // activate, stop -> 101
	g.OnTick(ctx, 110) // stop -> 105
	g.OnTick(ctx, 104) // 104 <= trailed stop 105 -> close
	g.OnTick(ctx, 103) // must NOT double-close
	if len(b.orders) != 1 {
		t.Fatalf("want exactly 1 close, got %d", len(b.orders))
	}
	if b.orders[0].Side != strategy.SideSell {
		t.Fatalf("long close must be SELL, got %v", b.orders[0].Side)
	}
	if b.orders[0].Reason == "" {
		t.Fatalf("close should carry a Reason")
	}
}

func TestGuardian_BarLowTriggersInitialStop(t *testing.T) {
	g, b, ctx := newLongGuardian(trailCfg())
	// close 99 => pnlR -0.2, trail not active; bar low 94 breaches initial stop 95.
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 94, Close: 99})
	if len(b.orders) != 1 {
		t.Fatalf("bar low below stop should close once, got %d", len(b.orders))
	}
}

func TestGuardian_TakeProfit(t *testing.T) {
	cfg := trailCfg()
	cfg.TPMode = TPR
	cfg.TPValue = 3 // tp = 100 + 3*5 = 115
	g, b, ctx := newLongGuardian(cfg)
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 116, Low: 99, Close: 114})
	if len(b.orders) != 1 {
		t.Fatalf("bar high above TP should close, got %d", len(b.orders))
	}
	if b.orders[0].Reason != "guardian_take_profit" {
		t.Fatalf("TP reason = %q", b.orders[0].Reason)
	}
}

func TestGuardian_ShortClosesWithBuy(t *testing.T) {
	cfg := trailCfg()
	g := NewGuardian("ETHUSDT", NewProtection(SideShort, 100, 1, cfg, 0), 14, zap.NewNop()) // stop 105
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{qty: -1, avg: 100}, b, zap.NewNop())
	g.OnTick(ctx, 106) // 106 >= stop 105 -> close
	if len(b.orders) != 1 || b.orders[0].Side != strategy.SideBuy {
		t.Fatalf("short close must BUY once, got %d orders", len(b.orders))
	}
}

func TestGuardian_OnFillMarksDone(t *testing.T) {
	g, b, ctx := newLongGuardian(trailCfg())
	g.OnTick(ctx, 106) // activate, stop -> 101
	g.OnTick(ctx, 100) // 100 <= 101 -> close placed
	g.OnFill(ctx, strategy.Fill{Symbol: "ETHUSDT", Qty: 1, Price: 100})
	if !g.done {
		t.Fatalf("OnFill of the protective close should mark done")
	}
	g.OnTick(ctx, 102) // no further action
	if len(b.orders) != 1 {
		t.Fatalf("no orders after done, got %d", len(b.orders))
	}
}

func TestGuardian_StatusExposesState(t *testing.T) {
	g, _, ctx := newLongGuardian(trailCfg())
	g.OnTick(ctx, 110) // activate + trail
	st := g.Status()
	if st["side"] != SideLong {
		t.Fatalf("status side = %v", st["side"])
	}
	if _, ok := st["stop"]; !ok {
		t.Fatalf("status missing stop")
	}
	if _, ok := st["pnl_r"]; !ok {
		t.Fatalf("status missing pnl_r")
	}
	if tv, ok := st["trail_active"].(bool); !ok || !tv {
		t.Fatalf("status trail_active should be true after activation")
	}
}
