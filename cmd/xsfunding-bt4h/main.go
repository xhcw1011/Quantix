// Command xsfunding-bt4h backtests the cross-sectional funding strategy at INTRADAY
// (4h) resolution from the DB, to validate faster rebalance cadences (4h/8h/daily) that
// the daily backtest cannot test. Bar-indexed; funding bucketed into 4h bars. Optional
// hysteresis (rank buffer) to gauge boundary-flicker churn at high frequency.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/rebalancer"
	"github.com/Quantix/quantix/internal/xsfunding"
)

func main() {
	interval := flag.String("interval", "4h", "kline interval")
	barsPerDay := flag.Int("bpd", 6, "bars per day (4h=6)")
	ldays := flag.Int("l", 14, "momentum/return lookback in DAYS (unused for funding factor)")
	wdays := flag.Int("w", 7, "trailing funding window in DAYS")
	k := flag.Int("k", 5, "positions per side")
	rebBars := flag.Int("reb", 2, "rebalance cadence in BARS (4h=1, 8h=2, 24h=6)")
	cost := flag.Float64("cost", 0.0010, "per-side cost")
	cap := flag.Float64("cap", 0.15, "per-coin cap frac")
	hyst := flag.Int("hyst", 0, "hysteresis buffer (held coin kept if within top-(K+hyst); 0=off)")
	flag.Parse()
	L := *ldays * *barsPerDay
	W := *wdays * *barsPerDay

	log := zap.NewNop()
	cfg, _ := config.Load("config/config.yaml")
	ctx := context.Background()
	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		fmt.Println("db:", err)
		return
	}
	defer store.Close()

	start, _ := time.Parse("2006-01-02", "2024-07-01")
	end := time.Now().UTC().AddDate(0, 0, 1)

	// load bar-indexed px/qv + bucketed funding
	px := map[string]map[int64]float64{}
	qv := map[string]map[int64]float64{}
	fund := map[string]map[int64]float64{}
	first := map[string]int64{}
	tset := map[int64]bool{}
	barMs := int64(24/(*barsPerDay)) * 3600 * 1000
	loaded := 0
	for _, s := range rebalancer.DefaultUniverse {
		kl, err := store.GetKlinesBetween(ctx, s, *interval, start, end)
		if err != nil || len(kl) == 0 {
			continue
		}
		px[s], qv[s], fund[s] = map[int64]float64{}, map[int64]float64{}, map[int64]float64{}
		first[s] = kl[0].OpenTime.UnixMilli()
		for _, b := range kl {
			t := b.OpenTime.UnixMilli()
			px[s][t], qv[s][t] = b.Close, b.QuoteVolume
			tset[t] = true
		}
		fr, _ := store.GetFunding(ctx, s)
		for _, r := range fr {
			bar := r.Time.UnixMilli() / barMs * barMs // truncate to bar boundary
			fund[s][bar] += r.Rate
		}
		loaded++
	}
	times := make([]int64, 0, len(tset))
	for t := range tset {
		times = append(times, t)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	idx := map[int64]int{}
	for i, t := range times {
		idx[t] = i
	}
	firstIdx := map[string]int{}
	for s, ft := range first {
		if i, ok := idx[ft]; ok {
			firstIdx[s] = i
		}
	}

	trailFund := func(s string, i, n int) float64 {
		var v float64
		for j := i - n + 1; j <= i; j++ {
			if j >= 0 {
				v += fund[s][times[j]]
			}
		}
		return v
	}
	retVol := func(s string, i int) float64 {
		var rs []float64
		for j := i - *barsPerDay*30 + 1; j <= i; j++ {
			if j >= 1 {
				p0, ok0 := px[s][times[j-1]]
				p1, ok1 := px[s][times[j]]
				if ok0 && ok1 && p0 > 0 {
					rs = append(rs, p1/p0-1)
				}
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
		var vv float64
		for _, r := range rs {
			vv += (r - m) * (r - m)
		}
		return math.Sqrt(vv / float64(len(rs)))
	}

	equity := 1.0
	var steps []float64
	prev := map[string]float64{}
	var heldL, heldS []string // for hysteresis
	var turnoverTotal float64
	for i := L + 1; i+*rebBars < len(times); i += *rebBars {
		var coins []xsfunding.CoinState
		for s := range px {
			p, ok := px[s][times[i]]
			if !ok || p <= 0 {
				continue
			}
			if _, hasF := fund[s][times[max(0, i-W)]]; !hasF {
				continue
			}
			coins = append(coins, xsfunding.CoinState{
				Symbol: s, TrailFunding: trailFund(s, i, W), Price: p,
				TrailVolume: 1e9, DaysListed: (i - firstIdx[s]), Vol: retVol(s, i),
			})
		}
		elig := xsfunding.Eligible(coins, L, 1.0)
		longs, shorts := selectHyst(elig, *k, *hyst, heldL, heldS)
		if longs == nil {
			steps = append(steps, 0)
			continue
		}
		heldL, heldS = longs, shorts
		vol := map[string]float64{}
		for _, c := range elig {
			vol[c.Symbol] = c.Vol
		}
		targets := xsfunding.BuildTargetsRP(longs, shorts, 10000, 1.0, vol, *cap)
		fwdRet := map[string]float64{}
		fwdFund := map[string]float64{}
		for _, t := range targets {
			s := t.Symbol
			if pn, ok := px[s][times[i]]; ok && pn > 0 {
				if pf, ok2 := px[s][times[i+*rebBars]]; ok2 {
					fwdRet[s] = pf/pn - 1
				}
			}
			var ff float64
			for j := i + 1; j <= i+*rebBars && j < len(times); j++ {
				ff += fund[s][times[j]]
			}
			fwdFund[s] = ff
		}
		// turnover
		var tv float64
		tset2 := map[string]bool{}
		for _, t := range targets {
			tv += math.Abs(t.Notional - prev[t.Symbol])
			tset2[t.Symbol] = true
		}
		for s, c := range prev {
			if !tset2[s] {
				tv += math.Abs(c)
			}
		}
		turnoverTotal += tv / 10000
		pnl := xsfunding.StepPnL(targets, prev, fwdRet, fwdFund, 10000, *cost)
		equity *= 1 + pnl
		steps = append(steps, pnl)
		prev = map[string]float64{}
		for _, t := range targets {
			prev[t.Symbol] = t.Notional
		}
	}

	// stats
	n := len(steps)
	if n < 2 {
		fmt.Println("数据不足")
		return
	}
	var mean float64
	for _, s := range steps {
		mean += s
	}
	mean /= float64(n)
	var sd float64
	for _, s := range steps {
		sd += (s - mean) * (s - mean)
	}
	sd = math.Sqrt(sd / float64(n))
	ppy := float64(365**barsPerDay) / float64(*rebBars)
	sharpe := 0.0
	if sd > 0 {
		sharpe = mean / sd * math.Sqrt(ppy)
	}
	yrs := float64(times[len(times)-1]-times[0]) / 1000 / 86400 / 365
	ann := math.Pow(equity, 1/yrs) - 1
	eq := 1.0
	peak, mdd := 0.0, 0.0
	for _, s := range steps {
		eq *= 1 + s
		if eq > peak {
			peak = eq
		}
		if d := (peak - eq) / peak; d > mdd {
			mdd = d
		}
	}
	fmt.Printf("REB=%d bar(%dh) W%dd hyst%d cost%.0fbp | 累计%+.1f%% 年化%+.1f%% Sharpe%.2f maxDD%.1f%% 年换手%.0fx (%d 期,%d币)\n",
		*rebBars, *rebBars*(24/(*barsPerDay)), *wdays, *hyst, *cost*1e4,
		(equity-1)*100, ann*100, sharpe, mdd*100, turnoverTotal/yrs, n, loaded)
}

// selectHyst picks K longs (lowest funding) / K shorts (highest), keeping currently-held
// coins that are still within top-(K+buffer) to damp boundary flicker.
func selectHyst(coins []xsfunding.CoinState, k, buffer int, heldL, heldS []string) (longs, shorts []string) {
	if len(coins) < 2*k {
		return nil, nil
	}
	sorted := make([]xsfunding.CoinState, len(coins))
	copy(sorted, coins)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TrailFunding < sorted[j].TrailFunding })
	pick := func(order []xsfunding.CoinState, held []string) []string {
		heldSet := map[string]bool{}
		for _, s := range held {
			heldSet[s] = true
		}
		band := k + buffer
		if band > len(order) {
			band = len(order)
		}
		inBand := map[string]bool{}
		for _, c := range order[:band] {
			inBand[c.Symbol] = true
		}
		var out []string
		seen := map[string]bool{}
		for _, c := range order { // kept held (in rank order) first
			if heldSet[c.Symbol] && inBand[c.Symbol] && len(out) < k {
				out = append(out, c.Symbol)
				seen[c.Symbol] = true
			}
		}
		for _, c := range order[:min(k, len(order))] { // fill from top-K
			if len(out) >= k {
				break
			}
			if !seen[c.Symbol] {
				out = append(out, c.Symbol)
				seen[c.Symbol] = true
			}
		}
		for _, c := range order { // safety fill
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
	longs = pick(sorted, heldL)
	// shorts: highest funding → reverse order
	rev := make([]xsfunding.CoinState, len(sorted))
	for i := range sorted {
		rev[i] = sorted[len(sorted)-1-i]
	}
	shorts = pick(rev, heldS)
	return longs, shorts
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
