// Package xsfunding implements the pure core of the cross-sectional funding
// strategy — the deterministic "brain" of a new portfolio rebalancer: rank a
// mid-cap perp universe by funding, long the lowest / short the highest,
// dollar-neutral, rebalanced to target. No I/O; validated against
// scripts/xsmom_funding.py before any exchange wiring. The live runner
// (scheduler + position read + ORG-gated order placement + its own pool) is a
// follow-on that consumes these functions.
package xsfunding

// CoinState is one coin's inputs at a rebalance.
type CoinState struct {
	Symbol       string
	TrailFunding float64 // summed funding over the lookback window
	Price        float64
	TrailVolume  float64 // avg daily $ volume (liquidity filter)
	DaysListed   int     // for the point-in-time universe
}

// Config holds the strategy parameters.
type Config struct {
	K             int     // positions per side
	GrossFrac     float64 // gross exposure = capital × GrossFrac
	MinDaysListed int     // point-in-time listing filter
	MinVolume     float64 // liquidity floor ($ avg daily volume)
	FeeRate       float64 // per-side fee (backtest)
}

// Target is a per-symbol target position (signed notional; + long, − short).
type Target struct {
	Symbol   string
	Notional float64
}

// Order is a per-symbol trade to place (signed notional; + buy, − sell).
type Order struct {
	Symbol   string
	Notional float64
}

// Eligible returns the point-in-time tradeable universe: listed long enough, liquid
// enough, and priced.
func Eligible(coins []CoinState, minDaysListed int, minVolume float64) []CoinState {
	out := make([]CoinState, 0, len(coins))
	for _, c := range coins {
		if c.DaysListed >= minDaysListed && c.TrailVolume >= minVolume && c.Price > 0 {
			out = append(out, c)
		}
	}
	return out
}
