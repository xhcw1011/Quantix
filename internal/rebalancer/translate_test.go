package rebalancer

import (
	"math"
	"testing"

	"github.com/Quantix/quantix/internal/xsfunding"
)

func TestPositionsToNotional(t *testing.T) {
	// long 0.5 BTC @ 60000 = +30000 ; short 10 SOL @ 150 = -1500
	pos := []Position{
		{Symbol: "BTCUSDT", SignedQty: 0.5, Price: 60000},
		{Symbol: "SOLUSDT", SignedQty: -10, Price: 150},
		{Symbol: "ETHUSDT", SignedQty: 0, Price: 3000}, // flat → omitted
	}
	got := PositionsToNotional(pos)
	if math.Abs(got["BTCUSDT"]-30000) > 1e-9 || math.Abs(got["SOLUSDT"]+1500) > 1e-9 {
		t.Fatalf("notional wrong: %+v", got)
	}
	if _, ok := got["ETHUSDT"]; ok {
		t.Fatalf("flat position must be omitted, got %+v", got)
	}
}

func TestOrdersToTrades(t *testing.T) {
	orders := []xsfunding.Order{
		{Symbol: "BTCUSDT", Notional: 6000},  // buy 0.1 BTC @ 60000
		{Symbol: "SOLUSDT", Notional: -1500}, // sell 10 SOL @ 150
		{Symbol: "XRPUSDT", Notional: 0.4},   // dust < 1 step*price → skipped
	}
	prices := map[string]float64{"BTCUSDT": 60000, "SOLUSDT": 150, "XRPUSDT": 1}
	steps := map[string]float64{"BTCUSDT": 0.001, "SOLUSDT": 1, "XRPUSDT": 1}
	got := OrdersToTrades(orders, prices, steps)
	if len(got) != 2 {
		t.Fatalf("want 2 trades (dust skipped), got %d: %+v", len(got), got)
	}
	if got[0].Symbol != "BTCUSDT" || got[0].Side != "BUY" || math.Abs(got[0].Qty-0.1) > 1e-9 {
		t.Fatalf("BTC trade wrong: %+v", got[0])
	}
	if got[1].Symbol != "SOLUSDT" || got[1].Side != "SELL" || math.Abs(got[1].Qty-10) > 1e-9 {
		t.Fatalf("SOL trade wrong: %+v", got[1])
	}
}
