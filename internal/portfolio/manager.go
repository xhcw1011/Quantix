// Package portfolio manages multiple strategy slots running concurrently,
// each with an isolated capital allocation.
package portfolio

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/bus"
	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/monitor"
	"github.com/Quantix/quantix/internal/notify"
	"github.com/Quantix/quantix/internal/paper"
	"github.com/Quantix/quantix/internal/risk"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// Config configures the portfolio manager.
type Config struct {
	TotalCapital   float64
	FeeRate        float64
	Slippage       float64
	Slots          []config.SlotConfig
	StatusInterval time.Duration
}

// slot holds one strategy's isolated engine.
type slot struct {
	cfg    config.SlotConfig
	engine *paper.Engine
}

// Manager runs N strategy slots sharing a common kline feed.
type Manager struct {
	cfg     Config
	slots   []*slot
	bus     *bus.Bus
	metrics *monitor.TradingMetrics
	notif   *notify.Notifier
	log     *zap.Logger

	startTime time.Time
}

// New creates a Manager from configuration.
// Returns an error if any strategy cannot be instantiated.
func New(
	cfg Config,
	riskCfg config.RiskConfig,
	b *bus.Bus,
	tm *monitor.TradingMetrics,
	notif *notify.Notifier,
	log *zap.Logger,
) (*Manager, error) {
	if cfg.FeeRate == 0 {
		cfg.FeeRate = 0.001
	}
	if cfg.Slippage == 0 {
		cfg.Slippage = 0.0005
	}
	if cfg.StatusInterval == 0 {
		cfg.StatusInterval = time.Minute
	}

	// Validate total capital fraction
	var totalFrac float64
	for _, sc := range cfg.Slots {
		totalFrac += sc.FracCapital
	}
	if totalFrac > 1.01 {
		return nil, fmt.Errorf("portfolio: total capital_frac %.2f exceeds 1.0", totalFrac)
	}

	slots := make([]*slot, 0, len(cfg.Slots))
	for _, sc := range cfg.Slots {
		capital := cfg.TotalCapital * sc.FracCapital
		if capital <= 0 {
			return nil, fmt.Errorf("slot %s/%s: capital=%.2f (check capital_frac and total_capital)",
				sc.Strategy, sc.Symbol, capital)
		}

		strat, err := registry.Create(sc.Strategy, sc.Params, log)
		if err != nil {
			return nil, fmt.Errorf("slot %s: %w", sc.Strategy, err)
		}

		rm := risk.New(risk.Config{
			MaxPositionPct:   riskCfg.MaxPositionPct,
			MaxDrawdownPct:   riskCfg.MaxDrawdownPct,
			MaxSingleLossPct: riskCfg.MaxSingleLossPct,
		}, capital, log)

		strategyID := fmt.Sprintf("%s-%s-%s", sc.Strategy, sc.Symbol, sc.Interval)
		eng := paper.New(paper.Config{
			StrategyID:     strategyID,
			InitialCapital: capital,
			FeeRate:        cfg.FeeRate,
			Slippage:       cfg.Slippage,
			StatusInterval: cfg.StatusInterval,
		}, strat, rm, b, tm, notif, log)

		slots = append(slots, &slot{cfg: sc, engine: eng})
		log.Info("portfolio slot created",
			zap.String("strategy", sc.Strategy),
			zap.String("symbol", sc.Symbol),
			zap.String("interval", sc.Interval),
			zap.Float64("capital", capital),
			zap.Float64("capital_frac", sc.FracCapital),
		)
	}

	return &Manager{
		cfg:     cfg,
		slots:   slots,
		bus:     b,
		metrics: tm,
		notif:   notif,
		log:     log,
	}, nil
}

// Run reads klines from klineCh and routes them to matching slots.
// Blocks until ctx is cancelled.
func (m *Manager) Run(ctx context.Context, klineCh <-chan exchange.Kline) error {
	m.startTime = time.Now()

	// Each slot gets its own buffered channel
	slotChans := make([]chan exchange.Kline, len(m.slots))
	for i := range slotChans {
		slotChans[i] = make(chan exchange.Kline, 64)
	}

	// Start per-slot engines
	errCh := make(chan error, len(m.slots))
	for i, sl := range m.slots {
		go func(engine *paper.Engine, ch <-chan exchange.Kline) {
			errCh <- engine.Run(ctx, ch)
		}(sl.engine, slotChans[i])
	}

	statusTicker := time.NewTicker(m.cfg.StatusInterval)
	defer statusTicker.Stop()

	m.log.Info("portfolio manager started",
		zap.Int("slots", len(m.slots)),
		zap.Float64("total_capital", m.cfg.TotalCapital),
	)

	for {
		select {
		case <-ctx.Done():
			m.log.Info(m.Summary())
			return nil

		case err := <-errCh:
			m.log.Info(m.Summary())
			return err

		case kline, ok := <-klineCh:
			if !ok {
				return nil
			}
			// Route to matching slots
			for i, sl := range m.slots {
				if sl.cfg.Symbol == kline.Symbol && sl.cfg.Interval == kline.Interval {
					select {
					case slotChans[i] <- kline:
					default:
						m.log.Warn("slot channel full, dropping bar",
							zap.String("strategy", sl.cfg.Strategy),
							zap.String("symbol", kline.Symbol))
					}
				}
			}

		case <-statusTicker.C:
			m.log.Info(m.Summary())
		}
	}
}

// TotalEquity returns the sum of equity across all slots.
func (m *Manager) TotalEquity() float64 {
	// We can't call engine.Equity() directly without a price feed;
	// return a best-effort sum by reading broker state.
	// For now, rely on Summary() which logs the state.
	return m.cfg.TotalCapital // placeholder
}

// Summary returns a human-readable multi-slot summary.
func (m *Manager) Summary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Portfolio Summary | %d slots | Total capital: $%.2f | Uptime: %s\n",
		len(m.slots), m.cfg.TotalCapital, time.Since(m.startTime).Truncate(time.Second)))
	for _, sl := range m.slots {
		sb.WriteString(fmt.Sprintf("  [%s/%s/%s] %s\n",
			sl.cfg.Strategy, sl.cfg.Symbol, sl.cfg.Interval,
			sl.engine.Summary()))
	}
	return sb.String()
}
