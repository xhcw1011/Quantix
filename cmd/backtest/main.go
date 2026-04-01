// cmd/backtest runs a strategy backtest against historical data stored in TimescaleDB.
//
// Usage:
//
//	go run ./cmd/backtest [flags]
//	go run ./cmd/backtest -strategy macross -symbol BTCUSDT -interval 1h -capital 10000
//	go run ./cmd/backtest -strategy meanreversion -params '{"BBPeriod":20}'
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/backtest"
	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/logger"
	"github.com/Quantix/quantix/internal/strategy/registry"

	// Strategy registrations (side-effect imports)
	_ "github.com/Quantix/quantix/internal/strategy/aistrat"
	_ "github.com/Quantix/quantix/internal/strategy/grid"
	_ "github.com/Quantix/quantix/internal/strategy/macross"
	_ "github.com/Quantix/quantix/internal/strategy/meanreversion"
	_ "github.com/Quantix/quantix/internal/strategy/mlstrat"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// ── CLI flags ─────────────────────────────────────────────────────────────
	cfgPath := flag.String("config", "config/config.yaml", "config file path")
	strategyName := flag.String("strategy", "macross", "strategy name (macross|meanreversion|grid|ml)")
	symbol := flag.String("symbol", "BTCUSDT", "trading pair")
	interval := flag.String("interval", "1h", "kline interval (1m, 5m, 1h …)")
	capital := flag.Float64("capital", 10000.0, "initial capital in USDT")
	// Legacy macross flags (still supported)
	fast := flag.Int("fast", 10, "fast SMA period (macross only)")
	slow := flag.Int("slow", 30, "slow SMA period (macross only)")
	feeRate := flag.Float64("fee", 0.001, "taker fee rate (0.001 = 0.1%)")
	slippage := flag.Float64("slippage", 0.0005, "slippage fraction (0.0005 = 0.05%)")
	startStr := flag.String("start", "", "start date YYYY-MM-DD (optional)")
	endStr := flag.String("end", "", "end date YYYY-MM-DD (optional)")
	paramsJSON := flag.String("params", "", `strategy params JSON e.g. '{"FastPeriod":10}'`)
	outJSON := flag.String("out-json", "", "write full report to JSON file (optional)")
	outCSV := flag.String("out-csv", "", "write trades CSV prefix (optional, e.g. ./reports/bt)")
	flag.Parse()

	// ── Bootstrap ─────────────────────────────────────────────────────────────
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log, err := logger.New(cfg.App.Env, cfg.App.LogLevel)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer log.Sync() //nolint:errcheck

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// ── Database ──────────────────────────────────────────────────────────────
	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer store.Close()

	// ── Parse params ──────────────────────────────────────────────────────────
	params := map[string]any{
		"Symbol":     *symbol,
		"FastPeriod": float64(*fast),
		"SlowPeriod": float64(*slow),
	}
	if *paramsJSON != "" {
		var extra map[string]any
		if err := json.Unmarshal([]byte(*paramsJSON), &extra); err != nil {
			return fmt.Errorf("parse -params JSON: %w", err)
		}
		for k, v := range extra {
			params[k] = v
		}
		// Ensure Symbol is set
		if _, ok := params["Symbol"]; !ok {
			params["Symbol"] = *symbol
		}
	}

	// ── Strategy ──────────────────────────────────────────────────────────────
	strat, err := registry.Create(*strategyName, params, log)
	if err != nil {
		return fmt.Errorf("create strategy %q: %w", *strategyName, err)
	}

	// ── Time range ────────────────────────────────────────────────────────────
	btCfg := backtest.Config{
		Symbol:         *symbol,
		Interval:       *interval,
		InitialCapital: *capital,
		FeeRate:        *feeRate,
		Slippage:       *slippage,
	}
	if *startStr != "" {
		t, err := time.Parse("2006-01-02", *startStr)
		if err != nil {
			return fmt.Errorf("parse -start: %w", err)
		}
		btCfg.StartTime = t
	}
	if *endStr != "" {
		t, err := time.Parse("2006-01-02", *endStr)
		if err != nil {
			return fmt.Errorf("parse -end: %w", err)
		}
		btCfg.EndTime = t.Add(24*time.Hour - time.Second)
	}

	engine := backtest.New(btCfg, store, strat, log)

	// ── Run ───────────────────────────────────────────────────────────────────
	log.Info("running backtest",
		zap.String("strategy", strat.Name()),
		zap.String("symbol", *symbol),
		zap.String("interval", *interval),
		zap.Float64("capital", *capital),
	)

	report, err := engine.Run(ctx)
	if err != nil {
		return fmt.Errorf("backtest failed: %w", err)
	}

	// ── Output ────────────────────────────────────────────────────────────────
	backtest.PrintSummary(report, os.Stdout)

	if *outJSON != "" {
		f, err := os.Create(*outJSON)
		if err != nil {
			return fmt.Errorf("create json file: %w", err)
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return fmt.Errorf("write json: %w", err)
		}
		log.Info("report written", zap.String("path", *outJSON))
	}

	if *outCSV != "" {
		tradesPath := *outCSV + "_trades.csv"
		equityPath := *outCSV + "_equity.csv"

		if err := backtest.WriteTradesCSV(report, tradesPath); err != nil {
			return fmt.Errorf("write trades csv: %w", err)
		}
		if err := backtest.WriteEquityCSV(report, equityPath); err != nil {
			return fmt.Errorf("write equity csv: %w", err)
		}
		log.Info("CSV reports written",
			zap.String("trades", tradesPath),
			zap.String("equity", equityPath))
	}

	return nil
}
