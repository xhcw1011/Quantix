// Package rebalancer builds the live cross-sectional funding rebalancer around the
// parity-confirmed internal/xsfunding brain: assemble point-in-time universe state
// from the DB, target vs current positions, and (shadow) log the rotation.
package rebalancer

import (
	"math"
	"sort"

	"github.com/Quantix/quantix/internal/xsfunding"
)

// retVol is the stdev of daily simple returns over the last n dates ending at index ai
// (0 if too few points) — the inverse-vol sizing input.
func retVol(price map[string]float64, dates []string, ai, n int) float64 {
	var rs []float64
	for j := ai - n + 1; j <= ai; j++ {
		if j < 1 {
			continue
		}
		p0, ok0 := price[dates[j-1]]
		p1, ok1 := price[dates[j]]
		if ok0 && ok1 && p0 > 0 {
			rs = append(rs, p1/p0-1)
		}
	}
	if len(rs) < 5 {
		return 0
	}
	var m float64
	for _, r := range rs {
		m += r
	}
	m /= float64(len(rs))
	var v float64
	for _, r := range rs {
		v += (r - m) * (r - m)
	}
	return math.Sqrt(v / float64(len(rs)))
}

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
			Vol: retVol(sr.Price, dates, ai, volWin),
		})
	}
	return out
}
