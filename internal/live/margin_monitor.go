package live

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/notify"
)

const (
	// defaultMarginWarnThreshold triggers a Telegram warning when any position's
	// maintenance margin ratio falls below this level (20%).
	defaultMarginWarnThreshold = 0.20

	// defaultMarginCriticalThreshold triggers an urgent alert when a position's
	// maintenance margin ratio falls below this level (12%).
	// At this level, liquidation is imminent and manual intervention is required.
	defaultMarginCriticalThreshold = 0.12
)

// MarginMonitor periodically polls exchange margin ratios and fires alerts
// via Telegram when positions approach liquidation.
//
// It is started as a goroutine from live.Engine.Run() and operates independently
// of the trading loop. Supported exchanges: OKX SWAP, Binance USDM Futures.
type MarginMonitor struct {
	strategyID        string
	client            exchange.MarginQuerier
	notif             *notify.Notifier // may be nil — alerts are skipped
	log               *zap.Logger
	interval          time.Duration
	warnThreshold     float64
	criticalThreshold float64
	consecutiveFails  int // tracks consecutive GetMarginRatios failures
}

// NewMarginMonitor creates a MarginMonitor.
// interval defaults to 60s when zero.
// warnThreshold defaults to 0.20 (20%) when zero.
// criticalThreshold defaults to 0.12 (12%) when zero.
func NewMarginMonitor(
	strategyID string,
	client exchange.MarginQuerier,
	notif *notify.Notifier,
	log *zap.Logger,
	interval time.Duration,
	warnThreshold float64,
	criticalThreshold float64,
) *MarginMonitor {
	if interval == 0 {
		interval = 60 * time.Second
	}
	if warnThreshold <= 0 {
		warnThreshold = defaultMarginWarnThreshold
	}
	if criticalThreshold <= 0 {
		criticalThreshold = defaultMarginCriticalThreshold
	}
	return &MarginMonitor{
		strategyID:        strategyID,
		client:            client,
		notif:             notif,
		log:               log,
		interval:          interval,
		warnThreshold:     warnThreshold,
		criticalThreshold: criticalThreshold,
	}
}

// Run polls margin ratios at the configured interval until ctx is cancelled.
// Intended to be launched as a goroutine: go mm.Run(ctx).
func (m *MarginMonitor) Run(ctx context.Context) {
	m.log.Info("margin monitor started",
		zap.String("strategy", m.strategyID),
		zap.Duration("interval", m.interval),
	)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.log.Info("margin monitor stopped", zap.String("strategy", m.strategyID))
			return
		case <-ticker.C:
			m.check(ctx)
		}
	}
}

// check queries margin ratios and emits alerts as needed.
func (m *MarginMonitor) check(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	positions, err := m.client.GetMarginRatios(checkCtx)
	if err != nil {
		m.consecutiveFails++
		m.log.Warn("margin monitor: failed to query margin ratios",
			zap.String("strategy", m.strategyID),
			zap.Int("consecutive_failures", m.consecutiveFails),
			zap.Error(err),
		)
		// After 3 consecutive failures, alert the operator — margin status is unknown
		// and liquidation could be occurring silently.
		// Re-alert every 10 consecutive failures so operators aren't blind indefinitely.
		if m.consecutiveFails >= 3 && m.consecutiveFails%10 == 3 && m.notif != nil {
			m.notif.SystemAlert("CRITICAL", fmt.Sprintf(
				"🚨 MARGIN MONITOR BLIND — %d consecutive query failures\n"+
					"Strategy: %s\n"+
					"Error: %s\n"+
					"Margin status unknown — liquidation risk unmonitored.\n"+
					"MANUAL INTERVENTION REQUIRED",
				m.consecutiveFails, m.strategyID, err.Error(),
			))
		}
		return
	}
	m.consecutiveFails = 0 // reset on success

	for _, p := range positions {
		m.evaluate(p)
	}
}

// evaluate checks a single position and fires the appropriate alert level.
func (m *MarginMonitor) evaluate(p exchange.PositionMarginInfo) {
	posDesc := p.Symbol
	if p.PositionSide != "" {
		posDesc += " " + p.PositionSide
	}

	switch {
	case p.MarginRatio < m.criticalThreshold:
		// Liquidation is imminent — escalate with maximum urgency.
		msg := fmt.Sprintf(
			"🚨 CRITICAL MARGIN — LIQUIDATION IMMINENT\n"+
				"Strategy: %s | Position: %s\n"+
				"Margin Ratio: %.1f%% (threshold: %.0f%%)\n"+
				"Size: %.4f | MANUAL INTERVENTION REQUIRED",
			m.strategyID, posDesc, p.MarginRatio*100, m.criticalThreshold*100, p.Size,
		)
		m.log.Error("CRITICAL: margin ratio below liquidation threshold",
			zap.String("strategy", m.strategyID),
			zap.String("symbol", p.Symbol),
			zap.String("position_side", p.PositionSide),
			zap.Float64("margin_ratio", p.MarginRatio),
			zap.Float64("size", p.Size),
		)
		if m.notif != nil {
			m.notif.SystemAlert("CRITICAL", msg)
		}

	case p.MarginRatio < m.warnThreshold:
		// Approaching danger zone — warn operators.
		msg := fmt.Sprintf(
			"⚠️ LOW MARGIN WARNING\n"+
				"Strategy: %s | Position: %s\n"+
				"Margin Ratio: %.1f%% (threshold: %.0f%%)\n"+
				"Size: %.4f",
			m.strategyID, posDesc, p.MarginRatio*100, m.warnThreshold*100, p.Size,
		)
		m.log.Warn("low margin ratio detected",
			zap.String("strategy", m.strategyID),
			zap.String("symbol", p.Symbol),
			zap.String("position_side", p.PositionSide),
			zap.Float64("margin_ratio", p.MarginRatio),
			zap.Float64("size", p.Size),
		)
		if m.notif != nil {
			m.notif.SystemAlert("WARN", msg)
		}
	}
}
