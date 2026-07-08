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
	"sort"
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

// flatSyncer reports no open positions — pure shadow (no account). Replace with
// rebalancer.NewExchangeSyncer once a demo/live account is wired.
type flatSyncer struct{}

func (flatSyncer) Positions(context.Context) ([]rebalancer.Position, error) { return nil, nil }

// splitBook returns the long and short target symbols of a plan (sorted).
func splitBook(plan rebalancer.Plan) (longs, shorts []string) {
	for _, tg := range plan.Targets {
		if tg.Notional > 0 {
			longs = append(longs, tg.Symbol)
		} else {
			shorts = append(shorts, tg.Symbol)
		}
	}
	sort.Strings(longs)
	sort.Strings(shorts)
	return longs, shorts
}

func main() {
	once := flag.Bool("once", false, "single rotation as of the latest date (live-tick shape) instead of full replay")
	schedule := flag.Bool("schedule", false, "run the wall-clock shadow loop (rotate every REB days, log only, no orders)")
	tickNow := flag.Bool("tick-now", false, "fire one shadow tick immediately then exit (proves the scheduled tick end-to-end)")
	every := flag.Int("every", REB, "rebalance cadence in days (schedule mode)")
	atHour := flag.Int("at-hour", 8, "UTC hour to rebalance at, post-funding (schedule mode)")
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
		W: W, VolWin: 30, MinOrder: 1.0, Capital: capital, MaxPerCoinFrac: 0.15,
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

	// ── schedule / tick-now: wall-clock SHADOW loop (real syncer, no orders) ──
	if *schedule || *tickNow {
		// Pure shadow has no account → a flat syncer (no positions). Swap in
		// rebalancer.NewExchangeSyncer(querier, priceFn) once a demo account is wired.
		var sync rebalancer.Syncer = flatSyncer{}
		shadowTick := func(scheduled time.Time) {
			ser, dts := rebalancer.LoadSeries(ctx, store, rebalancer.DefaultUniverse, start, end) // refresh DB
			if len(dts) == 0 {
				log.Warn("shadow tick: no data")
				return
			}
			asOf := dts[len(dts)-1]
			positions, err := sync.Positions(ctx)
			if err != nil {
				log.Warn("shadow tick: sync failed", zap.Error(err))
				return
			}
			plan := rebalancer.PlanRotation(ser, dts, asOf, rebalancer.PositionsToNotional(positions), rc, nil)
			longs, shorts := splitBook(plan)
			log.Info("shadow rotation (NO ORDERS)",
				zap.Time("scheduled", scheduled), zap.String("asOf", asOf),
				zap.Int("held", len(positions)), zap.Strings("long", longs), zap.Strings("short", shorts),
				zap.Int("trades", len(plan.Trades)))
		}
		if *tickNow {
			next := rebalancer.NextTick(time.Now(), *every, *atHour)
			fmt.Printf("next scheduled rotation: %s UTC (every %dd @ %02d:00). Firing one tick now:\n", next.Format(time.RFC3339), *every, *atHour)
			shadowTick(time.Now())
			return
		}
		fmt.Printf("xsfunding-runner SCHEDULE (shadow): rotate every %dd @ %02d:00 UTC, log only. Ctrl-C to stop.\n", *every, *atHour)
		rebalancer.Loop(ctx, *every, *atHour, time.Now, shadowTick, log)
		return
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
