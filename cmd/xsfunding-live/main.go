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
	"strings"
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

// liveBroker adapts a Binance OrderBroker to strategy.Broker. In limit mode it posts a
// maker limit at the touch (buy@bid / sell@ask) and waits limitTO for a fill, then
// cancels and markets only the UNfilled remainder — capturing the maker fee (~2bp vs ~5bp
// taker) + avoiding the half-spread when it fills, without ever over-filling. The broker
// quantizes qty to each symbol's step internally.
type liveBroker struct {
	ob      *binance_futures.OrderBroker
	ctx     context.Context
	log     *zap.Logger
	dry     bool
	limit   bool
	limitTO time.Duration
	seq     int64
}

// tryMakerLimit posts a limit at the touch and polls up to limitTO; returns the qty
// filled as maker (0 on any error → caller markets the whole order).
func (l *liveBroker) tryMakerLimit(req strategy.OrderRequest, side exchange.OrderSide, cid string) float64 {
	bid, ask, err := l.ob.GetBookTicker(l.ctx, req.Symbol)
	if err != nil {
		return 0
	}
	price := bid // buy joins the bid
	if side == exchange.OrderSideSell {
		price = ask // sell joins the ask
	}
	oid, err := l.ob.PlaceLimitOrder(l.ctx, req.Symbol, side, string(req.PositionSide), req.Qty, price, cid)
	if err != nil {
		return 0
	}
	deadline := time.Now().Add(l.limitTO)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		status, fill, err := l.ob.GetOrderStatus(l.ctx, req.Symbol, oid)
		if err != nil {
			continue
		}
		if strings.EqualFold(status, "FILLED") {
			l.log.Info("MAKER FILLED", zap.String("sym", req.Symbol), zap.String("side", string(side)), zap.Float64("qty", fill.FilledQty), zap.Float64("px", price))
			return fill.FilledQty
		}
	}
	// timeout → cancel, then market only the unfilled remainder
	_ = l.ob.CancelOrder(l.ctx, req.Symbol, oid)
	_, fill, _ := l.ob.GetOrderStatus(l.ctx, req.Symbol, oid)
	if fill.FilledQty > 0 {
		l.log.Info("MAKER partial", zap.String("sym", req.Symbol), zap.Float64("filled", fill.FilledQty), zap.Float64("of", req.Qty))
	}
	return fill.FilledQty
}

func (l *liveBroker) PlaceOrder(req strategy.OrderRequest) string {
	side := exchange.OrderSideBuy
	if req.Side == strategy.SideSell {
		side = exchange.OrderSideSell
	}
	l.seq++
	cid := fmt.Sprintf("xsf-%d", l.seq)
	if l.dry {
		l.log.Info("DRY would place", zap.String("sym", req.Symbol), zap.String("side", string(side)), zap.Float64("qty", req.Qty), zap.Bool("limit", l.limit))
		return cid
	}

	remaining := req.Qty
	if l.limit { // maker attempt; market only what didn't fill
		remaining -= l.tryMakerLimit(req, side, cid)
	}
	if remaining <= req.Qty*5e-3 { // maker filled ≥99.5% — skip the dust (would reject on min-qty)
		return cid
	}

	var fill exchange.OrderFill
	var err error
	for attempt := 0; attempt < 3; attempt++ { // testnet throws transient 502s
		fill, err = l.ob.PlaceMarketOrder(l.ctx, req.Symbol, side, string(req.PositionSide), remaining, cid+"-m")
		if err == nil {
			break
		}
		time.Sleep(time.Duration(400*(attempt+1)) * time.Millisecond)
	}
	if err != nil {
		l.log.Warn("place FAILED", zap.String("sym", req.Symbol), zap.String("side", string(side)), zap.Float64("qty", remaining), zap.Error(err))
		return ""
	}
	l.log.Info("MARKET", zap.String("sym", req.Symbol), zap.String("side", string(side)),
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
	limit := flag.Bool("limit", false, "post maker limit at the touch (buy@bid/sell@ask) then market the unfilled remainder — saves ~3bp/side")
	limitTO := flag.Int("limit-timeout", 20, "seconds to wait for the maker limit to fill before markets fallback")
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
		if err != nil {
			fmt.Printf("GetPositions err: %v\n", err)
			return
		}
		eq, _ := ob.GetEquity(ctx, "USDT")
		fmt.Printf("\n# %s 账户盈亏  equity %.2f USDT\n", net, eq)
		fmt.Printf("  %-10s %-6s %10s %10s %10s %10s\n", "币", "方向", "名义$", "开仓价", "标记价", "浮动盈亏$")
		var gross, totPnl float64
		for _, r := range ps {
			dir := "多"
			if r.Amt < 0 {
				dir = "空"
			}
			notl := math.Abs(r.Amt) * r.MarkPrice
			gross += notl
			totPnl += r.UnrealizedPnl
			fmt.Printf("  %-10s %-6s %10.0f %10.4g %10.4g %+10.2f\n", r.Symbol, dir, notl, r.EntryPrice, r.MarkPrice, r.UnrealizedPnl)
		}
		fmt.Printf("  ── 合计:%d 仓,gross $%.0f,浮动盈亏 %+.2f USDT(%.2f%%/gross)\n",
			len(ps), gross, totPnl, totPnl/gross*100)
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

		lb := &liveBroker{ob: ob, ctx: ctx, log: log, dry: *dry, limit: *limit, limitTO: time.Duration(*limitTO) * time.Second}
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
