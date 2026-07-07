package xsfunding

import (
	"math"
	"sort"
)

// Deltas returns the orders that move current signed notional to the target book:
// per target, trade (target − current); symbols held but no longer targeted are
// closed. Deltas with |notional| < minOrder are skipped (no dust trades). Output is
// sorted by symbol for determinism.
func Deltas(current map[string]float64, targets []Target, minOrder float64) []Order {
	targeted := make(map[string]bool, len(targets))
	var out []Order
	for _, t := range targets {
		targeted[t.Symbol] = true
		d := t.Notional - current[t.Symbol]
		if math.Abs(d) >= minOrder {
			out = append(out, Order{Symbol: t.Symbol, Notional: d})
		}
	}
	for s, cur := range current {
		if !targeted[s] && math.Abs(cur) >= minOrder {
			out = append(out, Order{Symbol: s, Notional: -cur})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}

// Rebalance chains the pure pieces: filter to the point-in-time universe, rank by
// funding, build the dollar-neutral target book, and diff against current positions.
// Returns nil when the eligible universe is too small to form 2K positions (no rotation).
func Rebalance(coins []CoinState, current map[string]float64, capital float64, cfg Config, minOrder float64) []Order {
	longs, shorts := Rank(Eligible(coins, cfg.MinDaysListed, cfg.MinVolume), cfg.K)
	if longs == nil {
		return nil
	}
	return Deltas(current, BuildTargets(longs, shorts, capital, cfg.GrossFrac), minOrder)
}
