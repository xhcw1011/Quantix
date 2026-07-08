package xsfunding

import "math"

// StepPnL is one rebalance period's return as a fraction of capital: for each target
// position, price return weighted by its signed weight, plus funding (a long PAYS
// funding, a short COLLECTS it), minus per-side fees on the traded turnover
// (prevNotional → targets). fwdRet/fwdFunding are the forward price return and
// forward summed funding over the holding period, per symbol.
func StepPnL(targets []Target, prevNotional, fwdRet, fwdFunding map[string]float64, capital, feeRate float64) float64 {
	var pnl float64
	targeted := make(map[string]bool, len(targets))
	for _, t := range targets {
		w := t.Notional / capital // signed weight
		pnl += w * fwdRet[t.Symbol]
		pnl += -w * fwdFunding[t.Symbol] // long (w>0) pays funding; short (w<0) collects
		targeted[t.Symbol] = true
	}
	var turnover float64
	for _, t := range targets {
		turnover += math.Abs(t.Notional - prevNotional[t.Symbol])
	}
	for s, cur := range prevNotional {
		if !targeted[s] {
			turnover += math.Abs(cur)
		}
	}
	return pnl - turnover/capital*feeRate
}

// Period is one rebalance point's inputs for the in-memory backtest: the universe
// state, and the forward price return + forward summed funding per symbol over the
// following holding period.
type Period struct {
	Coins      []CoinState
	FwdRet     map[string]float64
	FwdFunding map[string]float64
}

// RunBacktest replays periods through the same pure pieces the live path uses,
// compounding StepPnL. Returns final equity (start 1.0) and the per-period returns.
// A period whose eligible universe is too small contributes 0 and holds flat.
func RunBacktest(periods []Period, capital float64, cfg Config, minOrder float64) (equity float64, steps []float64) {
	equity = 1.0
	prev := map[string]float64{}
	for _, p := range periods {
		elig := Eligible(p.Coins, cfg.MinDaysListed, cfg.MinVolume)
		longs, shorts := Rank(elig, cfg.K)
		if longs == nil {
			steps = append(steps, 0)
			continue
		}
		vol := make(map[string]float64, len(elig))
		for _, c := range elig {
			vol[c.Symbol] = c.Vol
		}
		targets := BuildTargetsRP(longs, shorts, capital, cfg.GrossFrac, vol, cfg.MaxPerCoinFrac)
		pnl := StepPnL(targets, prev, p.FwdRet, p.FwdFunding, capital, cfg.FeeRate)
		equity *= 1 + pnl
		steps = append(steps, pnl)
		prev = map[string]float64{}
		for _, t := range targets {
			prev[t.Symbol] = t.Notional
		}
	}
	return equity, steps
}
