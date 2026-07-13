// cascade-backtest runs the full liquidation-cascade-reversion (crash-fade) portfolio sim
// on 4h klines: detect sigma-shock bars, buy the close, hold, exit — with a hard cap on
// concurrent positions. Reports the make-or-break stats the mean-only gauge could not see:
// equity curve, max drawdown, left-tail (worst trades), exposure during crashes, and dumps
// daily returns for correlation with the live funding factor. No trading, no DB writes.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Quantix/quantix/internal/cascade"
	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"go.uber.org/zap"
)

var universe = []string{
	"AAVEUSDT", "ADAUSDT", "ALGOUSDT", "APTUSDT", "ARBUSDT", "ATOMUSDT", "AVAXUSDT", "AXSUSDT",
	"BCHUSDT", "BNBUSDT", "BTCUSDT", "CHZUSDT", "CRVUSDT", "DOGEUSDT", "DOTUSDT", "DYDXUSDT",
	"EGLDUSDT", "EOSUSDT", "ETCUSDT", "ETHUSDT", "FILUSDT", "GALAUSDT", "GRTUSDT", "HBARUSDT",
	"ICPUSDT", "IMXUSDT", "INJUSDT", "JUPUSDT", "LDOUSDT", "LINKUSDT", "LTCUSDT", "MANAUSDT",
	"NEARUSDT", "OPUSDT", "ORDIUSDT", "PYTHUSDT", "RUNEUSDT", "SANDUSDT", "SEIUSDT", "SOLUSDT",
	"STXUSDT", "SUIUSDT", "THETAUSDT", "TIAUSDT", "TRXUSDT", "UNIUSDT", "VETUSDT", "WLDUSDT",
	"XLMUSDT", "XRPUSDT",
}

const barsPerYear = 6 * 365 // 4h bars

func main() {
	k := flag.Float64("k", 3.0, "shock threshold in sigma")
	hold := flag.Int("hold", 6, "holding period in 4h bars (6 = 24h)")
	volWin := flag.Int("vol-win", 30, "rolling std window in bars")
	frac := flag.Float64("frac", 0.10, "capital fraction per position")
	maxc := flag.Int("max-concurrent", 8, "hard cap on simultaneous positions")
	cost := flag.Float64("cost", 0.0030, "round-trip cost (cascade slippage; 30bp default)")
	stop := flag.Float64("stop", 0, "per-position stop-loss fraction (0 = off)")
	wick := flag.Float64("wick", 0, "entry confirmation: require intrabar recovery (close-low)/(high-low) >= this (0 = off)")
	stepsPath := flag.String("steps", "", "dump daily returns CSV (to correlate vs funding)")
	flag.Parse()

	ctx := context.Background()
	log, _ := zap.NewProduction()
	cfg, err := config.Load("config/config.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()
	kStart := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	kEnd := time.Now().UTC()

	// 1. load 4h klines; collect the global time grid (union of all bar timestamps)
	type kbar struct {
		close, shock, wick float64
	}
	perSym := map[string]map[int64]kbar{}
	tsSet := map[int64]bool{}
	for _, sym := range universe {
		kl, err := store.GetKlinesBetween(ctx, sym, "4h", kStart, kEnd)
		if err != nil || len(kl) < *volWin+*hold+2 {
			continue
		}
		// shock per bar = return / trailing-vol over volWin bars (on the symbol's own series)
		m := map[int64]kbar{}
		ret := make([]float64, len(kl))
		for i := 1; i < len(kl); i++ {
			if kl[i-1].Close > 0 {
				ret[i] = kl[i].Close/kl[i-1].Close - 1
			}
		}
		for i := range kl {
			sh := 0.0
			if i >= *volWin {
				var mn, sd float64
				for j := i - *volWin; j < i; j++ {
					mn += ret[j]
				}
				mn /= float64(*volWin)
				for j := i - *volWin; j < i; j++ {
					sd += (ret[j] - mn) * (ret[j] - mn)
				}
				sd = math.Sqrt(sd / float64(*volWin))
				if sd > 0 {
					sh = ret[i] / sd
				}
			}
			wk := 1.0 // intrabar recovery: 1 = closed at high, 0 = closed at low
			if rng := kl[i].High - kl[i].Low; rng > 0 {
				wk = (kl[i].Close - kl[i].Low) / rng
			}
			ts := kl[i].OpenTime.UTC().UnixMilli()
			m[ts] = kbar{kl[i].Close, sh, wk}
			tsSet[ts] = true
		}
		perSym[sym] = m
	}
	if len(tsSet) == 0 {
		fmt.Println("no 4h data loaded")
		return
	}
	times := make([]int64, 0, len(tsSet))
	for ts := range tsSet {
		times = append(times, ts)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	gi := map[int64]int{}
	for i, ts := range times {
		gi[ts] = i
	}

	// 2. build aligned Series per symbol
	data0 := map[string]cascade.Series{}
	for sym, m := range perSym {
		s := make(cascade.Series, len(times))
		for ts, kb := range m {
			sh := kb.shock
			if *wick > 0 && kb.wick < *wick {
				sh = 0 // failed entry confirmation (still a falling knife) → no trigger
			}
			s[gi[ts]] = cascade.Bar{Close: kb.close, Shock: sh, Has: true}
		}
		data0[sym] = s
	}

	// 3. run the sim
	r := cascade.Simulate(times, data0, cascade.Config{
		K: *k, HoldBars: *hold, FracPerTrade: *frac, MaxConcurrent: *maxc, CostRT: *cost, StopLoss: *stop,
	})

	// 4. stats
	eq := r.Equity
	final := eq[len(eq)-1]
	years := float64(times[len(times)-1]-times[0]) / 1000 / 86400 / 365
	ann := math.Pow(final, 1/years) - 1
	// bar returns → Sharpe
	var rs []float64
	for i := 1; i < len(eq); i++ {
		if eq[i-1] > 0 {
			rs = append(rs, eq[i]/eq[i-1]-1)
		}
	}
	mean, sd := meanStd(rs)
	sharpe := 0.0
	if sd > 0 {
		sharpe = mean / sd * math.Sqrt(barsPerYear)
	}
	peak, maxDD := 0.0, 0.0
	for _, e := range eq {
		if e > peak {
			peak = e
		}
		if dd := (peak - e) / peak; dd > maxDD {
			maxDD = dd
		}
	}
	// trades
	var wins int
	var tsum float64
	rets := make([]float64, len(r.Trades))
	for i, t := range r.Trades {
		rets[i] = t.Ret
		tsum += t.Ret
		if t.Ret > 0 {
			wins++
		}
	}
	sort.Float64s(rets)
	// exposure
	var gsum, gmax float64
	for _, g := range r.Gross {
		gsum += g
		if g > gmax {
			gmax = g
		}
	}

	fmt.Printf("=== 崩盘抄底正式回测: k=%.1fσ hold=%d bars(%.0fh) frac=%.0f%% maxConc=%d cost=%.0fbp ===\n",
		*k, *hold, float64(*hold)*4, *frac*100, *maxc, *cost*1e4)
	fmt.Printf("  %s → %s  (%.2f yrs, %d coins, %d bars)\n",
		time.UnixMilli(times[0]).UTC().Format("2006-01-02"),
		time.UnixMilli(times[len(times)-1]).UTC().Format("2006-01-02"), years, len(perSym), len(times))
	fmt.Printf("\n  累计 %+.1f%%   年化 %+.1f%%   Sharpe %.2f   maxDD %.1f%%\n", (final-1)*100, ann*100, sharpe, maxDD*100)
	fmt.Printf("  交易 %d 笔   胜率 %.0f%%   均值 %+.3f%%/笔\n", len(r.Trades), float64(wins)/float64(len(r.Trades))*100, tsum/float64(len(r.Trades))*100)
	if len(rets) >= 5 {
		fmt.Printf("  左尾(最差5笔): %.1f%% %.1f%% %.1f%% %.1f%% %.1f%%   最好: %+.1f%%\n",
			rets[0]*100, rets[1]*100, rets[2]*100, rets[3]*100, rets[4]*100, rets[len(rets)-1]*100)
	}
	fmt.Printf("  平均敞口 %.0f%%   峰值敞口 %.0f%% (=市场级崩盘时的满仓程度; frac×maxConc=%.0f%%)\n",
		gsum/float64(len(r.Gross))*100, gmax*100, *frac*float64(*maxc)*100)

	// 5. dump daily returns (resample equity to last bar of each UTC day)
	if *stepsPath != "" {
		type dr struct {
			day string
			eq  float64
		}
		lastOfDay := map[string]float64{}
		var days []string
		for i, ts := range times {
			d := time.UnixMilli(ts).UTC().Format("2006-01-02")
			if _, ok := lastOfDay[d]; !ok {
				days = append(days, d)
			}
			lastOfDay[d] = eq[i]
		}
		sort.Strings(days)
		var b strings.Builder
		b.WriteString("date,ret\n")
		prev := 1.0
		for _, d := range days {
			fmt.Fprintf(&b, "%s,%.8f\n", d, lastOfDay[d]/prev-1)
			prev = lastOfDay[d]
		}
		if err := os.WriteFile(*stepsPath, []byte(b.String()), 0644); err == nil {
			fmt.Printf("\n  daily returns → %s (%d days)\n", *stepsPath, len(days))
		}
	}
}

func meanStd(xs []float64) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	var m float64
	for _, x := range xs {
		m += x
	}
	m /= float64(len(xs))
	var v float64
	for _, x := range xs {
		v += (x - m) * (x - m)
	}
	return m, math.Sqrt(v / float64(len(xs)))
}
