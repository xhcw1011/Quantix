// Command xsfunding-live runs the cross-sectional funding rebalancer against a REAL
// (testnet by default) Binance Futures account: ExchangeSyncer reads live positions,
// PlanRotation computes the target, and orders flow ExecuteRotation → ORG gateway →
// Binance OrderBroker. Start with -dry (sync + plan, NO orders) to preview, then drop
// -dry to place. Credentials from env: QUANTIX_TESTNET_API_KEY / QUANTIX_TESTNET_SECRET.
//
//	source .testnet.env
//	go run ./cmd/xsfunding-live -dry           # preview one rotation, no orders
//	go run ./cmd/xsfunding-live -once          # place one rotation (testnet)
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
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/exchange/binance_futures"
	"github.com/Quantix/quantix/internal/orgateway"
	"github.com/Quantix/quantix/internal/rebalancer"
	"github.com/Quantix/quantix/internal/strategy"
)

const (
	L, W, K, REB = 14, 14, 5, 3
)

// liveBroker adapts a Binance OrderBroker to strategy.Broker (market orders, one-way
// mode → positionSide ""). The broker quantizes qty to each symbol's step internally.
type liveBroker struct {
	ob  *binance_futures.OrderBroker
	ctx context.Context
	log *zap.Logger
	dry bool
	seq int64
}

func (l *liveBroker) PlaceOrder(req strategy.OrderRequest) string {
	side := exchange.OrderSideBuy
	if req.Side == strategy.SideSell {
		side = exchange.OrderSideSell
	}
	l.seq++
	cid := fmt.Sprintf("xsf-%d", l.seq)
	if l.dry {
		l.log.Info("DRY would place", zap.String("sym", req.Symbol), zap.String("side", string(side)), zap.Float64("qty", req.Qty))
		return cid
	}
	var fill exchange.OrderFill
	var err error
	for attempt := 0; attempt < 3; attempt++ { // testnet throws transient 502s
		fill, err = l.ob.PlaceMarketOrder(l.ctx, req.Symbol, side, string(req.PositionSide), req.Qty, cid)
		if err == nil {
			break
		}
		time.Sleep(time.Duration(400*(attempt+1)) * time.Millisecond)
	}
	if err != nil {
		l.log.Warn("place FAILED", zap.String("sym", req.Symbol), zap.String("side", string(side)), zap.Float64("qty", req.Qty), zap.Error(err))
		return ""
	}
	l.log.Info("PLACED", zap.String("sym", req.Symbol), zap.String("side", string(side)),
		zap.Float64("qty", fill.FilledQty), zap.Float64("avgPx", fill.AvgPrice))
	return fill.ExchangeID
}

func (l *liveBroker) CancelOrder(string) error { return nil }

// liveState feeds the ORG a minimal snapshot (equity fixed at capital, gross from the
// synced book). ORG runs in Shadow for the first mechanics test, so it only observes.
type liveState struct {
	current map[string]float64
	capital float64
}

func (s *liveState) Snapshot(req strategy.OrderRequest) orgateway.OrderState {
	var gross float64
	for _, n := range s.current {
		gross += math.Abs(n)
	}
	return orgateway.OrderState{Equity: s.capital, GrossNotional: gross, Leverage: 1, Now: time.Now()}
}

func main() {
	dry := flag.Bool("dry", false, "sync + plan + log, place NO orders (preview)")
	once := flag.Bool("once", false, "place one rotation (required to actually trade)")
	schedule := flag.Bool("schedule", false, "run continuously: rotate every -every days at -at-hour UTC")
	flatten := flag.Bool("flatten", false, "close ALL open positions and exit (clean stop)")
	hedge := flag.Bool("hedge", false, "account is in hedge (dual-side) mode; default assumes one-way")
	testnet := flag.Bool("testnet", true, "testnet (safe); -testnet=false hits MAINNET real money (needs QUANTIX_LIVE_CONFIRM=true)")
	every := flag.Int("every", REB, "rebalance cadence in days (schedule mode)")
	atHour := flag.Int("at-hour", 8, "UTC hour to rebalance at, post-funding (schedule mode)")
	capital := flag.Float64("capital", 3000, "gross exposure in USDT (each position = capital/2K)")
	flag.Parse()
	if !*dry && !*once && !*flatten && !*schedule {
		fmt.Println("specify -dry (preview), -once (place), -schedule (loop), or -flatten (close all). refusing to run without a mode.")
		return
	}

	apiKey := os.Getenv("QUANTIX_TESTNET_API_KEY")
	secret := os.Getenv("QUANTIX_TESTNET_SECRET")
	if apiKey == "" || secret == "" {
		fmt.Println("missing QUANTIX_TESTNET_API_KEY / QUANTIX_TESTNET_SECRET (source .testnet.env)")
		os.Exit(1)
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

	ob, err := binance_futures.NewOrderBroker(apiKey, secret, *testnet, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "order broker: %v\n", err)
		os.Exit(1)
	}
	net := "TESTNET"
	if !*testnet {
		net = "MAINNET (REAL MONEY)"
	}

	if eq, err := ob.GetEquity(ctx, "USDT"); err == nil {
		fmt.Printf("# testnet account equity: %.2f USDT\n", eq)
	}

	if os.Getenv("XSF_INSPECT") != "" {
		ps, err := ob.GetPositions(ctx)
		fmt.Printf("raw GetPositions signed (err=%v):\n", err)
		for _, r := range ps {
			fmt.Printf("  %-10s posSide=%-6q amt=%+.4f\n", r.Symbol, r.PositionSide, r.Amt)
		}
		return
	}

	if *flatten {
		sy := rebalancer.NewExchangeSyncer(ob, func(string) float64 { return 0 })
		pos, err := sy.Positions(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sync: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("flattening %d positions...\n", len(pos))
		for _, p := range pos {
			side, ps := exchange.OrderSideSell, "LONG"
			if p.SignedQty < 0 {
				side, ps = exchange.OrderSideBuy, "SHORT"
			}
			qty := math.Abs(p.SignedQty)
			var e error
			for attempt := 0; attempt < 3; attempt++ {
				_, e = ob.PlaceMarketOrder(ctx, p.Symbol, side, ps, qty, fmt.Sprintf("flat-%s-%d", p.Symbol, attempt))
				if e == nil {
					break
				}
				time.Sleep(time.Duration(400*(attempt+1)) * time.Millisecond)
			}
			if e != nil {
				fmt.Printf("  %-10s close FAILED: %v\n", p.Symbol, e)
			} else {
				fmt.Printf("  %-10s closed (%s %s %.4f)\n", p.Symbol, side, ps, qty)
			}
		}
		time.Sleep(2 * time.Second)
		after, _ := sy.Positions(ctx)
		fmt.Printf("after flatten: %d open positions\n", len(after))
		for _, p := range after {
			fmt.Printf("    still %-10s qty %+.4f\n", p.Symbol, p.SignedQty)
		}
		return
	}

	start, _ := time.Parse("2006-01-02", "2024-07-01")
	end, _ := time.Parse("2006-01-02", "2026-07-08")
	rc := rebalancer.Config{K: K, GrossFrac: 1.0, MinDaysListed: L, MinVolume: 1.0, W: W, VolWin: 30, MinOrder: 5.0, Capital: *capital, MaxPerCoinFrac: 0.15}

	// rotate runs one full rebalance: reload DB series, sync exchange positions, plan
	// vs current, and place the delta orders through ORG → Binance (or dry-preview).
	rotate := func() {
		series, dates := rebalancer.LoadSeries(ctx, store, rebalancer.DefaultUniverse, start, end)
		if len(dates) == 0 {
			log.Warn("rotate: no data — run ingest-klines + ingest-funding")
			return
		}
		asOf := dates[len(dates)-1]
		priceAt := func(sym string) float64 { return series[sym].Price[asOf] }
		syncer := rebalancer.NewExchangeSyncer(ob, priceAt)
		positions, err := syncer.Positions(ctx)
		if err != nil {
			log.Warn("rotate: sync failed", zap.Error(err))
			return
		}
		current := rebalancer.PositionsToNotional(positions)
		fmt.Printf("\n# xsfunding-live  %s  asOf %s  synced %d open positions\n", net, asOf, len(positions))
		for _, p := range positions {
			fmt.Printf("    held %-10s qty %+.4f  ~$%.0f\n", p.Symbol, p.SignedQty, p.SignedQty*p.Price)
		}
		plan := rebalancer.PlanRotation(series, dates, asOf, current, rc, nil)
		if len(plan.Targets) == 0 {
			log.Warn("rotate: universe too small to form a book")
			return
		}
		fmt.Printf("  plan: %d target positions, %d delta trades\n", len(plan.Targets), len(plan.Trades))

		lb := &liveBroker{ob: ob, ctx: ctx, log: log, dry: *dry}
		rules := []orgateway.Rule{
			orgateway.MaxGrossLeverageRule{Frac: 100},
			orgateway.MaxNotionalPerOrderRule{Max: 1e9},
			&orgateway.OrderRateRule{Max: 100000, Window: time.Minute},
		}
		gw := orgateway.New(lb, rules, &liveState{current: current, capital: *capital}, orgateway.Shadow, log)
		rebalancer.ExecuteRotationSink(series, dates, asOf, rc, priceAt, current, gw, *hedge)
		fmt.Printf("  ORG stats: %v\n", gw.Stats())
		if *dry {
			fmt.Println("  DRY — no orders sent.")
			return
		}
		time.Sleep(2 * time.Second)
		after, _ := syncer.Positions(ctx)
		fmt.Printf("  after: %d open positions\n", len(after))
	}

	if *schedule {
		fmt.Printf("xsfunding-live SCHEDULE on %s: rotate every %dd @ %02d:00 UTC, cap %.0f%%/coin, $%.0f gross. Ctrl-C to stop.\n",
			net, *every, *atHour, rc.MaxPerCoinFrac*100, *capital)
		rebalancer.Loop(ctx, *every, *atHour, time.Now, func(t time.Time) { rotate() }, log)
		return
	}
	rotate() // -dry / -once
}
