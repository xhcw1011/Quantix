package cascade

import (
	"math"
	"testing"
)

func b(close, shock float64) Bar { return Bar{Close: close, Shock: shock, Has: true} }

// A single down-shock that bounces should open one position, hold HoldBars, and realize the
// full price move; equity (mark-to-market) must reconcile with the trade return.
func TestSimulateSingleTrade(t *testing.T) {
	times := []int64{0, 1, 2, 3, 4}
	data := map[string]Series{
		"A": {b(90, 0), b(95, 0), b(100, -2), b(105, 0), b(110, 0)}, // shock at gi2
	}
	cfg := Config{K: 1, HoldBars: 2, FracPerTrade: 1.0, MaxConcurrent: 1, CostRT: 0}
	r := Simulate(times, data, cfg)
	if len(r.Trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(r.Trades))
	}
	tr := r.Trades[0]
	if tr.Symbol != "A" || tr.EntryGi != 2 || tr.ExitGi != 4 {
		t.Fatalf("bad trade timing: %+v", tr)
	}
	if math.Abs(tr.Ret-0.10) > 1e-9 { // 110/100 - 1
		t.Fatalf("trade ret = %v, want 0.10", tr.Ret)
	}
	final := r.Equity[len(r.Equity)-1]
	if math.Abs(final-1.10) > 1e-6 { // mark-to-market must match the +10% move
		t.Fatalf("final equity = %v, want 1.10", final)
	}
}

// When more coins shock than MaxConcurrent allows, only the biggest shocks get positions.
func TestSimulateConcurrencyCap(t *testing.T) {
	times := []int64{0, 1, 2, 3}
	data := map[string]Series{
		"A": {b(100, 0), b(100, -1.5), b(100, 0), b(100, 0)}, // smaller shock
		"B": {b(100, 0), b(100, -3.0), b(100, 0), b(100, 0)}, // bigger shock
	}
	cfg := Config{K: 1, HoldBars: 1, FracPerTrade: 0.5, MaxConcurrent: 1, CostRT: 0}
	r := Simulate(times, data, cfg)
	if len(r.Trades) != 1 || r.Trades[0].Symbol != "B" {
		t.Fatalf("cap should keep only the biggest shock (B), got %+v", r.Trades)
	}
}

// Round-trip cost is deducted from the realized trade return.
func TestSimulateCost(t *testing.T) {
	times := []int64{0, 1, 2, 3, 4}
	data := map[string]Series{
		"A": {b(90, 0), b(95, 0), b(100, -2), b(105, 0), b(110, 0)},
	}
	cfg := Config{K: 1, HoldBars: 2, FracPerTrade: 1.0, MaxConcurrent: 1, CostRT: 0.02}
	r := Simulate(times, data, cfg)
	if math.Abs(r.Trades[0].Ret-0.08) > 1e-9 { // 0.10 - 0.02 cost
		t.Fatalf("net trade ret = %v, want 0.08", r.Trades[0].Ret)
	}
}
