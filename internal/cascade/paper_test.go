package cascade

import (
	"math"
	"testing"
)

func TestStepOpenHoldClose(t *testing.T) {
	cfg := Config{K: 2, HoldBars: 1, FracPerTrade: 1.0, MaxConcurrent: 1, CostRT: 0, WickMin: 0.5}
	st := PaperState{}

	// tick 1: A shocks with confirmed recovery → opens a position at 100
	st, ev := Step(st, []Tick{{Symbol: "A", Close: 100, Shock: -3, Wick: 0.6}}, 0, 1, cfg)
	if len(st.Positions) != 1 || st.Positions[0].Symbol != "A" || len(ev.Opened) != 1 {
		t.Fatalf("tick1 should open A, got %+v", st.Positions)
	}

	// tick 2: hold elapsed (exitTs=1) → close at 110 for +10%
	st, ev = Step(st, []Tick{{Symbol: "A", Close: 110, Shock: 0, Wick: 0.5}}, 1, 1, cfg)
	if len(st.Positions) != 0 || len(st.Trades) != 1 || len(ev.Closed) != 1 {
		t.Fatalf("tick2 should close A, got pos=%+v trades=%+v", st.Positions, st.Trades)
	}
	if math.Abs(st.Trades[0].Ret-0.10) > 1e-9 {
		t.Fatalf("trade ret = %v, want 0.10", st.Trades[0].Ret)
	}
	if math.Abs(st.Equity-1.10) > 1e-6 {
		t.Fatalf("equity = %v, want 1.10", st.Equity)
	}
}

func TestStepWickAndShockGate(t *testing.T) {
	cfg := Config{K: 2, HoldBars: 1, FracPerTrade: 1.0, MaxConcurrent: 5, CostRT: 0, WickMin: 0.5}
	st := PaperState{}
	st, _ = Step(st, []Tick{
		{Symbol: "A", Close: 100, Shock: -3, Wick: 0.2}, // shock ok but wick too low (falling knife)
		{Symbol: "B", Close: 100, Shock: -1, Wick: 0.9}, // wick ok but shock too small
		{Symbol: "C", Close: 100, Shock: -3, Wick: 0.7}, // both pass → opens
	}, 0, 1, cfg)
	if len(st.Positions) != 1 || st.Positions[0].Symbol != "C" {
		t.Fatalf("only C should open (A fails wick, B fails shock), got %+v", st.Positions)
	}
}

func TestStepIdempotentGuard(t *testing.T) {
	cfg := Config{K: 2, HoldBars: 1, FracPerTrade: 1.0, MaxConcurrent: 1, WickMin: 0.5}
	st := PaperState{Equity: 1.0, LastTs: 100}
	st2, ev := Step(st, []Tick{{Symbol: "A", Close: 100, Shock: -3, Wick: 0.9}}, 100, 1, cfg)
	if len(st2.Positions) != 0 || len(ev.Opened) != 0 {
		t.Fatalf("a tick at/<= LastTs must be ignored, got %+v", st2.Positions)
	}
}
