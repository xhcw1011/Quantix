package rebalancer

import (
	"context"
	"math"
	"testing"

	"github.com/Quantix/quantix/internal/exchange"
)

type fakeQuerier struct {
	ratios []exchange.PositionMarginInfo
	err    error
}

func (f *fakeQuerier) GetMarginRatios(context.Context) ([]exchange.PositionMarginInfo, error) {
	return f.ratios, f.err
}

func TestExchangeSyncer(t *testing.T) {
	q := &fakeQuerier{ratios: []exchange.PositionMarginInfo{
		{Symbol: "BTCUSDT", PositionSide: "LONG", Size: 0.5},
		{Symbol: "SOLUSDT", PositionSide: "SHORT", Size: 10}, // hedge-mode short: |Size| with side sign
		{Symbol: "ADAUSDT", PositionSide: "", Size: -100},    // one-way: signed size
		{Symbol: "XRPUSDT", PositionSide: "LONG", Size: 0},   // flat → skipped
	}}
	prices := func(s string) float64 {
		return map[string]float64{"BTCUSDT": 60000, "SOLUSDT": 150, "ADAUSDT": 0.5}[s]
	}
	sy := NewExchangeSyncer(q, prices)
	pos, err := sy.Positions(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := map[string]Position{}
	for _, p := range pos {
		got[p.Symbol] = p
	}
	if len(got) != 3 {
		t.Fatalf("flat XRP must be skipped, got %d: %+v", len(got), pos)
	}
	if math.Abs(got["BTCUSDT"].SignedQty-0.5) > 1e-9 || got["BTCUSDT"].Price != 60000 {
		t.Fatalf("BTC wrong: %+v", got["BTCUSDT"])
	}
	if math.Abs(got["SOLUSDT"].SignedQty+10) > 1e-9 { // SHORT → negative
		t.Fatalf("SOL should be short -10, got %+v", got["SOLUSDT"])
	}
	if math.Abs(got["ADAUSDT"].SignedQty+100) > 1e-9 { // one-way signed size preserved
		t.Fatalf("ADA should be -100, got %+v", got["ADAUSDT"])
	}
}
