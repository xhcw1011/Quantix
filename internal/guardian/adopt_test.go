package guardian

import (
	"testing"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
)

func TestGuardian_AdoptsLongFromPortfolio(t *testing.T) {
	cfg := ProtectionConfig{StopMode: StopPct, StopValue: 0.05}
	g := NewAdoptGuardian("ETHUSDT", cfg, 14, zap.NewNop())
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{qty: 2, avg: 100}, b, zap.NewNop())

	// Status before arming must not panic.
	if st := g.Status(); st["state"] != "arming" {
		t.Fatalf("pre-arm state = %v", st["state"])
	}

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	st := g.Status()
	if st["side"] != SideLong || st["entry"].(float64) != 100 || st["qty"].(float64) != 2 {
		t.Fatalf("should adopt long 2 @ 100, got %v", st)
	}

	// A drop through the 5% stop (95) closes the adopted size.
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 100, Low: 94, Close: 96})
	if len(b.orders) != 1 || b.orders[0].Qty != 2 {
		t.Fatalf("adopted stop should close qty 2, got %+v", b.orders)
	}
}

func TestGuardian_AdoptsShortFromPortfolio(t *testing.T) {
	cfg := ProtectionConfig{StopMode: StopPct, StopValue: 0.05}
	g := NewAdoptGuardian("ETHUSDT", cfg, 14, zap.NewNop())
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{qty: -3, avg: 100}, b, zap.NewNop())

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	if st := g.Status(); st["side"] != SideShort || st["qty"].(float64) != 3 {
		t.Fatalf("should adopt short 3, got %v", st)
	}
}

func TestGuardian_AdoptWaitsForPosition(t *testing.T) {
	cfg := ProtectionConfig{StopMode: StopPct, StopValue: 0.05}
	g := NewAdoptGuardian("ETHUSDT", cfg, 14, zap.NewNop())
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{qty: 0}, b, zap.NewNop()) // flat

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 90, Close: 100})
	if g.prot != nil {
		t.Fatal("no position -> must not arm")
	}
	if len(b.orders) != 0 {
		t.Fatal("no position -> no orders")
	}
}
