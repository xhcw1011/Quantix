package rebalancer

import (
	"math"
	"testing"
)

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
