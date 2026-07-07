package rebalancer

import (
	"math"
	"testing"
)

func TestCrossCostBpHalfSpreadOnly(t *testing.T) {
	// One deep level at 100, mid 99.9 → a $1000 buy fills entirely at 100.
	// cost = (100-99.9)/99.9 = 10.01 bp (just the half-spread).
	got := CrossCostBp([]Level{{Price: 100, Qty: 100}}, 99.9, 1000)
	if math.Abs(got-10.01) > 0.05 {
		t.Fatalf("half-spread cost = %.2f bp, want ~10.01", got)
	}
}

func TestCrossCostBpWalksBookForImpact(t *testing.T) {
	// Thin top (10@100 = $1000), then a jump to 110. Buy $1500 walks into 110.
	// filled: $1000@100 (10 qty) + $500@110 (4.5454 qty) → avg 103.125.
	// slippage = (103.125-99.5)/99.5 = 364.3 bp.
	got := CrossCostBp([]Level{{Price: 100, Qty: 10}, {Price: 110, Qty: 100}}, 99.5, 1500)
	if math.Abs(got-364.3) > 1.0 {
		t.Fatalf("impact cost = %.1f bp, want ~364.3", got)
	}
}

func TestCrossCostBpBookExhausted(t *testing.T) {
	// Only $500 of depth for a $1000 order → remainder fills at the worst price (100),
	// so the whole order effectively fills at ~100. cost = (100-99.9)/99.9 ≈ 10 bp.
	got := CrossCostBp([]Level{{Price: 100, Qty: 5}}, 99.9, 1000)
	if got <= 0 {
		t.Fatalf("exhausted book must still return a positive cost, got %v", got)
	}
}
