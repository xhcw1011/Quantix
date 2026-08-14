package live

import (
	"fmt"
	"math"

	"go.uber.org/zap"
)

// positionDivergenceEpsilon is the minimum quantity difference between the
// position syncer's exchange-reported position and the OMS's fill-derived
// position to treat as a real divergence rather than float64 noise -- far
// below any realistic single trade size on any tradable symbol.
const positionDivergenceEpsilon = 1e-6

func positionQtyDiverges(a, b float64) bool {
	return math.Abs(a-b) > positionDivergenceEpsilon
}

// positionsDiverge compares the position syncer's ground-truth exchange
// position (LONG/SHORT, hedge-mode futures/swap) against the OMS's
// fill-derived belief. They can disagree when a fill changed the real
// exchange position without ever being processed by this engine -- e.g. one
// that landed while the process was fully down, so it never got a DB row for
// recoverFromDB/claimOrphanOrders to reconcile on restart. Every strategy
// shares this exposure, not just guardian (2026-08-06 finding); this check
// is strategy-agnostic on purpose.
func (e *Engine) positionsDiverge() bool {
	if e.posSyncer == nil {
		return false
	}
	symbol := e.cfg.Symbol

	var realLongQty, realShortQty float64
	if lp := e.posSyncer.GetLong(); lp != nil {
		realLongQty = lp.Qty
	}
	if sp := e.posSyncer.GetShort(); sp != nil {
		realShortQty = sp.Qty
	}

	var believedLongQty, believedShortQty float64
	if lp, ok := e.positions.LongPosition(symbol); ok {
		believedLongQty = lp.Qty
	}
	if sp, ok := e.positions.ShortPosition(symbol); ok {
		believedShortQty = sp.Qty
	}

	return positionQtyDiverges(realLongQty, believedLongQty) ||
		positionQtyDiverges(realShortQty, believedShortQty)
}

// checkPositionDivergence fires a CRITICAL alert the first time
// positionsDiverge() goes true, then stays quiet on subsequent ticks until
// it resolves (mirrors the existing staleAlerted convention in
// engine_status.go) -- called from the engine's existing status ticker, so
// no new polling loop is introduced.
func (e *Engine) checkPositionDivergence() {
	if !e.positionsDiverge() {
		e.divergenceAlerted = false
		return
	}
	if e.divergenceAlerted {
		return
	}
	e.divergenceAlerted = true
	e.log.Error("仓位不一致：交易所真实持仓与系统记录的持仓对不上",
		zap.String("id", e.cfg.StrategyID))
	if e.notifier != nil {
		e.notifier.SystemAlert("CRITICAL", fmt.Sprintf(
			"⚠️ %s: 交易所真实持仓与系统记录的持仓不一致(疑似有成交未被系统处理,例如进程崩溃期间成交),请立即人工核对并处理",
			e.cfg.StrategyID))
	}
}
