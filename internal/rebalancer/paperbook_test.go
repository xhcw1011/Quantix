package rebalancer

import (
	"math"
	"testing"

	"github.com/Quantix/quantix/internal/strategy"
)

func TestPaperBookFillsAndPositions(t *testing.T) {
	b := NewPaperBook(0.0005) // 5bp per-side fee
	b.SetPrice("BTCUSDT", 60000)
	b.SetPrice("SOLUSDT", 150)

	b.PlaceOrder(strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy, Type: strategy.OrderMarket, Qty: 0.1})
	b.PlaceOrder(strategy.OrderRequest{Symbol: "SOLUSDT", Side: strategy.SideSell, Type: strategy.OrderMarket, Qty: 10})

	pos := map[string]Position{}
	for _, p := range b.Positions() {
		pos[p.Symbol] = p
	}
	if math.Abs(pos["BTCUSDT"].SignedQty-0.1) > 1e-9 {
		t.Fatalf("BTC qty = %v, want 0.1", pos["BTCUSDT"].SignedQty)
	}
	if math.Abs(pos["SOLUSDT"].SignedQty+10) > 1e-9 {
		t.Fatalf("SOL qty = %v, want -10", pos["SOLUSDT"].SignedQty)
	}
	// realized fee = (0.1*60000 + 10*150) * 0.0005 = (6000 + 1500)*0.0005 = 3.75
	if math.Abs(b.RealizedCost()-3.75) > 1e-9 {
		t.Fatalf("realized cost = %v, want 3.75", b.RealizedCost())
	}
}

func TestPaperBookNetsAndClosesToFlat(t *testing.T) {
	b := NewPaperBook(0)
	b.SetPrice("BTCUSDT", 60000)
	b.PlaceOrder(strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy, Type: strategy.OrderMarket, Qty: 0.1})
	b.PlaceOrder(strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideSell, Type: strategy.OrderMarket, Qty: 0.1})
	if len(b.Positions()) != 0 {
		t.Fatalf("flat book should report no positions, got %+v", b.Positions())
	}
}
