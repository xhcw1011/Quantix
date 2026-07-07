package rebalancer

import "testing"

func TestPlanRotation(t *testing.T) {
	// 4 coins on d1; K=1 → long lowest-funding A, short highest-funding D. Flat start.
	mk := func(fund float64) Series {
		return Series{
			Price:   map[string]float64{"d0": 100, "d1": 100},
			Volume:  map[string]float64{"d0": 5e6, "d1": 5e6},
			Funding: map[string]float64{"d0": fund, "d1": fund},
			First:   "d0",
		}
	}
	series := map[string]Series{"A": mk(-0.02), "B": mk(0.00), "C": mk(0.01), "D": mk(0.05)}
	cfg := Config{K: 1, GrossFrac: 1.0, MinDaysListed: 1, MinVolume: 1e6, W: 2, VolWin: 2, MinOrder: 1, Capital: 10000}
	steps := map[string]float64{"A": 1, "B": 1, "C": 1, "D": 1}
	plan := PlanRotation(series, []string{"d0", "d1"}, "d1", map[string]float64{}, cfg, steps)
	byS := map[string]float64{}
	for _, tg := range plan.Targets {
		byS[tg.Symbol] = tg.Notional
	}
	if byS["A"] != 5000 || byS["D"] != -5000 {
		t.Fatalf("targets wrong: %+v", plan.Targets)
	}
	tr := map[string]Trade{}
	for _, x := range plan.Trades {
		tr[x.Symbol] = x
	}
	if tr["A"].Side != "BUY" || tr["A"].Qty != 50 || tr["D"].Side != "SELL" || tr["D"].Qty != 50 {
		t.Fatalf("trades wrong: %+v", plan.Trades)
	}
}
