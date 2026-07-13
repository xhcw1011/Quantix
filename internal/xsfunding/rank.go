package xsfunding

import "sort"

// Rank sorts the eligible universe by trailing funding and returns the K
// lowest-funding symbols (to long) and K highest-funding symbols (to short).
// Returns nil, nil when fewer than 2K coins are available.
func Rank(coins []CoinState, k int) (longs, shorts []string) {
	if k <= 0 || len(coins) < 2*k {
		return nil, nil
	}
	sorted := make([]CoinState, len(coins))
	copy(sorted, coins)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TrailFunding < sorted[j].TrailFunding })
	for i := 0; i < k; i++ {
		longs = append(longs, sorted[i].Symbol)
		shorts = append(shorts, sorted[len(sorted)-1-i].Symbol)
	}
	return longs, shorts
}

// RankHysteresis is Rank with a stickiness buffer: a currently-held coin is kept if it
// is still within the top-(K+buffer) of its side (rather than churned out the moment it
// slips to rank K+1), then remaining slots fill from the ideal top-K. Damps boundary
// flicker — validated to lift 8h-cadence return and cut turnover ~½. buffer=0 ≈ Rank.
func RankHysteresis(coins []CoinState, k, buffer int, heldLongs, heldShorts []string) (longs, shorts []string) {
	if k <= 0 || len(coins) < 2*k {
		return nil, nil
	}
	sorted := make([]CoinState, len(coins))
	copy(sorted, coins)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TrailFunding < sorted[j].TrailFunding })

	pick := func(order []CoinState, held []string) []string {
		heldSet := make(map[string]bool, len(held))
		for _, s := range held {
			heldSet[s] = true
		}
		band := k + buffer
		if band > len(order) {
			band = len(order)
		}
		inBand := make(map[string]bool, band)
		for _, c := range order[:band] {
			inBand[c.Symbol] = true
		}
		out := make([]string, 0, k)
		seen := map[string]bool{}
		for _, c := range order { // keep still-in-band held coins first (best rank first)
			if len(out) >= k {
				break
			}
			if heldSet[c.Symbol] && inBand[c.Symbol] {
				out = append(out, c.Symbol)
				seen[c.Symbol] = true
			}
		}
		for _, c := range order[:k] { // fill from the ideal top-K
			if len(out) >= k {
				break
			}
			if !seen[c.Symbol] {
				out = append(out, c.Symbol)
				seen[c.Symbol] = true
			}
		}
		for _, c := range order { // safety fill (rare)
			if len(out) >= k {
				break
			}
			if !seen[c.Symbol] {
				out = append(out, c.Symbol)
				seen[c.Symbol] = true
			}
		}
		return out
	}

	longs = pick(sorted, heldLongs)
	rev := make([]CoinState, len(sorted))
	for i := range sorted {
		rev[i] = sorted[len(sorted)-1-i]
	}
	shorts = pick(rev, heldShorts)
	return longs, shorts
}
