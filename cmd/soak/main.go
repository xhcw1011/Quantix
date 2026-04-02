// Command soak runs a paper-trading engine against a live WebSocket feed for
// a configurable duration and prints a resource/performance report on exit.
//
// Usage:
//
//	go run ./cmd/soak \
//	  -config  config/config.example.yaml \
//	  -strategy macross \
//	  -symbol   BTCUSDT \
//	  -interval 1m \
//	  -duration 4h
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/exchange/factory"
	"github.com/Quantix/quantix/internal/logger"
	"github.com/Quantix/quantix/internal/monitor"
	"github.com/Quantix/quantix/internal/paper"
	"github.com/Quantix/quantix/internal/risk"
	"github.com/Quantix/quantix/internal/strategy/registry"

	// Strategy side-effect registrations
	_ "github.com/Quantix/quantix/internal/strategy/aistrat"
	_ "github.com/Quantix/quantix/internal/strategy/grid"
	_ "github.com/Quantix/quantix/internal/strategy/macross"
	_ "github.com/Quantix/quantix/internal/strategy/meanreversion"
	_ "github.com/Quantix/quantix/internal/strategy/mlstrat"
)

func main() {
	cfgPath := flag.String("config", "config/config.example.yaml", "path to config YAML")
	strategyID := flag.String("strategy", "macross", "strategy name (e.g. macross, grid, meanreversion)")
	symbol := flag.String("symbol", "BTCUSDT", "trading symbol")
	interval := flag.String("interval", "1m", "kline interval (e.g. 1m, 5m, 1h)")
	duration := flag.Duration("duration", 4*time.Hour, "soak test duration (e.g. 4h, 30m)")
	flag.Parse()

	// ── Logger ────────────────────────────────────────────────────────────────
	log, err := logger.New("development", "info")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync() //nolint:errcheck

	// ── Config ────────────────────────────────────────────────────────────────
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal("failed to load config", zap.String("path", *cfgPath), zap.Error(err))
	}

	// Safety: if neither demo nor testnet is set, force demo to avoid accidental live connections.
	if !cfg.Exchange.Binance.Demo && !cfg.Exchange.Binance.Testnet {
		cfg.Exchange.Binance.Demo = true
	}

	log.Info("soak test starting",
		zap.String("strategy", *strategyID),
		zap.String("symbol", *symbol),
		zap.String("interval", *interval),
		zap.Duration("duration", *duration),
	)

	// ── Baseline memory snapshot ───────────────────────────────────────────────
	var memStart, memEnd runtime.MemStats
	runtime.ReadMemStats(&memStart)
	goroutinesStart := runtime.NumGoroutine()

	// ── Signal-aware context with hard deadline ───────────────────────────────
	baseCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	runCtx, runCancel := context.WithTimeout(baseCtx, *duration)
	defer runCancel()

	// ── Strategy ──────────────────────────────────────────────────────────────
	strat, err := registry.Create(*strategyID, nil, log)
	if err != nil {
		log.Fatal("failed to create strategy", zap.String("id", *strategyID), zap.Error(err))
	}

	// ── Risk manager ──────────────────────────────────────────────────────────
	const initialCapital = 10_000.0
	rm := risk.New(risk.Config{
		MaxPositionPct:   cfg.Risk.MaxPositionPct,
		MaxDrawdownPct:   cfg.Risk.MaxDrawdownPct,
		MaxSingleLossPct: cfg.Risk.MaxSingleLossPct,
	}, initialCapital, log)

	// ── Trading metrics (Prometheus) ──────────────────────────────────────────
	tm := monitor.NewTradingMetrics()

	// ── Paper engine ──────────────────────────────────────────────────────────
	engineCfg := paper.Config{
		StrategyID:     *strategyID,
		InitialCapital: initialCapital,
		FeeRate:        0.001,
		Slippage:       0.0005,
		Leverage:       1,
		StatusInterval: 30 * time.Second,
		// No DB persistence in soak mode — Store left nil.
	}
	eng := paper.New(engineCfg, strat, rm, nil, tm, nil, log)

	// ── WebSocket client ──────────────────────────────────────────────────────
	wsClient, err := factory.NewWSClient(cfg.Exchange, cfg.WS, log)
	if err != nil {
		log.Fatal("failed to create WS client", zap.Error(err))
	}

	// ── Kline channel ─────────────────────────────────────────────────────────
	klineCh := make(chan exchange.Kline, 256)

	// Subscribe in background goroutine; only deliver closed bars to the engine.
	go wsClient.SubscribeKlines(runCtx, []string{*symbol}, []string{*interval},
		func(k exchange.Kline) {
			if !k.IsClosed {
				return
			}
			select {
			case klineCh <- k:
			default:
				log.Warn("kline channel full, dropping bar",
					zap.String("symbol", k.Symbol),
					zap.Time("open_time", k.OpenTime),
				)
			}
		},
	)

	// ── Run ───────────────────────────────────────────────────────────────────
	startTime := time.Now()
	runErr := eng.Run(runCtx, klineCh)
	elapsed := time.Since(startTime).Truncate(time.Second)

	// ── Final memory snapshot ─────────────────────────────────────────────────
	runtime.ReadMemStats(&memEnd)
	goroutinesEnd := runtime.NumGoroutine()

	// ── Report ────────────────────────────────────────────────────────────────
	printReport(log, reportData{
		strategy:        *strategyID,
		symbol:          *symbol,
		interval:        *interval,
		duration:        *duration,
		elapsed:         elapsed,
		exitErr:         runErr,
		summary:         eng.Summary(),
		memStartAlloc:   memStart.Alloc,
		memEndAlloc:     memEnd.Alloc,
		memTotalAlloc:   memEnd.TotalAlloc,
		memSys:          memEnd.Sys,
		gcCycles:        memEnd.NumGC,
		goroutinesStart: goroutinesStart,
		goroutinesEnd:   goroutinesEnd,
	})
}

type reportData struct {
	strategy, symbol, interval string
	duration, elapsed          time.Duration
	exitErr                    error
	summary                    string

	memStartAlloc   uint64
	memEndAlloc     uint64
	memTotalAlloc   uint64
	memSys          uint64
	gcCycles        uint32
	goroutinesStart int
	goroutinesEnd   int
}

func printReport(log *zap.Logger, r reportData) {
	deltaAlloc := int64(r.memEndAlloc) - int64(r.memStartAlloc)
	exitErrStr := "nil"
	if r.exitErr != nil {
		exitErrStr = r.exitErr.Error()
	}

	log.Info("══════════════════════════════════════════")
	log.Info("SOAK TEST REPORT")
	log.Info("══════════════════════════════════════════")
	log.Info("run parameters",
		zap.String("strategy", r.strategy),
		zap.String("symbol", r.symbol),
		zap.String("interval", r.interval),
		zap.Duration("target_duration", r.duration),
		zap.Duration("actual_elapsed", r.elapsed),
	)
	log.Info("exit",
		zap.String("error", exitErrStr),
	)
	log.Info("memory",
		zap.String("start_alloc", formatBytes(r.memStartAlloc)),
		zap.String("end_alloc", formatBytes(r.memEndAlloc)),
		zap.String("delta_alloc", formatBytesDelta(deltaAlloc)),
		zap.String("total_alloc", formatBytes(r.memTotalAlloc)),
		zap.String("sys", formatBytes(r.memSys)),
		zap.Uint32("gc_cycles", r.gcCycles),
	)
	log.Info("goroutines",
		zap.Int("start", r.goroutinesStart),
		zap.Int("end", r.goroutinesEnd),
		zap.Int("delta", r.goroutinesEnd-r.goroutinesStart),
	)
	log.Info("engine summary", zap.String("summary", r.summary))
	log.Info("══════════════════════════════════════════")
}

func formatBytes(b uint64) string {
	const mib = 1 << 20
	if b >= mib {
		return fmt.Sprintf("%.2f MiB", float64(b)/float64(mib))
	}
	return fmt.Sprintf("%.2f KiB", float64(b)/1024)
}

func formatBytesDelta(b int64) string {
	const mib = 1 << 20
	sign := ""
	if b > 0 {
		sign = "+"
	}
	abs := b
	if abs < 0 {
		abs = -abs
	}
	if abs >= int64(mib) {
		return fmt.Sprintf("%s%.2f MiB", sign, float64(b)/float64(mib))
	}
	return fmt.Sprintf("%s%.2f KiB", sign, float64(b)/1024)
}
