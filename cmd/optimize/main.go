// cmd/optimize runs Walk-Forward Optimization (WFO) for a trading strategy.
//
// Usage:
//
//	go run ./cmd/optimize [flags]
//	go run ./cmd/optimize -strategy macross -symbol BTCUSDT -interval 1h \
//	  -params '{"FastPeriod":[5,10,15],"SlowPeriod":[20,30,40]}' \
//	  -out-json /tmp/wfo.json
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

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/logger"
	"github.com/Quantix/quantix/internal/optimize"

	// Strategy registrations
	_ "github.com/Quantix/quantix/internal/strategy/grid"
	_ "github.com/Quantix/quantix/internal/strategy/macross"
	_ "github.com/Quantix/quantix/internal/strategy/meanreversion"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// ── Flags ─────────────────────────────────────────────────────────────────
	cfgPath := flag.String("config", "config/config.yaml", "config file path")
	strategyName := flag.String("strategy", "macross", "strategy name")
	symbol := flag.String("symbol", "BTCUSDT", "trading pair")
	interval := flag.String("interval", "1h", "kline interval")
	startStr := flag.String("start", "", "global start date YYYY-MM-DD (optional; uses DB data)")
	endStr := flag.String("end", "", "global end date YYYY-MM-DD (optional)")
	inSample := flag.Int("insample", 500, "in-sample window (bars)")
	outSample := flag.Int("outsample", 100, "out-of-sample window (bars)")
	step := flag.Int("step", 100, "step size (bars)")
	paramsJSON := flag.String("params", "", `parameter grid JSON e.g. '{"FastPeriod":[5,10],"SlowPeriod":[20,30]}'`)
	workers := flag.Int("workers", 0, "worker goroutines (0 = GOMAXPROCS)")
	capital := flag.Float64("capital", 10000.0, "initial capital")
	fee := flag.Float64("fee", 0.001, "taker fee rate")
	slippage := flag.Float64("slippage", 0.0005, "slippage fraction")
	outJSON := flag.String("out-json", "", "write WFO results to JSON file")
	outCSV := flag.String("out-csv", "", "write WFO summary to CSV file")
	flag.Parse()

	_ = startStr // reserved for future DB time-range filtering
	_ = endStr

	// ── Bootstrap ─────────────────────────────────────────────────────────────
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log, err := logger.New(cfg.App.Env, cfg.App.LogLevel, cfg.App.LogDir)
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

	// ── Parse param grid ──────────────────────────────────────────────────────
	paramGrid := map[string][]float64{}
	if *paramsJSON != "" {
		if err := json.Unmarshal([]byte(*paramsJSON), &paramGrid); err != nil {
			return fmt.Errorf("parse -params JSON: %w", err)
		}
	} else {
		// Default grid for macross
		paramGrid = map[string][]float64{
			"FastPeriod": {5, 10, 15},
			"SlowPeriod": {20, 30, 40},
		}
	}

	// ── Optimizer ─────────────────────────────────────────────────────────────
	optCfg := optimize.Config{
		Symbol:        *symbol,
		Interval:      *interval,
		InSampleBars:  *inSample,
		OutSampleBars: *outSample,
		StepBars:      *step,
		ParamGrid:     paramGrid,
		StrategyName:  *strategyName,
		Capital:       *capital,
		Fee:           *fee,
		Slippage:      *slippage,
		Workers:       *workers,
	}

	opt := optimize.New(optCfg, store, log)

	log.Info("starting WFO",
		zap.String("strategy", *strategyName),
		zap.String("symbol", *symbol),
		zap.String("interval", *interval),
		zap.Int("insample", *inSample),
		zap.Int("outsample", *outSample),
		zap.Int("step", *step),
	)

	start := time.Now()
	results, err := opt.Run(ctx)
	if err != nil {
		return fmt.Errorf("WFO failed: %w", err)
	}
	elapsed := time.Since(start)

	// ── Console summary ────────────────────────────────────────────────────────
	fmt.Printf("\n╔══ WFO Summary ═══════════════════════════════════════════════╗\n")
	fmt.Printf("  Strategy:   %s | Symbol: %s | Interval: %s\n", *strategyName, *symbol, *interval)
	fmt.Printf("  Windows:    %d | IS bars: %d | OOS bars: %d | Step: %d\n",
		len(results), *inSample, *outSample, *step)
	fmt.Printf("  Elapsed:    %s\n", elapsed.Truncate(time.Second))
	fmt.Println()

	for _, r := range results {
		fmt.Printf("  Window %d  IS: %s → %s  OOS: %s → %s\n",
			r.Window.Index,
			r.Window.ISStart.Format("2006-01-02"),
			r.Window.ISEnd.Format("2006-01-02"),
			r.Window.OOSStart.Format("2006-01-02"),
			r.Window.OOSEnd.Format("2006-01-02"),
		)
		fmt.Printf("    IS results: %d | Pareto front: %d\n",
			len(r.AllResults), len(r.ParetoFront))
		for _, p := range r.ParetoFront {
			fmt.Printf("    Pareto: params=%v IS(sharpe=%.3f, maxDD=%.2f%%) OOS(sharpe=%.3f, maxDD=%.2f%%)\n",
				map[string]float64(p.Params),
				p.ISReport.SharpeRatio, p.ISReport.MaxDrawdown,
				p.OOSReport.SharpeRatio, p.OOSReport.MaxDrawdown,
			)
		}
	}
	fmt.Printf("╚══════════════════════════════════════════════════════════════╝\n\n")

	// ── JSON output ───────────────────────────────────────────────────────────
	if *outJSON != "" {
		f, err := os.Create(*outJSON)
		if err != nil {
			return fmt.Errorf("create json file: %w", err)
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return fmt.Errorf("write json: %w", err)
		}
		log.Info("WFO results written", zap.String("path", *outJSON))
	}

	// ── CSV output ────────────────────────────────────────────────────────────
	if *outCSV != "" {
		f, err := os.Create(*outCSV)
		if err != nil {
			return fmt.Errorf("create csv file: %w", err)
		}
		defer f.Close()
		fmt.Fprintln(f, "window,is_start,is_end,oos_start,oos_end,params,is_sharpe,is_maxdd,oos_sharpe,oos_maxdd")
		for _, r := range results {
			for _, p := range r.ParetoFront {
				params, _ := json.Marshal(p.Params)
				fmt.Fprintf(f, "%d,%s,%s,%s,%s,%s,%.4f,%.4f,%.4f,%.4f\n",
					r.Window.Index,
					r.Window.ISStart.Format("2006-01-02"),
					r.Window.ISEnd.Format("2006-01-02"),
					r.Window.OOSStart.Format("2006-01-02"),
					r.Window.OOSEnd.Format("2006-01-02"),
					params,
					p.ISReport.SharpeRatio, p.ISReport.MaxDrawdown,
					p.OOSReport.SharpeRatio, p.OOSReport.MaxDrawdown,
				)
			}
		}
		log.Info("WFO CSV written", zap.String("path", *outCSV))
	}

	return nil
}
