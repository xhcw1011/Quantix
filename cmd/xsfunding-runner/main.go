// Command xsfunding-runner drives the cross-sectional funding rebalancer through the
// REAL execution path — ExecuteRotation → ORG gateway (shadow) → multi-symbol PaperBook
// — to validate the ④+⑤ stack (scheduler + position syncer + ORG-gated execution) and
// meter realized turnover cost against the ≤20bp paper-forward budget.
//
//	go run ./cmd/xsfunding-runner            # replay the DB history every REB days
//	go run ./cmd/xsfunding-runner -once      # single rotation as of the latest date
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/orgateway"
	"github.com/Quantix/quantix/internal/rebalancer"
	"github.com/Quantix/quantix/internal/strategy"
)

const (
	L, W, K, REB = 14, 14, 5, 3
	cost         = 0.0010
	capital      = 10000.0
)

// paperState feeds the ORG a snapshot for each order, sourced from the paper book.
type paperState struct {
	book    *rebalancer.PaperBook
	capital float64
}

func (s *paperState) Snapshot(req strategy.OrderRequest) orgateway.OrderState {
	var gross float64
	for _, p := range s.book.Positions() {
		gross += math.Abs(p.SignedQty * p.Price)
	}
	return orgateway.OrderState{
		Equity:        s.capital,
		GrossNotional: gross,
		Price:         s.book.Price(req.Symbol),
		Leverage:      1,
		Now:           time.Now(),
	}
}

func main() {
	once := flag.Bool("once", false, "single rotation as of the latest date (live-tick shape) instead of full replay")
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
	end, _ := time.Parse("2006-01-02", "2026-07-08")
	series, dates := rebalancer.LoadSeries(ctx, store, rebalancer.DefaultUniverse, start, end)
	if len(dates) == 0 {
		fmt.Println("no data — run cmd/ingest-funding + backfill first")
		return
	}

	rc := rebalancer.Config{
		K: K, GrossFrac: 1.0, MinDaysListed: L, MinVolume: 1.0,
		W: W, VolWin: 30, MinOrder: 1.0, Capital: capital,
	}

	book := rebalancer.NewPaperBook(cost)
	// Generous Layer-1 caps: in shadow ORG never blocks; generous limits keep logs
	// clean while still exercising the full Strategy→ORG→broker path.
	rules := []orgateway.Rule{
		orgateway.MaxGrossLeverageRule{Frac: 100},
		orgateway.MaxNotionalPerOrderRule{Max: 1e12},
		&orgateway.OrderRateRule{Max: 1_000_000, Window: time.Minute},
	}
	gw := orgateway.New(book, rules, &paperState{book: book, capital: capital}, orgateway.Shadow, log)

	run := func(asOf string) rebalancer.Plan {
		return rebalancer.ExecuteRotation(series, dates, asOf, rc, nil, book, gw)
	}

	if *once {
		asOf := dates[len(dates)-1]
		plan := run(asOf)
		fmt.Printf("\n# xsfunding-runner ONCE  asOf %s  %d trades placed via ORG(shadow)→PaperBook\n", asOf, len(plan.Trades))
		reportBook(book)
		return
	}

	// Replay: step the date grid every REB days, rotating through the real path.
	var rotations, trades int
	firstAsOf, lastAsOf := "", ""
	for i := L; i+REB < len(dates); i += REB {
		asOf := dates[i]
		plan := run(asOf)
		if len(plan.Targets) == 0 {
			continue // universe too small this early
		}
		if firstAsOf == "" {
			firstAsOf = asOf
		}
		lastAsOf = asOf
		rotations++
		trades += len(plan.Trades)
	}

	fmt.Printf("\n# xsfunding-runner REPLAY  %s→%s  %d rotations  %d trades  (ExecuteRotation→ORG shadow→PaperBook)\n",
		firstAsOf, lastAsOf, rotations, trades)
	rc0 := book.RealizedCost()
	fmt.Printf("  realized fee cost: $%.2f = %.2f%% of $%.0f capital  (~%.1fbp/rotation on gross $%.0f)\n",
		rc0, rc0/capital*100, capital, rc0/float64(max1(rotations))/(capital)*1e4, capital)
	fmt.Printf("  ORG stats: %v\n", gw.Stats())
	reportBook(book)
	fmt.Printf("  NOTE paper fills at daily close (no slippage) — this meters TURNOVER/fees, not live slippage.\n")
	fmt.Printf("       real paper-forward (live data, maker/taker, weeks) is the ≤20bp make-or-break gate.\n")
}

func reportBook(book *rebalancer.PaperBook) {
	pos := book.Positions()
	var gross float64
	for _, p := range pos {
		gross += math.Abs(p.SignedQty * p.Price)
	}
	fmt.Printf("  final book: %d positions, gross $%.0f\n", len(pos), gross)
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
