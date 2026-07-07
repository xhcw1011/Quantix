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
