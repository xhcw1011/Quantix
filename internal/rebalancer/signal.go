// Package rebalancer builds the live cross-sectional funding rebalancer around the
// parity-confirmed internal/xsfunding brain: assemble point-in-time universe state
// from the DB, target vs current positions, and (shadow) log the rotation.
package rebalancer

import (
	"sort"

	"github.com/Quantix/quantix/internal/xsfunding"
)

// Series is one coin's daily history (date string -> value) plus its first listed date.
type Series struct {
	Price   map[string]float64
	Volume  map[string]float64
	Funding map[string]float64
	First   string // earliest date the coin has data (for DaysListed)
}

// BuildStates assembles the CoinState for each coin as of asOf: trailing funding over
// the last W dates, trailing volume over the last volWin dates, current price, and
// days listed. Coins without a price on asOf are skipped. `dates` must be sorted asc.
func BuildStates(series map[string]Series, dates []string, asOf string, W, volWin int) []xsfunding.CoinState {
	idx := make(map[string]int, len(dates))
	for i, d := range dates {
		idx[d] = i
	}
	ai, ok := idx[asOf]
	if !ok {
		return nil
	}
	syms := make([]string, 0, len(series))
	for s := range series {
		syms = append(syms, s)
	}
	sort.Strings(syms)

	var out []xsfunding.CoinState
	for _, s := range syms {
		sr := series[s]
		price, ok := sr.Price[asOf]
		if !ok || price <= 0 {
			continue
		}
		var tf float64
		for j := ai - W + 1; j <= ai; j++ {
			if j >= 0 {
				tf += sr.Funding[dates[j]]
			}
		}
		var tv float64
		var n int
		for j := ai - volWin + 1; j <= ai; j++ {
			if j >= 0 {
				if v, ok := sr.Volume[dates[j]]; ok {
					tv += v
					n++
				}
			}
		}
		if n > 0 {
			tv /= float64(n)
		}
		days := ai
		if fi, ok := idx[sr.First]; ok {
			days = ai - fi
		}
		out = append(out, xsfunding.CoinState{
			Symbol: s, TrailFunding: tf, Price: price, TrailVolume: tv, DaysListed: days,
		})
	}
	return out
}
