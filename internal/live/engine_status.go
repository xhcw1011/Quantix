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

	e.log.Info("──── Live Trading Status ────",
		zap.Duration("uptime", elapsed),
		zap.Float64("cash", cash),
		zap.Float64("equity", equity),
		zap.Float64("total_return_pct", totalReturn),
		zap.Float64("realized_pnl", rpnl),
		zap.Int("open_positions", len(positions)),
		zap.Bool("risk_halted", e.risk.Halted()),
	)

	// Stale bar detection: warn if no kline data for > 2 minutes (should arrive every ~1m for 1m bars).
	staleSince := time.Since(e.lastBarTime)
	if staleSince > 2*time.Minute && !e.staleAlerted {
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
	e.bus.PublishStatus(bus.StatusMsg{ //nolint:errcheck
		StrategyID:     e.cfg.StrategyID,
		Cash:           e.broker.Cash(),
		Equity:         equity,
		RealizedPnL:    rpnl,
		TotalReturnPct: totalReturnPct,
		OpenPositions:  len(e.positions.All()),
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
