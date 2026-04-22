package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/bus"
	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	xfactory "github.com/Quantix/quantix/internal/exchange/factory"
	"github.com/Quantix/quantix/internal/live"
	"github.com/Quantix/quantix/internal/logger"
	"github.com/Quantix/quantix/internal/monitor"
	"github.com/Quantix/quantix/internal/notify"
	"github.com/Quantix/quantix/internal/paper"
	"github.com/Quantix/quantix/internal/portfolio"
	"github.com/Quantix/quantix/internal/risk"
	"github.com/Quantix/quantix/internal/strategy/macross"
	"github.com/Quantix/quantix/internal/strategy/registry"

	// Strategy registrations (side-effect imports)
	_ "github.com/Quantix/quantix/internal/strategy/grid"
	_ "github.com/Quantix/quantix/internal/strategy/meanreversion"
	_ "github.com/Quantix/quantix/internal/strategy/mlstrat"
)

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to config file")
	flag.Parse()

	if err := run(*cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(cfgPath string) error {
	// ── Config ────────────────────────────────────────────────────────────────
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// ── Logger ────────────────────────────────────────────────────────────────
	log, err := logger.New(cfg.App.Env, cfg.App.LogLevel, cfg.App.LogDir)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer log.Sync() //nolint:errcheck

	log.Info("starting Quantix",
		zap.String("mode", cfg.Trading.Mode),
		zap.String("env", cfg.App.Env),
		zap.String("exchange", cfg.Exchange.Active),
	)

	// ── Context (graceful shutdown) ────────────────────────────────────────────
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// ── Infrastructure metrics (Prometheus) ───────────────────────────────────
	infraMetrics := monitor.New()
	tradingMetrics := monitor.NewTradingMetrics()
	_ = tradingMetrics

	if cfg.Monitor.Enabled {
		addr := fmt.Sprintf(":%d", cfg.Monitor.PrometheusPort)
		go monitor.ServeHTTP(addr, log)
		log.Info("Prometheus metrics available", zap.String("addr", addr+"/metrics"))
	}

	// ── NATS bus (optional) ───────────────────────────────────────────────────
	var natsbus *bus.Bus
	if cfg.NATS.URL != "" {
		b, err := bus.Connect(cfg.NATS.URL, log)
		if err != nil {
			log.Warn("NATS unavailable, event bus disabled", zap.Error(err))
		} else {
			natsbus = b
			defer natsbus.Close()
		}
	}

	// ── Telegram notifier (optional) ──────────────────────────────────────────
	notifier := notify.New(cfg.Telegram.BotToken, cfg.Telegram.ChatID, log)

	// ── Database ──────────────────────────────────────────────────────────────
	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer store.Close()

	// ── Exchange clients (via factory) ────────────────────────────────────────
	restClient, err := xfactory.NewRESTClient(cfg.Exchange, log)
	if err != nil {
		return fmt.Errorf("create REST client: %w", err)
	}
	wsClient, err := xfactory.NewWSClient(cfg.Exchange, cfg.WS, log)
	if err != nil {
		return fmt.Errorf("create WS client: %w", err)
	}

	// ── Data ingestion pipeline ───────────────────────────────────────────────
	pipeline := data.NewPipeline(
		store,
		restClient,
		wsClient,
		infraMetrics,
		cfg.Data.Symbols,
		cfg.Data.Intervals,
		cfg.Data.BackfillLimit,
		log,
	)

	// ── Mode dispatch ─────────────────────────────────────────────────────────
	switch cfg.Trading.Mode {
	case "paper":
		if cfg.Portfolio.Enabled {
			return runPortfolio(ctx, cfg, pipeline, natsbus, tradingMetrics, notifier, log)
		}
		return runPaper(ctx, cfg, pipeline, natsbus, tradingMetrics, notifier, log)
	case "live":
		return runLive(ctx, cfg, pipeline, natsbus, tradingMetrics, notifier, log)
	default:
		log.Info("running in ingest-only mode")
		return pipeline.Run(ctx)
	}
}

// runPaper starts the paper trading engine alongside the data ingestion pipeline.
func runPaper(
	ctx context.Context,
	cfg *config.Config,
	pipeline *data.Pipeline,
	natsbus *bus.Bus,
	tradingMetrics *monitor.TradingMetrics,
	notifier *notify.Notifier,
	log *zap.Logger,
) error {
	paperCfg := cfg.Paper
	if paperCfg.InitialCapital == 0 {
		paperCfg.InitialCapital = 10_000.0
	}
	if paperCfg.FeeRate == 0 {
		paperCfg.FeeRate = 0.001
	}
	if paperCfg.Slippage == 0 {
		paperCfg.Slippage = 0.0005
	}
	if paperCfg.StrategyID == "" {
		paperCfg.StrategyID = "macross"
	}

	klineCh := make(chan exchange.Kline, 100)

	pipeline.OnClosedKline = func(k exchange.Kline) {
		if natsbus != nil {
			natsbus.PublishKline(bus.KlineMsg{ //nolint:errcheck
				Symbol:    k.Symbol,
				Interval:  k.Interval,
				Open:      k.Open,
				High:      k.High,
				Low:       k.Low,
				Close:     k.Close,
				Volume:    k.Volume,
				OpenTime:  k.OpenTime,
				CloseTime: k.CloseTime,
			})
		}
		select {
		case klineCh <- k:
		default:
			log.Warn("paper kline channel full, dropping bar",
				zap.String("symbol", k.Symbol),
				zap.String("interval", k.Interval))
		}
	}

	rm := risk.New(risk.Config{
		MaxPositionPct:   cfg.Risk.MaxPositionPct,
		MaxDrawdownPct:   cfg.Risk.MaxDrawdownPct,
		MaxSingleLossPct: cfg.Risk.MaxSingleLossPct,
	}, paperCfg.InitialCapital, log)

	symbol := cfg.Data.Symbols[0]
	interval := cfg.Data.Intervals[0]

	strat := macross.New(macross.Config{
		Symbol:     symbol,
		FastPeriod: 10,
		SlowPeriod: 30,
	})

	log.Info("paper trading mode",
		zap.String("strategy", strat.Name()),
		zap.String("symbol", symbol),
		zap.String("interval", interval),
		zap.Float64("capital", paperCfg.InitialCapital),
	)

	engine := paper.New(paper.Config{
		StrategyID:     paperCfg.StrategyID,
		InitialCapital: paperCfg.InitialCapital,
		FeeRate:        paperCfg.FeeRate,
		Slippage:       paperCfg.Slippage,
	}, strat, rm, natsbus, tradingMetrics, notifier, log)

	pipelineErr := make(chan error, 1)
	go func() { pipelineErr <- pipeline.Run(ctx) }()

	engineErr := make(chan error, 1)
	go func() { engineErr <- engine.Run(ctx, klineCh) }()

	select {
	case err := <-pipelineErr:
		log.Info(engine.Summary())
		return err
	case err := <-engineErr:
		log.Info(engine.Summary())
		return err
	}
}

// runPortfolio starts the multi-strategy portfolio manager.
func runPortfolio(
	ctx context.Context,
	cfg *config.Config,
	pipeline *data.Pipeline,
	natsbus *bus.Bus,
	tradingMetrics *monitor.TradingMetrics,
	notifier *notify.Notifier,
	log *zap.Logger,
) error {
	paperCfg := cfg.Paper
	if paperCfg.InitialCapital == 0 {
		paperCfg.InitialCapital = 10_000.0
	}

	klineCh := make(chan exchange.Kline, 256)

	pipeline.OnClosedKline = func(k exchange.Kline) {
		if natsbus != nil {
			natsbus.PublishKline(bus.KlineMsg{ //nolint:errcheck
				Symbol:    k.Symbol,
				Interval:  k.Interval,
				Open:      k.Open,
				High:      k.High,
				Low:       k.Low,
				Close:     k.Close,
				Volume:    k.Volume,
				OpenTime:  k.OpenTime,
				CloseTime: k.CloseTime,
			})
		}
		select {
		case klineCh <- k:
		default:
			log.Warn("portfolio kline channel full, dropping bar",
				zap.String("symbol", k.Symbol))
		}
	}

	mgr, err := portfolio.New(portfolio.Config{
		TotalCapital:   paperCfg.InitialCapital,
		FeeRate:        paperCfg.FeeRate,
		Slippage:       paperCfg.Slippage,
		Slots:          cfg.Portfolio.Slots,
		StatusInterval: time.Minute,
	}, cfg.Risk, natsbus, tradingMetrics, notifier, log)
	if err != nil {
		return fmt.Errorf("create portfolio manager: %w", err)
	}

	pipelineErr := make(chan error, 1)
	go func() { pipelineErr <- pipeline.Run(ctx) }()

	mgrErr := make(chan error, 1)
	go func() { mgrErr <- mgr.Run(ctx, klineCh) }()

	select {
	case err := <-pipelineErr:
		log.Info(mgr.Summary())
		return err
	case err := <-mgrErr:
		log.Info(mgr.Summary())
		return err
	}
}

// runLive starts the live trading engine alongside the data ingestion pipeline.
func runLive(
	ctx context.Context,
	cfg *config.Config,
	pipeline *data.Pipeline,
	natsbus *bus.Bus,
	tradingMetrics *monitor.TradingMetrics,
	notifier *notify.Notifier,
	log *zap.Logger,
) error {
	klineCh := make(chan exchange.Kline, 100)

	pipeline.OnClosedKline = func(k exchange.Kline) {
		if natsbus != nil {
			natsbus.PublishKline(bus.KlineMsg{ //nolint:errcheck
				Symbol:    k.Symbol,
				Interval:  k.Interval,
				Open:      k.Open,
				High:      k.High,
				Low:       k.Low,
				Close:     k.Close,
				Volume:    k.Volume,
				OpenTime:  k.OpenTime,
				CloseTime: k.CloseTime,
			})
		}
		select {
		case klineCh <- k:
		default:
			log.Warn("live kline channel full, dropping bar",
				zap.String("symbol", k.Symbol),
				zap.String("interval", k.Interval))
		}
	}

	// ── Live config with defaults ─────────────────────────────────────────────
	liveCfg := cfg.Live
	if liveCfg.StrategyID == "" {
		liveCfg.StrategyID = "macross"
	}
	if liveCfg.Symbol == "" && len(cfg.Data.Symbols) > 0 {
		liveCfg.Symbol = cfg.Data.Symbols[0]
	}
	if liveCfg.Interval == "" && len(cfg.Data.Intervals) > 0 {
		liveCfg.Interval = cfg.Data.Intervals[0]
	}
	baseCurrency := cfg.Trading.BaseCurrency
	if baseCurrency == "" {
		baseCurrency = "USDT"
	}

	// ── Strategy (via registry) ────────────────────────────────────────────────
	strat, err := registry.Create(liveCfg.StrategyID, map[string]any{
		"Symbol":   liveCfg.Symbol,
		"Interval": liveCfg.Interval,
	}, log)
	if err != nil {
		return fmt.Errorf("create live strategy %q: %w", liveCfg.StrategyID, err)
	}

	rm := risk.New(risk.Config{
		MaxPositionPct:   cfg.Risk.MaxPositionPct,
		MaxDrawdownPct:   cfg.Risk.MaxDrawdownPct,
		MaxSingleLossPct: cfg.Risk.MaxSingleLossPct,
	}, 0, log)

	orderClient, err := xfactory.NewOrderClient(cfg.Exchange, log)
	if err != nil {
		return fmt.Errorf("create order client: %w", err)
	}

	engine, err := live.NewEngine(
		live.EngineConfig{StrategyID: liveCfg.StrategyID + "-live"},
		strat,
		rm,
		natsbus,
		tradingMetrics,
		notifier,
		orderClient,
		log,
	)
	if err != nil {
		return fmt.Errorf("create live engine: %w", err)
	}

	syncCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := engine.SyncBalance(syncCtx, baseCurrency); err != nil {
		return fmt.Errorf("sync balance: %w", err)
	}

	pipelineErr := make(chan error, 1)
	go func() { pipelineErr <- pipeline.Run(ctx) }()

	engineErr := make(chan error, 1)
	go func() { engineErr <- engine.Run(ctx, klineCh) }()

	select {
	case err := <-pipelineErr:
		log.Info(engine.Summary())
		return err
	case err := <-engineErr:
		log.Info(engine.Summary())
		return err
	}
}
