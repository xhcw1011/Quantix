// listing-probe is a RESEARCH gauge for the post-listing-decay thesis: newly-listed coins
// pump then bleed; short the confirmed breakdown from the post-listing high and ride the
// decay. Scans the full daily-kline universe (not just the funding-50), keeps only genuine
// new listings (first bar well after the data start), shorts the first breakdown after a
// pump, and reports the decay captured AND the short-squeeze left tail. No trading.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"go.uber.org/zap"
)

func main() {
	minDays := flag.Int("min-days", 5, "min days after listing before an entry is allowed (let it pump)")
	drop := flag.Float64("drop", 0.20, "breakdown: short when close falls this fraction below the post-listing high")
	hold := flag.Int("hold", 20, "holding period in days (ride the decay)")
	pumpMin := flag.Float64("pump-min", 1.3, "require post-listing high >= this x the listing price (pumped coins only)")
	maxSetup := flag.Int("max-setup", 60, "only look for the breakdown within this many days of listing")
	cost := flag.Float64("cost", 0.004, "round-trip short cost (new coins = wide spreads; 40bp default)")
	stop := flag.Float64("stop", 0, "short stop-loss: exit if price rises this fraction above entry (0 = off)")
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

	syms, err := store.ListSymbols(ctx, "1d")
	if err != nil {
		fmt.Fprintf(os.Stderr, "list: %v\n", err)
		os.Exit(1)
	}
	kStart := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	kEnd := time.Now().UTC()

	// first pass: per-coin first bar time, to find the global data start
	type coinKl struct {
		dates  []string
		close  []float64
		firstT time.Time
	}
	loaded := map[string]coinKl{}
	globalStart := kEnd
	for _, s := range syms {
		kl, err := store.GetKlinesBetween(ctx, s, "1d", kStart, kEnd)
		if err != nil || len(kl) < 20 {
			continue
		}
		ck := coinKl{firstT: kl[0].OpenTime.UTC()}
		for _, k := range kl {
			ck.dates = append(ck.dates, k.OpenTime.UTC().Format("2006-01-02"))
			ck.close = append(ck.close, k.Close)
		}
		loaded[s] = ck
		if ck.firstT.Before(globalStart) {
			globalStart = ck.firstT
		}
	}
	newCut := globalStart.AddDate(0, 0, 14) // genuine new listing = first bar > start+14d

	type trade struct {
		coin     string
		date     string
		pumpMult float64
		shortRet float64 // gross: 1 - exit/entry
	}
	var trades []trade
	var nNew int

	for s, ck := range loaded {
		if !ck.firstT.After(newCut) {
			continue // established coin (data starts at global start) — not a real listing
		}
		nNew++
		first := ck.close[0]
		runMax := ck.close[0]
		for i := 1; i < len(ck.close) && i <= *maxSetup; i++ {
			if ck.close[i] > runMax {
				runMax = ck.close[i]
			}
			if i < *minDays || runMax < first**pumpMin {
				continue
			}
			if ck.close[i] < runMax*(1-*drop) { // breakdown confirmed → short here
				if i+*hold < len(ck.close) {
					entry := ck.close[i]
					exit := ck.close[i+*hold]
					if *stop > 0 { // short stop: exit first day close rises stop above entry
						for j := i + 1; j <= i+*hold; j++ {
							if ck.close[j] >= entry*(1+*stop) {
								exit = ck.close[j]
								break
							}
						}
					}
					trades = append(trades, trade{s, ck.dates[i], runMax / first, 1 - exit/entry})
				}
				break // one entry per coin
			}
		}
	}

	if len(trades) == 0 {
		fmt.Printf("no qualifying setups. new-listing coins found: %d (of %d loaded). loosen filters.\n", nNew, len(loaded))
		return
	}
	sort.Slice(trades, func(i, j int) bool { return trades[i].shortRet < trades[j].shortRet })
	var sum, sumNet float64
	var wins int
	for _, t := range trades {
		sum += t.shortRet
		sumNet += t.shortRet - *cost
		if t.shortRet-*cost > 0 {
			wins++
		}
	}
	n := float64(len(trades))
	fmt.Printf("=== 上市后阴跌 gauge: 空破位(高点回撤%.0f%%) 骑%dd  pump>=%.1fx  ===\n", *drop*100, *hold, *pumpMin)
	fmt.Printf("  universe: %d 币载入, %d 个真新上市(首bar>%s), %d 个符合(pump后破位)\n",
		len(loaded), nNew, newCut.Format("2006-01-02"), len(trades))
	fmt.Printf("\n  做空前向收益(骑%dd): 均值 %+.2f%%(gross) / %+.2f%%(净%.0fbp)   胜率 %.0f%%\n",
		*hold, sum/n*100, sumNet/n*100, *cost*1e4, float64(wins)/n*100)
	fmt.Printf("  中位数 %+.2f%%   最好 %+.1f%%\n", trades[len(trades)/2].shortRet*100, trades[len(trades)-1].shortRet*100)
	fmt.Printf("  🔴逼空左尾(最差5笔=空了还继续拉): %.1f%% %.1f%% %.1f%% %.1f%% %.1f%%\n",
		trades[0].shortRet*100, trades[1].shortRet*100, trades[2].shortRet*100, trades[3].shortRet*100, trades[4].shortRet*100)
	// pump multiple distribution
	var pm float64
	for _, t := range trades {
		pm += t.pumpMult
	}
	fmt.Printf("  平均 pump 倍数(高点/上市价): %.2fx\n", pm/n)
}
