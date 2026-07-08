package rebalancer

import (
	"math"
	"testing"

	"github.com/Quantix/quantix/internal/strategy"
)

func mkBuy(sym string, qty float64) strategy.OrderRequest {
	return strategy.OrderRequest{Symbol: sym, Side: strategy.SideBuy, Type: strategy.OrderMarket, Qty: qty}
}

func rotSeries() map[string]Series {
	mk := func(fund float64) Series {
		return Series{
			Price:   map[string]float64{"d0": 100, "d1": 100},
			Volume:  map[string]float64{"d0": 5e6, "d1": 5e6},
			Funding: map[string]float64{"d0": fund, "d1": fund},
			First:   "d0",
		}
	}
	return map[string]Series{"A": mk(-0.02), "B": mk(0.00), "C": mk(0.01), "D": mk(0.05)}
}

func TestExecuteRotationFromFlat(t *testing.T) {
	series := rotSeries()
	cfg := Config{K: 1, GrossFrac: 1.0, MinDaysListed: 1, MinVolume: 1e6, W: 2, VolWin: 2, MinOrder: 1, Capital: 10000}
	book := NewPaperBook(0)
	// no steps → qty = notional/price = 5000/100 = 50 per leg
	p1 := ExecuteRotation(series, []string{"d0", "d1"}, "d1", cfg, nil, book, book)
	if len(p1.Trades) != 2 {
		t.Fatalf("want 2 opening trades, got %d: %+v", len(p1.Trades), p1.Trades)
	}
	pos := map[string]float64{}
	for _, p := range book.Positions() {
		pos[p.Symbol] = p.SignedQty
	}
	if math.Abs(pos["A"]-50) > 1e-9 || math.Abs(pos["D"]+50) > 1e-9 {
		t.Fatalf("book should be long A 50 / short D -50, got %+v", pos)
	}
}

func TestExecuteRotationClosesDroppedSymbol(t *testing.T) {
	// A held position in a symbol NOT in the current universe (no price on asOf) must
	// still be closed, priced off the book's last price — else it lingers forever.
	series := rotSeries()
	cfg := Config{K: 1, GrossFrac: 1.0, MinDaysListed: 1, MinVolume: 1e6, W: 2, VolWin: 2, MinOrder: 1, Capital: 10000}
	book := NewPaperBook(0)
	book.SetPrice("GHOST", 50)
	book.PlaceOrder(mkBuy("GHOST", 10)) // book holds +10 GHOST @ 50, not in series
	ExecuteRotation(series, []string{"d0", "d1"}, "d1", cfg, nil, book, book)
	pos := map[string]float64{}
	for _, p := range book.Positions() {
		pos[p.Symbol] = p.SignedQty
	}
	if _, held := pos["GHOST"]; held {
		t.Fatalf("GHOST (dropped from universe) must be closed, still held: %+v", pos)
	}
	if math.Abs(pos["A"]-50) > 1e-9 || math.Abs(pos["D"]+50) > 1e-9 {
		t.Fatalf("expected long A 50 / short D -50, got %+v", pos)
	}
}

type captureSink struct{ placed []strategy.OrderRequest }

func (c *captureSink) PlaceOrder(r strategy.OrderRequest) string { c.placed = append(c.placed, r); return "id" }
func (c *captureSink) CancelOrder(string) error                  { return nil }

func TestExecuteRotationSink(t *testing.T) {
	series := rotSeries()
	cfg := Config{K: 1, GrossFrac: 1.0, MinDaysListed: 1, MinVolume: 1e6, W: 2, VolWin: 2, MinOrder: 1, Capital: 10000}
	priceFn := func(string) float64 { return 100 }
	sink := &captureSink{}
	plan := ExecuteRotationSink(series, []string{"d0", "d1"}, "d1", cfg, priceFn, map[string]float64{}, sink, false)
	if len(plan.Targets) != 2 || len(sink.placed) != 2 {
		t.Fatalf("expected 2 targets + 2 orders placed, got %d/%d", len(plan.Targets), len(sink.placed))
	}
	got := map[string]strategy.OrderRequest{}
	for _, r := range sink.placed {
		got[r.Symbol] = r
	}
	if got["A"].Side != strategy.SideBuy || math.Abs(got["A"].Qty-50) > 1e-9 {
		t.Fatalf("A should be buy 50, got %+v", got["A"])
	}
	if got["D"].Side != strategy.SideSell || math.Abs(got["D"].Qty-50) > 1e-9 {
		t.Fatalf("D should be sell 50, got %+v", got["D"])
	}
}

func TestExecuteRotationNoChurnWhenAtTarget(t *testing.T) {
	series := rotSeries()
	cfg := Config{K: 1, GrossFrac: 1.0, MinDaysListed: 1, MinVolume: 1e6, W: 2, VolWin: 2, MinOrder: 1, Capital: 10000}
	book := NewPaperBook(0)
	ExecuteRotation(series, []string{"d0", "d1"}, "d1", cfg, nil, book, book) // reach target
	p2 := ExecuteRotation(series, []string{"d0", "d1"}, "d1", cfg, nil, book, book)
	if len(p2.Trades) != 0 {
		t.Fatalf("already at target → expected no trades, got %+v", p2.Trades)
	}
}
