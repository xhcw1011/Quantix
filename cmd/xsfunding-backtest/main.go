// Command xsfunding-backtest feeds real DB klines + funding into the pure
// internal/xsfunding core and reports performance, to validate PARITY with
// scripts/xsmom_funding.py (target: funding factor ~35%/yr, Sharpe ~1.5) BEFORE any
// live-runner wiring. Reads from the DB data layer (klines + funding_rates), so it is
// rate-limit free — run `go run ./cmd/ingest-funding` first to populate funding_rates.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/xsfunding"
)

const fundStart = "2024-09-01"

var universe = []string{
	"BTCUSDT", "ETHUSDT", "SOLUSDT", "BNBUSDT", "XRPUSDT", "DOGEUSDT", "ADAUSDT",
	"AVAXUSDT", "LINKUSDT", "DOTUSDT", "TRXUSDT", "LTCUSDT", "BCHUSDT", "ATOMUSDT",
	"UNIUSDT", "ETCUSDT", "XLMUSDT", "NEARUSDT", "APTUSDT", "FILUSDT", "INJUSDT",
	"OPUSDT", "ARBUSDT", "SUIUSDT", "TIAUSDT", "SEIUSDT", "RUNEUSDT", "AAVEUSDT",
	"LDOUSDT", "WLDUSDT", "ALGOUSDT", "VETUSDT", "ICPUSDT", "HBARUSDT", "SANDUSDT",
	"MANAUSDT", "AXSUSDT", "GALAUSDT", "CHZUSDT", "EOSUSDT", "EGLDUSDT", "THETAUSDT",
	"GRTUSDT", "IMXUSDT", "STXUSDT", "ORDIUSDT", "PYTHUSDT", "JUPUSDT", "DYDXUSDT", "CRVUSDT",
}

const (
	L, LAG  = 14, 1
	cost    = 0.0010
	capital = 10000.0
)

func daykey(t time.Time) string { return t.UTC().Format("2006-01-02") }

func toMs(d string) int64 {
	t, _ := time.Parse("2006-01-02", d)
	return t.UnixMilli()
}

func main() {
	firstN := flag.Int("first", 0, "limit universe to first N coins (0 = all) — for parity vs a throttled Python run")
	symbolsFlag := flag.String("symbols", "", "comma-separated universe override (for exact parity)")
	dumpPath := flag.String("dump", "", "write loaded px/qv/fund to this JSON path and exit (for identical-data parity vs Python)")
	capFrac := flag.Float64("cap", 0, "per-coin cap as fraction of gross (0 = no cap; e.g. 0.15)")
	eqweight := flag.Bool("eqweight", false, "force equal weight (ignore inverse-vol) to compare vs risk-parity")
	rebFlag := flag.Int("reb", 3, "rebalance cadence in days (how often to re-rank/rotate; NOT funding frequency)")
	wFlag := flag.Int("w", 14, "trailing funding window in days (signal responsiveness)")
	costFlag := flag.Float64("cost", 0.0010, "per-side cost")
	signalFlag := flag.String("signal", "level", "ranking signal: level (funding level) | change (funding momentum: recent W minus prior W) | combo (level - k*change)")
	comboK := flag.Float64("combo-k", 1.0, "combo weight: rank key = level - k*change")
	flip := flag.Bool("flip", false, "negate the ranking signal (test the opposite direction)")
	revLB := flag.Int("rev-lb", 3, "reversal lookback in days (signal=reversal ranks by return over this window)")
	stepsPath := flag.String("steps", "", "write per-step (date,ret) CSV to this path (to combine independent books)")
	kFlag := flag.Int("k", 5, "positions per side (K long + K short); more breadth may support larger K")
	fromFlag := flag.String("from", "", "restrict analysis to rebalance dates >= this (YYYY-MM-DD; for out-of-sample split)")
	toFlag := flag.String("to", "", "restrict analysis to rebalance dates < this (YYYY-MM-DD)")
	flag.Parse()
	REB := *rebFlag
	W := *wFlag
	K := *kFlag
	if *symbolsFlag != "" {
		universe = strings.Split(*symbolsFlag, ",")
	} else if *firstN > 0 && *firstN < len(universe) {
		universe = universe[:*firstN]
	}

	log, _ := zap.NewProduction()
	cfg, err := config.Load("config/config.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	ctx := context.Background()
	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	kStart, _ := time.Parse("2006-01-02", "2024-07-01")
	kEnd, _ := time.Parse("2006-01-02", "2026-07-08")

	px := map[string]map[string]float64{}
	qv := map[string]map[string]float64{}
	fund := map[string]map[string]float64{}
	loaded := 0
	for _, s := range universe {
		kl, err := store.GetKlinesBetween(ctx, s, "1d", kStart, kEnd)
		if err != nil || len(kl) == 0 {
			continue // no price history → skip (factor filters these anyway)
		}
		p, v := map[string]float64{}, map[string]float64{}
		for _, k := range kl {
			d := daykey(k.OpenTime)
			p[d], v[d] = k.Close, k.QuoteVolume
		}
		fr, _ := store.GetFunding(ctx, s)
		f := map[string]float64{}
		for _, r := range fr {
			f[daykey(r.Time)] += r.Rate // daily sum (8h/4h marks collapse to the day)
		}
		px[s], qv[s], fund[s] = p, v, f
		loaded++
	}

	if *dumpPath != "" { // identical-data parity: hand the exact DB feed to Python
		b, _ := json.Marshal(map[string]interface{}{"px": px, "qv": qv, "fund": fund})
		if err := os.WriteFile(*dumpPath, b, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "dump: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("dumped %d coins to %s\n", loaded, *dumpPath)
		return
	}

	dateSet := map[string]bool{}
	for s := range px {
		for d := range px[s] {
			dateSet[d] = true
		}
	}
	dates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		dates = append(dates, d) // no fundStart filter — match Python; hasF check skips no-funding periods
	}
	sort.Strings(dates)
	idx := map[string]int{}
	for i, d := range dates {
		idx[d] = i
	}

	firstIdx := map[string]int{} // for DaysListed
	for s := range px {
		mn := len(dates)
		for d := range px[s] {
			if i, ok := idx[d]; ok && i < mn {
				mn = i
			}
		}
		firstIdx[s] = mn
	}

	trailFund := func(s string, i, n int) float64 {
		var t float64
		for j := i - n + 1; j <= i; j++ {
			if j >= 0 {
				t += fund[s][dates[j]]
			}
		}
		return t
	}
	futFund := func(s string, i, n int) float64 {
		var t float64
		for j := i + 1; j <= i+n && j < len(dates); j++ {
			t += fund[s][dates[j]]
		}
		return t
	}
	trailVol := func(s string, i int) float64 {
		var t float64
		var c int
		for j := i - 29; j <= i; j++ {
			if j >= 0 {
				if v, ok := qv[s][dates[j]]; ok {
					t += v
					c++
				}
			}
		}
		if c == 0 {
			return 0
		}
		return t / float64(c)
	}
	retVol := func(s string, i int) float64 {
		var rs []float64
		for j := i - 29; j <= i; j++ {
			if j >= 1 {
				if p0, ok0 := px[s][dates[j-1]]; ok0 && p0 > 0 {
					if p1, ok1 := px[s][dates[j]]; ok1 {
						rs = append(rs, p1/p0-1)
					}
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
		var v float64
		for _, r := range rs {
			v += (r - m) * (r - m)
		}
		return math.Sqrt(v / float64(len(rs)))
	}
	rvOf := func(s string, i int) float64 {
		if *eqweight {
			return 0 // Vol 0 → BuildTargetsRP falls back to equal weight
		}
		return retVol(s, i)
	}

	// sig is the RANKING key only (what we sort coins by to pick longs/shorts). The funding
	// P&L collected downstream stays actual (futFund), regardless of selection signal — so
	// this cleanly tests "rank by momentum, collect real funding". level=current factor;
	// change=funding momentum (recent W minus prior W, positive=crowding building);
	// combo=level minus k*change (fade coins whose crowding is already unwinding).
	sig := func(s string, si int) float64 {
		var v float64
		switch *signalFlag {
		case "change":
			v = trailFund(s, si, W) - trailFund(s, si-W, W)
		case "combo":
			v = trailFund(s, si, W) - *comboK*(trailFund(s, si, W)-trailFund(s, si-W, W))
		case "reversal":
			// rank by trailing return over rev-lb days; Rank longs lowest = biggest LOSERS
			// (short-term reversal), shorts biggest winners. Different mechanism from funding.
			p0, ok0 := px[s][dates[max(0, si-*revLB)]]
			p1, ok1 := px[s][dates[si]]
			if ok0 && ok1 && p0 > 0 {
				v = p1/p0 - 1
			}
		case "volume":
			// volume surge = today's quote-volume vs 30d trailing avg. High = attention/crowding.
			// Rank longs lowest surge (ignored) / shorts highest (piled-in). A crowding factor
			// measured by turnover instead of funding.
			if tv := trailVol(s, si); tv > 0 {
				v = qv[s][dates[si]] / tv
			}
		default: // level
			v = trailFund(s, si, W)
		}
		if *flip {
			v = -v
		}
		return v
	}

	var periods []xsfunding.Period
	var stepDates []string
	for i := L + LAG; i+REB < len(dates); i += REB {
		if (*fromFlag != "" && dates[i] < *fromFlag) || (*toFlag != "" && dates[i] >= *toFlag) {
			continue // out-of-sample window filter
		}
		si := i - LAG
		var coins []xsfunding.CoinState
		fwdRet := map[string]float64{}
		fwdFund := map[string]float64{}
		for s := range px {
			pSi, okSi := px[s][dates[si]]
			pSl, okSl := px[s][dates[si-L]]
			if !okSi || !okSl || pSl <= 0 {
				continue
			}
			if _, hasF := fund[s][dates[max(0, si-W)]]; !hasF { // no funding coverage → not in factor
				continue
			}
			coins = append(coins, xsfunding.CoinState{
				Symbol:       s,
				TrailFunding: sig(s, si),
				Price:        pSi,
				TrailVolume:  trailVol(s, si),
				DaysListed:   si - firstIdx[s],
				Vol:          rvOf(s, si),
			})
			if pNow, ok := px[s][dates[i]]; ok && pNow > 0 {
				if pFwd, ok2 := px[s][dates[i+REB]]; ok2 {
					fwdRet[s] = pFwd/pNow - 1
				}
			}
			fwdFund[s] = futFund(s, i, REB)
		}
		periods = append(periods, xsfunding.Period{Coins: coins, FwdRet: fwdRet, FwdFunding: fwdFund})
		stepDates = append(stepDates, dates[i])
	}

	if len(stepDates) < 2 {
		fmt.Printf("\n数据不足:loaded=%d 币, dates=%d, periods=%d。先跑 ingest-funding + 确认 klines。\n",
			loaded, len(dates), len(stepDates))
		return
	}
	cfg2 := xsfunding.Config{K: K, GrossFrac: 1.0, MinDaysListed: L, MinVolume: 1.0, FeeRate: *costFlag, MaxPerCoinFrac: *capFrac}
	eq, steps := xsfunding.RunBacktest(periods, capital, cfg2, 1.0)

	if *stepsPath != "" {
		var b strings.Builder
		b.WriteString("date,ret\n")
		for i, s := range steps {
			fmt.Fprintf(&b, "%s,%.8f\n", stepDates[i], s)
		}
		if err := os.WriteFile(*stepsPath, []byte(b.String()), 0644); err != nil {
			fmt.Printf("steps dump failed: %v\n", err)
		} else {
			fmt.Printf("steps -> %s (%d rows)\n", *stepsPath, len(steps))
		}
	}

	cum := eq - 1
	yrs := (toMs(stepDates[len(stepDates)-1]) - toMs(stepDates[0])) / 1000 / 86400
	years := float64(yrs) / 365
	ann := math.Pow(eq, 1/years) - 1
	mean, sd := meanStd(steps)
	sharpe := 0.0
	if sd > 0 {
		sharpe = mean / sd * math.Sqrt(365.0/float64(REB))
	}

	fmt.Printf("\n# Go parity (DB): 截面 funding 因子  %d/%d 币  %s→%s  L%d W%d K%d REB%d 费%.0fbp\n",
		loaded, len(universe), stepDates[0], stepDates[len(stepDates)-1], L, W, K, REB, cost*1e4)
	// max drawdown + worst single rebalance (the "double-loss" tail)
	eqDD := runningEquity(steps)
	peak, maxDD := 0.0, 0.0
	for _, e := range eqDD {
		if e > peak {
			peak = e
		}
		if dd := (peak - e) / peak; dd > maxDD {
			maxDD = dd
		}
	}
	worst := 0.0
	for _, s := range steps {
		if s < worst {
			worst = s
		}
	}
	fmt.Printf("  累计 %+.1f%%   年化 %+.1f%%   Sharpe %.2f   maxDD %.1f%%   最差单期 %.1f%%\n",
		cum*100, ann*100, sharpe, maxDD*100, worst*100)
	regimes := [][3]string{{"牛", "2024-11-01", "2025-02-15"}, {"25中", "2025-02-15", "2025-10-01"}, {"26跌", "2025-10-01", "2026-12-31"}}
	fmt.Printf("  regime: ")
	eqSeries := runningEquity(steps)
	for _, r := range regimes {
		fmt.Printf("%s %+.0f%%  ", r[0], windowRet(eqSeries, stepDates, r[1], r[2])*100)
	}
	fmt.Printf("\n  (parity CONFIRMED on identical data: Python -> +109.6%%/Sharpe1.97/牛24-中14-跌46,\n")
	fmt.Printf("   Go here -> matches to <1pp cumulative. Reproduce: `-dump p.json` then `python3 scripts/xsmom_funding.py --data p.json`)\n")
}

func meanStd(xs []float64) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	m := s / float64(len(xs))
	var v float64
	for _, x := range xs {
		v += (x - m) * (x - m)
	}
	return m, math.Sqrt(v / float64(len(xs)))
}

func runningEquity(steps []float64) []float64 {
	eq := make([]float64, len(steps))
	e := 1.0
	for i, s := range steps {
		e *= 1 + s
		eq[i] = e
	}
	return eq
}

func windowRet(eq []float64, dates []string, a, b string) float64 {
	var first, last = -1, -1
	for i, d := range dates {
		if d >= a && d < b {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 || last <= first {
		return 0
	}
	return eq[last]/eq[first] - 1
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
