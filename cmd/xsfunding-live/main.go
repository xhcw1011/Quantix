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
	fill, err := l.ob.PlaceMarketOrder(l.ctx, req.Symbol, side, string(req.PositionSide), req.Qty, cid)
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
	capital := flag.Float64("capital", 3000, "gross exposure in USDT (each position = capital/2K)")
	flag.Parse()
	if !*dry && !*once {
		fmt.Println("specify -dry (preview) or -once (place). refusing to run without an explicit mode.")
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

	ob, err := binance_futures.NewOrderBroker(apiKey, secret, true /*testnet*/, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "order broker: %v\n", err)
		os.Exit(1)
	}

	if eq, err := ob.GetEquity(ctx, "USDT"); err == nil {
		fmt.Printf("# testnet account equity: %.2f USDT\n", eq)
	}

	start, _ := time.Parse("2006-01-02", "2024-07-01")
	end, _ := time.Parse("2006-01-02", "2026-07-08")
	series, dates := rebalancer.LoadSeries(ctx, store, rebalancer.DefaultUniverse, start, end)
	if len(dates) == 0 {
		fmt.Println("no data — run cmd/ingest-klines + ingest-funding first")
		return
	}
	asOf := dates[len(dates)-1]
	priceAt := func(sym string) float64 { return series[sym].Price[asOf] }

	rc := rebalancer.Config{K: K, GrossFrac: 1.0, MinDaysListed: L, MinVolume: 1.0, W: W, VolWin: 30, MinOrder: 5.0, Capital: *capital}

	// current positions from the exchange (truth), valued at asOf prices
	syncer := rebalancer.NewExchangeSyncer(ob, priceAt)
	positions, err := syncer.Positions(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sync positions: %v\n", err)
		os.Exit(1)
	}
	current := rebalancer.PositionsToNotional(positions)
	fmt.Printf("\n# xsfunding-live  asOf %s  testnet  synced %d open positions\n", asOf, len(positions))
	for _, p := range positions {
		fmt.Printf("    held %-10s qty %+.4f  ~$%.0f\n", p.Symbol, p.SignedQty, p.SignedQty*p.Price)
	}

	// plan the rotation vs current
	plan := rebalancer.PlanRotation(series, dates, asOf, current, rc, nil)
	if len(plan.Targets) == 0 {
		fmt.Println("universe too small to form a book — abort")
		return
	}
	fmt.Printf("  plan: %d target positions, %d delta trades\n", len(plan.Targets), len(plan.Trades))

	// execute (or dry-preview) through ORG(shadow) → Binance
	lb := &liveBroker{ob: ob, ctx: ctx, log: log, dry: *dry}
	rules := []orgateway.Rule{
		orgateway.MaxGrossLeverageRule{Frac: 100},
		orgateway.MaxNotionalPerOrderRule{Max: 1e9},
		&orgateway.OrderRateRule{Max: 100000, Window: time.Minute},
	}
	gw := orgateway.New(lb, rules, &liveState{current: current, capital: *capital}, orgateway.Shadow, log)
	rebalancer.ExecuteRotationSink(series, dates, asOf, rc, priceAt, current, gw, true /*hedge*/)

	fmt.Printf("\n  ORG stats: %v\n", gw.Stats())
	if *dry {
		fmt.Println("  DRY run — no orders sent. Re-run with -once to place.")
		return
	}
	// confirm: re-sync and show the resulting book
	time.Sleep(2 * time.Second)
	after, _ := syncer.Positions(ctx)
	fmt.Printf("  after: %d open positions on testnet\n", len(after))
	for _, p := range after {
		fmt.Printf("    now  %-10s qty %+.4f  ~$%.0f\n", p.Symbol, p.SignedQty, p.SignedQty*p.Price)
	}
}
