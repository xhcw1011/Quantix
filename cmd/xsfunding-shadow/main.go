// Command xsfunding-shadow computes the cross-sectional funding rebalancer's CURRENT
// target book from the DB and logs it — design §7 phase 3 (Shadow): compute + log the
// rotation, place NO orders. Run after `cmd/ingest-funding` populates funding_rates.
//
//	go run ./cmd/xsfunding-shadow
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/rebalancer"
)

func main() {
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
	end, _ := time.Parse("2006-01-02", "2026-07-08")
	series, dates := rebalancer.LoadSeries(ctx, store, rebalancer.DefaultUniverse, start, end)
	if len(dates) == 0 {
		fmt.Println("no data — run cmd/ingest-funding + backfill first")
		return
	}
	asOf := dates[len(dates)-1]

	rc := rebalancer.Config{
		K: 5, GrossFrac: 1.0, MinDaysListed: 14, MinVolume: 1.0,
		W: 14, VolWin: 30, MinOrder: 1.0, Capital: 10000,
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
