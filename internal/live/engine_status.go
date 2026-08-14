package live

import (
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/bus"
	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/strategy"
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
		if e.posSyncer.HasPosition("LONG") {
			openPos = max(openPos, 1)
		}
		if e.posSyncer.HasPosition("SHORT") {
			openPos = max(openPos, 1)
		}
		if e.posSyncer.HasPosition("LONG") && e.posSyncer.HasPosition("SHORT") {
			openPos = max(openPos, 2)
		}
	}

	e.log.Info("──── 实盘交易状态 ────",
		zap.Duration("uptime", elapsed),
		zap.Float64("wallet_balance", e.broker.WalletBalance()),
		zap.Float64("cash", cash),
		zap.Float64("equity", equity),
		zap.Float64("total_return_pct", totalReturn),
		zap.Float64("realized_pnl", rpnl),
		zap.Int("open_positions", openPos),
		zap.Bool("risk_halted", e.risk.Halted()),
	)

	// Order Risk Gateway (ORG) decision tally — the shadow-mode audit surface.
	if e.org != nil {
		e.log.Info("──── ORG (Order Risk Gateway) ────",
			zap.String("mode", e.org.Mode().String()),
			zap.Any("decisions", e.org.Stats()),
		)
	}

	// Capital Layer (Layer 3) pool status for this engine.
	if e.cfg.Pool != nil {
		ps := e.cfg.Pool.StatusFor(e.cfg.StrategyID)
		e.log.Info("──── Pool (Capital Layer) ────",
			zap.String("pool", ps.Name),
			zap.String("status", ps.Status.String()),
			zap.Float64("pool_dd_pct", ps.Drawdown*100),
			zap.Float64("pool_long_exp", ps.LongExp),
			zap.Float64("pool_short_exp", ps.ShortExp),
		)
	}

	// Push to WS dashboard via OnStatus callback (set by EngineManager when wsHub
	// is wired). Includes engine-level metrics + any strategy-reported state.
	if e.cfg.OnStatus != nil && e.cfg.UserID > 0 {
		payload := map[string]any{
			"engine_id":        e.cfg.StrategyID,
			"uptime_sec":       int64(elapsed.Seconds()),
			"wallet_balance":   e.broker.WalletBalance(),
			"cash":             cash,
			"equity":           equity,
			"total_return_pct": totalReturn,
			"realized_pnl":     rpnl,
			"open_positions":   openPos,
			"risk_halted":      e.risk.Halted(),
		}
		if e.org != nil {
			payload["org_mode"] = e.org.Mode().String()
			payload["org_decisions"] = e.org.Stats()
		}
		if sr, ok := e.strategy.(strategy.StatusReporter); ok {
			for k, v := range sr.Status() {
				payload["strat_"+k] = v
			}
		}
		e.cfg.OnStatus(e.cfg.UserID, payload)
	}

	// Stale bar detection: warn if no kline data for > 2× bar interval.
	// For 5m bars, threshold = 10min; for 1m bars, threshold = max(2min, 2×1min).
	staleThreshold := 2 * time.Minute
	if e.cfg.BarInterval > 0 {
		t := 2 * e.cfg.BarInterval
		if t > staleThreshold {
			staleThreshold = t
		}
	}
	// Tick-driven strategies (guardian) stay alive on real-time ticks even when the
	// kline feed is quiet — count the last tick so we don't cry "WS disconnect".
	lastActivity := e.lastBarTime
	if _, tickDriven := e.strategy.(strategy.TickReceiver); tickDriven {
		if tn := e.lastTickNano.Load(); tn > 0 {
			if t := time.Unix(0, tn); t.After(lastActivity) {
				lastActivity = t
			}
		}
	}
	staleSince := time.Since(lastActivity)
	if staleSince > staleThreshold && !e.staleAlerted {
		e.staleAlerted = true
		e.log.Error("长时间未收到K线数据 — 可能是WS连接断开",
			zap.Duration("silent_for", staleSince.Truncate(time.Second)),
			zap.String("strategy", e.cfg.StrategyID),
		)
		if e.notifier != nil {
			e.notifier.SystemAlert("CRITICAL", fmt.Sprintf(
				"⚠️ 已 %s 没收到K线数据\n策略 %s 可能卡住了，请检查WS连接",
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
		if e.posSyncer.HasPosition("LONG") {
			openPos = max(openPos, 1)
		}
		if e.posSyncer.HasPosition("SHORT") {
			openPos = max(openPos, 1)
		}
		if e.posSyncer.HasPosition("LONG") && e.posSyncer.HasPosition("SHORT") {
			openPos = max(openPos, 2)
		}
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

// SubmitParamUpdate queues a live parameter update to be applied by the run loop
// (never concurrently with OnBar/OnTick/OnFill). Non-blocking so the API handler
// never blocks on the engine goroutine; returns false if the buffer is full.
func (e *Engine) SubmitParamUpdate(params map[string]any) bool {
	select {
	case e.updateCh <- params:
		return true
	default:
		return false
	}
}

// SetExtra injects arbitrary data into the strategy context.
func (e *Engine) SetExtra(key string, val any) {
	if e.stratCtx != nil {
		e.stratCtx.Extra[key] = val
	}
}
