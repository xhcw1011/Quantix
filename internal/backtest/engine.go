// Package backtest implements an event-driven backtesting engine.
package backtest

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/orgateway"
	"github.com/Quantix/quantix/internal/strategy"
)

// Config defines the parameters for a single backtest run.
type Config struct {
	Symbol         string
	Interval       string
	InitialCapital float64
	FeeRate        float64 // e.g. 0.001 = 0.1% (Binance taker)
	Slippage       float64 // e.g. 0.0005 = 0.05%
	// ContextIntervals are extra kline series (e.g. ["15m"]) fed to the strategy
	// for multi-timeframe context (trend filter / regime) but which do NOT drive
	// the broker or equity curve — only primary-interval bars do that. They are
	// interleaved with the primary stream by close time so the strategy sees the
	// same multi-TF history a live engine would aggregate.
	ContextIntervals []string
	// StartTime / EndTime filter klines loaded from DB.
	// Zero value = load all available.
	StartTime time.Time
	EndTime   time.Time
	// Klines can be provided directly (used in tests / external callers).
	// If non-nil, DB is not queried.
	Klines []exchange.Kline
}

// Engine drives the backtest: loads data, replays bars through the strategy,
// routes fills back to the portfolio, and produces a performance report.
type Engine struct {
	cfg       Config
	store     *data.Store // may be nil when cfg.Klines is provided
	strategy  strategy.Strategy
	portfolio *Portfolio
	broker    *SimBroker
	org       *orgateway.Gateway
	stratCtx  *strategy.Context
	log       *zap.Logger
}

// New creates a ready-to-run backtest engine.
func New(cfg Config, store *data.Store, strat strategy.Strategy, log *zap.Logger) *Engine {
	portfolio := NewPortfolio(cfg.InitialCapital)
	broker := NewSimBroker(cfg.FeeRate, cfg.Slippage, portfolio, log)

	// Order Risk Gateway in Shadow mode: measure how often each strategy's orders
	// would trip the Layer-1 ORDER-SAFETY limits across history (never blocks; a Nop
	// logger keeps research backtests quiet — the tally surfaces in the Report).
	// Layer 1 only, deliberately: portfolio-relative caps (position %, single-trade
	// %) belong in the portfolio engine, not the per-strategy order gateway.
	org := orgateway.New(
		broker,
		[]orgateway.Rule{
			// Spot backtest → leverage 1; frac 1.0 means "fully invested is fine",
			// so a normal single-symbol strategy is not flagged as over-leveraged.
			orgateway.MaxGrossLeverageRule{Frac: btORGGrossFrac},
			orgateway.MaxNotionalPerOrderRule{Max: btORGMaxNotionalPerOrder},
		},
		&orgBTState{portfolio: portfolio, broker: broker},
		orgateway.Shadow,
		zap.NewNop(),
	)
	stratCtx := strategy.NewContext(portfolio, org, log)

	return &Engine{
		cfg:       cfg,
		store:     store,
		strategy:  strat,
		portfolio: portfolio,
		broker:    broker,
		org:       org,
		stratCtx:  stratCtx,
		log:       log,
	}
}

// ORG backtest Layer-1 thresholds. Spot (leverage 1), so gross frac 1.0 = "fully
// invested is fine"; per-order notional cap off by default.
const (
	btORGGrossFrac           = 1.0
	btORGMaxNotionalPerOrder = 0.0
)

// orgBTState feeds the backtest ORG a per-order snapshot from the sim portfolio.
// Single-symbol, spot-like (leverage 1); account-level state is left zero so the
// Layer-3 rules auto-skip.
type orgBTState struct {
	portfolio *Portfolio
	broker    *SimBroker
}

func (s *orgBTState) Snapshot(req strategy.OrderRequest) orgateway.OrderState {
	price := s.broker.LastPrice()
	prices := map[string]float64{req.Symbol: price}
	var posVal float64
	if qty, _, ok := s.portfolio.Position(req.Symbol); ok {
		posVal = math.Abs(qty) * price
	}
	return orgateway.OrderState{
		Equity:        s.portfolio.Equity(prices),
		PositionValue: posVal,
		GrossNotional: posVal,
		Price:         price,
		Leverage:      1,
	}
}

// SetExtra sets additional strategy context dependencies (e.g. Redis for signal replay).
func (e *Engine) SetExtra(key string, val any) {
	e.stratCtx.Extra[key] = val
}

// Run executes the backtest and returns a performance report.
func (e *Engine) Run(ctx context.Context) (Report, error) {
	klines, err := e.loadKlines(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("load klines: %w", err)
	}
	if len(klines) < 2 {
		return Report{}, fmt.Errorf("insufficient data: %d bars (need ≥ 2)", len(klines))
	}

	e.log.Info("starting backtest",
		zap.String("strategy", e.strategy.Name()),
		zap.String("symbol", e.cfg.Symbol),
		zap.String("interval", e.cfg.Interval),
		zap.Int("bars", len(klines)),
		zap.Time("from", klines[0].OpenTime),
		zap.Time("to", klines[len(klines)-1].CloseTime),
	)

	startTime := klines[0].OpenTime
	endTime := klines[len(klines)-1].CloseTime

	// ── Build the event stream ─────────────────────────────────────────────────
	// Primary bars drive the broker + equity curve. Context bars (e.g. 15m) are
	// fed to the strategy for multi-TF context only. Interleave by close time so
	// the trend filter sees the correct 15m history when each 5m bar runs; on a
	// tie, feed the context bar first (the 15m candle that closes with a 5m candle
	// is already available when that 5m bar's logic runs).
	type event struct {
		bar     exchange.Kline
		primary bool
	}
	events := make([]event, 0, len(klines))
	for _, b := range klines {
		events = append(events, event{bar: b, primary: true})
	}
	for _, iv := range e.cfg.ContextIntervals {
		if iv == "" || iv == e.cfg.Interval {
			continue
		}
		ctxBars, err := e.loadContextKlines(ctx, iv, startTime, endTime)
		if err != nil {
			return Report{}, fmt.Errorf("load context %s: %w", iv, err)
		}
		e.log.Info("loaded context interval", zap.String("interval", iv), zap.Int("bars", len(ctxBars)))
		for _, b := range ctxBars {
			events = append(events, event{bar: b, primary: false})
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		ti, tj := events[i].bar.CloseTime, events[j].bar.CloseTime
		if ti.Equal(tj) {
			return !events[i].primary && events[j].primary // context before primary on tie
		}
		return ti.Before(tj)
	})

	// Flag the strategy that this is a historical replay (sim-clock + no live guard).
	e.stratCtx.Extra["backtest"] = true

	// Record initial equity
	prices := map[string]float64{e.cfg.Symbol: klines[0].Open}
	e.portfolio.recordEquity(startTime, prices)

	// ── Event loop ────────────────────────────────────────────────────────────
	for _, ev := range events {
		// 1. Strategy receives the bar. Context (non-primary) bars only update the
		//    strategy's interval buffer; they never reach the broker.
		e.broker.SetLastPrice(ev.bar.Close) // value order-time state for the ORG gateway
		e.strategy.OnBar(e.stratCtx, ev.bar)
		if !ev.primary {
			continue
		}

		// 2. Broker processes queued orders against this primary bar's close
		currentPrice := map[string]float64{ev.bar.Symbol: ev.bar.Close}
		fills := e.broker.Process(ev.bar)

		// 3. Notify strategy of fills
		for _, fill := range fills {
			e.strategy.OnFill(e.stratCtx, fill)
		}

		// 4. Record equity snapshot after each primary bar
		e.portfolio.recordEquity(ev.bar.CloseTime, currentPrice)
	}

	// Force-close any open positions at final bar price
	lastBar := klines[len(klines)-1]
	e.closeOpenPositions(lastBar)

	report := CalcMetrics(
		e.strategy.Name(),
		e.cfg.Symbol,
		e.cfg.Interval,
		e.portfolio,
		startTime,
		endTime,
		len(klines),
	)
	report.ORGStats = e.org.Stats() // shadow-mode Layer-1 ALLOW/DENY tally

	e.log.Info("backtest complete",
		zap.Float64("total_return_pct", report.TotalReturn),
		zap.Float64("sharpe", report.SharpeRatio),
		zap.Float64("max_dd_pct", report.MaxDrawdown),
		zap.Int("trades", report.TotalTrades),
	)

	return report, nil
}

// loadKlines fetches bars from cfg.Klines or from the database.
func (e *Engine) loadKlines(ctx context.Context) ([]exchange.Kline, error) {
	if len(e.cfg.Klines) > 0 {
		return e.cfg.Klines, nil
	}
	if e.store == nil {
		return nil, fmt.Errorf("no data source: provide cfg.Klines or a data.Store")
	}

	// Use time-range query when both bounds are set
	if !e.cfg.StartTime.IsZero() && !e.cfg.EndTime.IsZero() {
		return e.store.GetKlinesBetween(ctx, e.cfg.Symbol, e.cfg.Interval, e.cfg.StartTime, e.cfg.EndTime)
	}

	// Load from DB — reasonable upper bound
	klines, err := e.store.GetLatestKlines(ctx, e.cfg.Symbol, e.cfg.Interval, 10000)
	if err != nil {
		return nil, err
	}

	// GetLatestKlines returns DESC; reverse to chronological order
	for i, j := 0, len(klines)-1; i < j; i, j = i+1, j-1 {
		klines[i], klines[j] = klines[j], klines[i]
	}

	// Apply start-only filter if EndTime is zero
	if !e.cfg.StartTime.IsZero() {
		filtered := klines[:0]
		for _, k := range klines {
			if !k.OpenTime.Before(e.cfg.StartTime) {
				filtered = append(filtered, k)
			}
		}
		klines = filtered
	}

	return klines, nil
}

// loadContextKlines loads a secondary interval (e.g. 15m) over the same span as
// the primary series, for multi-TF strategy context. It never drives the broker.
func (e *Engine) loadContextKlines(ctx context.Context, interval string, start, end time.Time) ([]exchange.Kline, error) {
	if e.store == nil {
		return nil, fmt.Errorf("no data store for context interval %s", interval)
	}
	return e.store.GetKlinesBetween(ctx, e.cfg.Symbol, interval, start, end)
}

// closeOpenPositions force-closes any remaining open positions at the last bar's close.
func (e *Engine) closeOpenPositions(lastBar exchange.Kline) {
	for sym := range e.portfolio.longPositions {
		e.broker.PlaceOrder(strategy.OrderRequest{
			Symbol:       sym,
			Side:         strategy.SideSell,
			PositionSide: strategy.PositionSideLong,
			Type:         strategy.OrderMarket,
			Qty:          0,
		})
	}
	for sym := range e.portfolio.shortPositions {
		e.broker.PlaceOrder(strategy.OrderRequest{
			Symbol:       sym,
			Side:         strategy.SideBuy,
			PositionSide: strategy.PositionSideShort,
			Type:         strategy.OrderMarket,
			Qty:          0,
		})
	}
	fills := e.broker.Process(lastBar)
	for _, fill := range fills {
		e.strategy.OnFill(e.stratCtx, fill)
	}
}
