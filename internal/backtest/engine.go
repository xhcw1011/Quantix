// Package backtest implements an event-driven backtesting engine.
package backtest

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// Config defines the parameters for a single backtest run.
type Config struct {
	Symbol         string
	Interval       string
	InitialCapital float64
	FeeRate        float64 // e.g. 0.001 = 0.1% (Binance taker)
	Slippage       float64 // e.g. 0.0005 = 0.05%
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
	stratCtx  *strategy.Context
	log       *zap.Logger
}

// New creates a ready-to-run backtest engine.
func New(cfg Config, store *data.Store, strat strategy.Strategy, log *zap.Logger) *Engine {
	portfolio := NewPortfolio(cfg.InitialCapital)
	broker := NewSimBroker(cfg.FeeRate, cfg.Slippage, portfolio, log)
	stratCtx := strategy.NewContext(portfolio, broker, log)

	return &Engine{
		cfg:      cfg,
		store:    store,
		strategy: strat,
		portfolio: portfolio,
		broker:   broker,
		stratCtx: stratCtx,
		log:      log,
	}
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

	// Record initial equity
	prices := map[string]float64{e.cfg.Symbol: klines[0].Open}
	e.portfolio.recordEquity(startTime, prices)

	// ── Event loop ────────────────────────────────────────────────────────────
	for _, bar := range klines {
		// 1. Strategy receives the closed bar and optionally queues orders
		e.strategy.OnBar(e.stratCtx, bar)

		// 2. Broker processes queued orders against this bar's close
		currentPrice := map[string]float64{bar.Symbol: bar.Close}
		fills := e.broker.Process(bar)

		// 3. Notify strategy of fills
		for _, fill := range fills {
			e.strategy.OnFill(e.stratCtx, fill)
		}

		// 4. Record equity snapshot after each bar
		e.portfolio.recordEquity(bar.CloseTime, currentPrice)
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

// closeOpenPositions force-closes any remaining open positions at the last bar's close.
func (e *Engine) closeOpenPositions(lastBar exchange.Kline) {
	for sym := range e.portfolio.positions {
		e.broker.PlaceOrder(strategy.OrderRequest{
			Symbol: sym,
			Side:   strategy.SideSell,
			Type:   strategy.OrderMarket,
			Qty:    0, // close all
		})
	}
	fills := e.broker.Process(lastBar)
	for _, fill := range fills {
		e.strategy.OnFill(e.stratCtx, fill)
	}
}
