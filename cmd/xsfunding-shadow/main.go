// Command xsfunding-shadow computes the cross-sectional funding rebalancer's CURRENT
// target book from the DB and logs it — design §7 phase 3 (Shadow): compute + log the
// rotation, place NO orders. Run after `cmd/ingest-funding` populates funding_rates.
//
//	go run ./cmd/xsfunding-shadow
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/rebalancer"
)

func bookAt(series map[string]rebalancer.Series, dates []string, asOf string, rc rebalancer.Config) (longs, shorts []string) {
	plan := rebalancer.PlanRotation(series, dates, asOf, map[string]float64{}, rc, nil)
	for _, tg := range plan.Targets {
		if tg.Notional > 0 {
			longs = append(longs, tg.Symbol)
		} else {
			shorts = append(shorts, tg.Symbol)
		}
	}
	sort.Strings(longs)
	sort.Strings(shorts)
	return
}

func changed(prev, cur []string) int {
	set := map[string]bool{}
	for _, s := range prev {
		set[s] = true
	}
	var kept int
	for _, s := range cur {
		if set[s] {
			kept++
		}
	}
	return len(cur) - kept // how many are NEW vs prior book
}

func main() {
	history := flag.Bool("history", false, "sample the target book every ~30 days to show rotation over time")
	flag.Parse()

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

	start, _ := time.Parse("2006-01-02", "2024-07-01")
	end := time.Now().UTC().AddDate(0, 0, 2) // dynamic: latest data
	series, dates := rebalancer.LoadSeries(ctx, store, rebalancer.DefaultUniverse, start, end)
	if len(dates) == 0 {
		fmt.Println("no data — run cmd/ingest-funding + backfill first")
		return
	}
	asOf := dates[len(dates)-1]

	rc := rebalancer.Config{
		K: 5, GrossFrac: 1.0, MinDaysListed: 14, MinVolume: 1.0,
		W: 14, VolWin: 30, MinOrder: 1.0, Capital: 10000, MaxPerCoinFrac: 0.15,
	}

	if *history { // show the book rotating over the whole history
		fmt.Printf("\n# xsfunding target book over time (每 ~30 天采样, K5 → 5 多 / 5 空)\n")
		fmt.Printf("  %-12s %-38s %-38s %s\n", "date", "LONG (低费率/收)", "SHORT (高费率/收)", "换手")
		var pl, ps []string
		for i := 30; i < len(dates); i += 30 {
			d := dates[i]
			l, s := bookAt(series, dates, d, rc)
			if len(l) == 0 {
				continue
			}
			ch := ""
			if pl != nil {
				ch = fmt.Sprintf("多换%d 空换%d", changed(pl, l), changed(ps, s))
			}
			short := func(xs []string) string {
				out := ""
				for _, x := range xs {
					out += x[:len(x)-4] + " " // strip USDT
				}
				return out
			}
			fmt.Printf("  %-12s %-38s %-38s %s\n", d, short(l), short(s), ch)
			pl, ps = l, s
		}
		return
	}

	// funding lookup for display
	states := rebalancer.BuildStates(series, dates, asOf, rc.W, rc.VolWin)
	fundBy := map[string]float64{}
	for _, c := range states {
		fundBy[c.Symbol] = c.TrailFunding
	}

	// Shadow: flat book, no lot-step rounding (raw qty for the log).
	plan := rebalancer.PlanRotation(series, dates, asOf, map[string]float64{}, rc, nil)
	if len(plan.Targets) == 0 {
		fmt.Printf("asOf %s: eligible universe too small for 2K positions — no rotation\n", asOf)
		return
	}

	var longSyms, shortSyms []string
	for _, tg := range plan.Targets {
		if tg.Notional > 0 {
			longSyms = append(longSyms, tg.Symbol)
		} else {
			shortSyms = append(shortSyms, tg.Symbol)
		}
	}
	sort.Slice(longSyms, func(i, j int) bool { return fundBy[longSyms[i]] < fundBy[longSyms[j]] })
	sort.Slice(shortSyms, func(i, j int) bool { return fundBy[shortSyms[i]] > fundBy[shortSyms[j]] })

	fmt.Printf("\n# xsfunding SHADOW rotation  asOf %s  universe %d/%d 币  K%d W%d cap $%.0f\n",
		asOf, len(states), len(rebalancer.DefaultUniverse), rc.K, rc.W, rc.Capital)
	fmt.Printf("  LONG  (lowest funding, we RECEIVE):\n")
	for _, s := range longSyms {
		fmt.Printf("    %-10s trailW-funding %+.4f%%\n", s, fundBy[s]*100)
	}
	fmt.Printf("  SHORT (highest funding, we RECEIVE):\n")
	for _, s := range shortSyms {
		fmt.Printf("    %-10s trailW-funding %+.4f%%\n", s, fundBy[s]*100)
	}
	fmt.Printf("  would place %d trades (from flat), gross $%.0f, dollar-neutral. NO ORDERS SENT (shadow).\n",
		len(plan.Trades), rc.Capital*rc.GrossFrac)
}
