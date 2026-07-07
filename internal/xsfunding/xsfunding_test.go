package xsfunding

import (
	"math"
	"testing"
)

func cs(sym string, funding, price, vol float64, days int) CoinState {
	return CoinState{Symbol: sym, TrailFunding: funding, Price: price, TrailVolume: vol, DaysListed: days}
}

func TestEligible(t *testing.T) {
	coins := []CoinState{
		cs("OK", 0.01, 100, 5e6, 30),   // passes
		cs("NEW", 0.01, 100, 5e6, 5),   // too new (days < 20)
		cs("THIN", 0.01, 100, 1e5, 30), // too illiquid (vol < 1e6)
		cs("ZERO", 0.01, 0, 5e6, 30),   // no price
	}
	got := Eligible(coins, 20, 1e6)
	if len(got) != 1 || got[0].Symbol != "OK" {
		t.Fatalf("expected only OK eligible, got %+v", got)
	}
}

func TestRank(t *testing.T) {
	coins := []CoinState{
		cs("A", -0.02, 100, 5e6, 30), // lowest funding
		cs("B", 0.00, 100, 5e6, 30),
		cs("C", 0.01, 100, 5e6, 30),
		cs("D", 0.05, 100, 5e6, 30), // highest funding
	}
	longs, shorts := Rank(coins, 1)
	if len(longs) != 1 || longs[0] != "A" {
		t.Fatalf("long should be lowest-funding A, got %v", longs)
	}
	if len(shorts) != 1 || shorts[0] != "D" {
		t.Fatalf("short should be highest-funding D, got %v", shorts)
	}
	if l, s := Rank(coins, 3); l != nil || s != nil {
		t.Fatalf("fewer than 2K coins should yield nil, got %v %v", l, s)
	}
}

func TestBuildTargets(t *testing.T) {
	targets := BuildTargets([]string{"A", "B"}, []string{"C", "D"}, 10000, 1.0)
	if len(targets) != 4 {
		t.Fatalf("expected 4 targets, got %d", len(targets))
	}
	byS := map[string]float64{}
	var net float64
	for _, tg := range targets {
		byS[tg.Symbol] = tg.Notional
		net += tg.Notional
	}
	if byS["A"] != 2500 || byS["C"] != -2500 {
		t.Fatalf("expected ±2500 per position, got %+v", byS)
	}
	if math.Abs(net) > 1e-9 {
		t.Fatalf("book must be dollar-neutral, net=%v", net)
	}
}

func TestDeltas(t *testing.T) {
	current := map[string]float64{"A": 2500, "B": -2500, "C": 1000}
	targets := []Target{{Symbol: "A", Notional: 2500}, {Symbol: "D", Notional: -2500}}
	got := Deltas(current, targets, 1.0)
	want := map[string]float64{"D": -2500, "B": 2500, "C": -1000}
	if len(got) != 3 {
		t.Fatalf("expected 3 orders, got %d: %+v", len(got), got)
	}
	for _, o := range got {
		if want[o.Symbol] != o.Notional {
			t.Fatalf("order %s = %v, want %v", o.Symbol, o.Notional, want[o.Symbol])
		}
	}
	if got[0].Symbol != "B" || got[1].Symbol != "C" || got[2].Symbol != "D" {
		t.Fatalf("orders must be sorted by symbol, got %+v", got)
	}
}

func TestRebalanceOrchestrator(t *testing.T) {
	coins := []CoinState{
		cs("A", -0.02, 100, 5e6, 30), // long
		cs("B", 0.00, 100, 5e6, 30),
		cs("C", 0.01, 100, 5e6, 30),
		cs("D", 0.05, 100, 5e6, 30),  // short
		cs("NEW", -0.9, 100, 5e6, 5), // extreme funding but too new → excluded
	}
	cfg := Config{K: 1, GrossFrac: 1.0, MinDaysListed: 20, MinVolume: 1e6}
	orders := Rebalance(coins, map[string]float64{}, 10000, cfg, 1.0)
	got := map[string]float64{}
	for _, o := range orders {
		got[o.Symbol] = o.Notional
	}
	if got["A"] != 5000 || got["D"] != -5000 || len(orders) != 2 {
		t.Fatalf("expected long A +5000 / short D -5000, got %+v", got)
	}
}

func TestStepPnL(t *testing.T) {
	targets := []Target{{Symbol: "A", Notional: 5000}, {Symbol: "D", Notional: -5000}} // capital 10000
	fwdRet := map[string]float64{"A": 0.10, "D": -0.10}
	fwdFund := map[string]float64{"A": -0.01, "D": 0.02}
	// price 0.10; funding 0.005+0.010=0.015; fees 10000/10000*0.0005=0.0005; total 0.1145
	got := StepPnL(targets, map[string]float64{}, fwdRet, fwdFund, 10000, 0.0005)
	if math.Abs(got-0.1145) > 1e-9 {
		t.Fatalf("StepPnL = %v, want 0.1145", got)
	}
}

func TestRunBacktest(t *testing.T) {
	cfg := Config{K: 1, GrossFrac: 1.0, MinDaysListed: 20, MinVolume: 1e6, FeeRate: 0.0}
	base := []CoinState{
		cs("A", -0.02, 100, 5e6, 30), cs("B", 0.00, 100, 5e6, 30),
		cs("C", 0.01, 100, 5e6, 30), cs("D", 0.05, 100, 5e6, 30),
	}
	period := Period{
		Coins:      base,
		FwdRet:     map[string]float64{"A": 0.10, "D": -0.10},
		FwdFunding: map[string]float64{"A": 0.0, "D": 0.0},
	}
	eq, steps := RunBacktest([]Period{period, period}, 10000, cfg, 1.0)
	if len(steps) != 2 || math.Abs(steps[0]-0.10) > 1e-9 {
		t.Fatalf("step pnl = %v, want 0.10", steps)
	}
	if math.Abs(eq-1.21) > 1e-9 {
		t.Fatalf("equity = %v, want 1.21", eq)
	}
}
