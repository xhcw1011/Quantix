package rebalancer

import "github.com/Quantix/quantix/internal/xsfunding"

// Config parameterizes a rebalance plan.
type Config struct {
	K                int
	GrossFrac        float64
	MinDaysListed    int
	MinVolume        float64
	W                int // trailing funding window (dates)
	VolWin           int // trailing volume window (dates)
	MinOrder         float64
	Capital          float64
	MaxPerCoinFrac   float64 // cap per-coin |notional| at this fraction of gross (0 = no cap)
	HysteresisBuffer int     // keep held coins within top-(K+buffer) to damp flicker (0 = off)
}

// Plan is one rotation's output: the target book, the notional deltas vs current, and
// the concrete step-rounded trades.
type Plan struct {
	Targets []xsfunding.Target
	Orders  []xsfunding.Order
	Trades  []Trade
}

// PlanRotation computes the full rotation (no side effects). `current` is signed
// notional per symbol; `steps` is each symbol's lot step. Returns an empty Plan when
// the eligible universe is too small to form 2K positions.
func PlanRotation(series map[string]Series, dates []string, asOf string, current map[string]float64, cfg Config, steps map[string]float64) Plan {
	coins := BuildStates(series, dates, asOf, cfg.W, cfg.VolWin)
	elig := xsfunding.Eligible(coins, cfg.MinDaysListed, cfg.MinVolume)
	var longs, shorts []string
	if cfg.HysteresisBuffer > 0 {
		var heldL, heldS []string // derive current book from signed notional
		for s, n := range current {
			if n > 0 {
				heldL = append(heldL, s)
			} else if n < 0 {
				heldS = append(heldS, s)
			}
		}
		longs, shorts = xsfunding.RankHysteresis(elig, cfg.K, cfg.HysteresisBuffer, heldL, heldS)
	} else {
		longs, shorts = xsfunding.Rank(elig, cfg.K)
	}
	if longs == nil {
		return Plan{}
	}
	vol := map[string]float64{}
	for _, c := range coins {
		vol[c.Symbol] = c.Vol
	}
	targets := xsfunding.BuildTargetsRP(longs, shorts, cfg.Capital, cfg.GrossFrac, vol, cfg.MaxPerCoinFrac)
	orders := xsfunding.Deltas(current, targets, cfg.MinOrder)
	prices := map[string]float64{}
	for _, c := range coins {
		prices[c.Symbol] = c.Price
	}
	return Plan{Targets: targets, Orders: orders, Trades: OrdersToTrades(orders, prices, steps)}
}
