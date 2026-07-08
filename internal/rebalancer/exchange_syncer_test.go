package rebalancer

import (
	"context"
	"math"
	"testing"

	"github.com/Quantix/quantix/internal/exchange"
)

type fakeQuerier struct {
	positions []exchange.PositionInfo
	err       error
}

func (f *fakeQuerier) GetPositions(context.Context) ([]exchange.PositionInfo, error) {
	return f.positions, f.err
}

func TestExchangeSyncer(t *testing.T) {
	q := &fakeQuerier{positions: []exchange.PositionInfo{
		{Symbol: "BTCUSDT", PositionSide: "", Amt: 0.5},       // one-way long: signed +
		{Symbol: "SOLUSDT", PositionSide: "", Amt: -10},       // one-way short: signed −
		{Symbol: "ADAUSDT", PositionSide: "SHORT", Amt: -100}, // hedge short: also signed −
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
		t.Fatalf("expected 3 positions, got %d: %+v", len(got), pos)
	}
	if math.Abs(got["BTCUSDT"].SignedQty-0.5) > 1e-9 || got["BTCUSDT"].Price != 60000 {
		t.Fatalf("BTC wrong: %+v", got["BTCUSDT"])
	}
	if math.Abs(got["SOLUSDT"].SignedQty+10) > 1e-9 { // short → negative
		t.Fatalf("SOL should be short -10, got %+v", got["SOLUSDT"])
	}
	if math.Abs(got["ADAUSDT"].SignedQty+100) > 1e-9 {
		t.Fatalf("ADA should be -100, got %+v", got["ADAUSDT"])
	}
}
