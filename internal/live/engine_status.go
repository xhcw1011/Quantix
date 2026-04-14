package live

import (
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/bus"
	"github.com/Quantix/quantix/internal/oms"
)

func (e *Engine) printStatus() {
	e.fillMu.Lock()
	rpnl := e.realizedPnL
	e.fillMu.Unlock()

	positions := e.positions.All()
	cash := e.broker.Cash()
	equity := e.broker.Equity()
	var totalReturn float64
	if e.cfg.InitialCapital > 0 {
		totalReturn = (equity/e.cfg.InitialCapital - 1) * 100
	}
	elapsed := time.Since(e.startTime).Truncate(time.Second)

	// Count positions: OMS-tracked + syncer-recovered (syncer positions may not be in OMS)
	openPos := len(positions)
	if e.posSyncer != nil {
		if e.posSyncer.HasPosition("LONG") { openPos = max(openPos, 1) }
		if e.posSyncer.HasPosition("SHORT") { openPos = max(openPos, 1) }
		if e.posSyncer.HasPosition("LONG") && e.posSyncer.HasPosition("SHORT") { openPos = max(openPos, 2) }
	}

	e.log.Info("──── Live Trading Status ────",
		zap.Duration("uptime", elapsed),
		zap.Float64("wallet_balance", e.broker.WalletBalance()),
		zap.Float64("cash", cash),
		zap.Float64("equity", equity),
		zap.Float64("total_return_pct", totalReturn),
		zap.Float64("realized_pnl", rpnl),
		zap.Int("open_positions", openPos),
		zap.Bool("risk_halted", e.risk.Halted()),
	)

	// Stale bar detection: warn if no kline data for > 2× bar interval.
	// For 5m bars, threshold = 10min; for 1m bars, threshold = max(2min, 2×1min).
	staleThreshold := 2 * time.Minute
	if e.cfg.BarInterval > 0 {
		t := 2 * e.cfg.BarInterval
		if t > staleThreshold {
			staleThreshold = t
		}
	}
	staleSince := time.Since(e.lastBarTime)
	if staleSince > staleThreshold && !e.staleAlerted {
		e.staleAlerted = true
		e.log.Error("no kline data received — possible WS disconnect",
			zap.Duration("silent_for", staleSince.Truncate(time.Second)),
			zap.String("strategy", e.cfg.StrategyID),
		)
		if e.notifier != nil {
			e.notifier.SystemAlert("CRITICAL", fmt.Sprintf(
				"⚠️ No kline data for %s\nStrategy %s may be stalled — check WS connection",
				staleSince.Truncate(time.Second), e.cfg.StrategyID,
			))
		}
	}
}

func (e *Engine) publishStatus() {
	if e.bus == nil {
		return
	}
	equity := e.broker.Equity()
	var totalReturnPct float64
	if e.cfg.InitialCapital > 0 {
		totalReturnPct = (equity/e.cfg.InitialCapital - 1) * 100
	}
	e.fillMu.Lock()
	rpnl := e.realizedPnL
	e.fillMu.Unlock()
	openPos := len(e.positions.All())
	if e.posSyncer != nil {
		if e.posSyncer.HasPosition("LONG") { openPos = max(openPos, 1) }
		if e.posSyncer.HasPosition("SHORT") { openPos = max(openPos, 1) }
		if e.posSyncer.HasPosition("LONG") && e.posSyncer.HasPosition("SHORT") { openPos = max(openPos, 2) }
	}
	e.bus.PublishStatus(bus.StatusMsg{ //nolint:errcheck
		StrategyID:     e.cfg.StrategyID,
		Cash:           e.broker.Cash(),
		Equity:         equity,
		RealizedPnL:    rpnl,
		TotalReturnPct: totalReturnPct,
		OpenPositions:  openPos,
		RiskHalted:     e.risk.Halted(),
	})
}

// Positions returns a copy of all currently open positions.
func (e *Engine) Positions() []oms.LivePosition { return e.positions.All() }

// LastPrice returns the close price of the most recently processed kline.
func (e *Engine) LastPrice() float64 {
	return safeLoadFloat64(&e.broker.lastPrice)
}

// Cash returns the current available cash balance.
func (e *Engine) Cash() float64 { return e.broker.Cash() }

// Equity returns the current total equity.
func (e *Engine) Equity() float64 { return e.broker.Equity() }

// GetTickCh returns the real-time ticker price channel.
func (e *Engine) GetTickCh() chan float64 {
	return e.tickCh
}

// SetExtra injects arbitrary data into the strategy context.
func (e *Engine) SetExtra(key string, val any) {
	if e.stratCtx != nil {
		e.stratCtx.Extra[key] = val
	}
}
