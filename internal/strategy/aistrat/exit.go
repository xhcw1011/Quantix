package aistrat

import (
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
)

// placeStagedExitOrders places exchange-native SL + 4 staged TP limit orders for trend mode.
//
// TP plan (based on R = |entry - stopLoss|, no ATR cap):
//   +1.5R → close 30%  (cover risk + small profit)
//   +2.0R → close 30%  (lock profit, 60% total closed)
//   Remaining 40% → R-based trailing (1.5R→BE, 2R→ATR trail) + bounce TP
func (s *AIStrategy) placeStagedExitOrders(ctx *strategy.Context, pos *posState) {
	ep, ok := ctx.Extra["staged_exit"].(strategy.StagedExitPlacer)
	if !ok {
		s.log.Warn("staged exit placer not available (paper/backtest mode), using local management")
		return
	}
	s.stagedEP = ep // cache for handleStagedTPFill (no ctx available there)

	R := pos.R
	if R <= 0 { return }
	// R = |entry - SL|, directly from position. No ATR cap — let profits run.
	entry := pos.entryPrice
	qty := pos.remainQty // use remaining qty, not initial (may have been partially closed)

	// Determine close side
	closeSide := "SELL"
	posSide := "LONG"
	if pos.side == "SHORT" {
		closeSide = "BUY"
		posSide = "SHORT"
	}

	levels := s.cfg.TPLevels
	splits := s.cfg.TPQtySplits
	if len(levels) == 0 || len(splits) == 0 || len(levels) != len(splits) {
		s.log.Error("staged TP: invalid TPLevels/TPQtySplits config")
		return
	}

	tps := make([]strategy.StagedTP, 0, len(levels))
	for i, lvl := range levels {
		var tpPrice float64
		if pos.side == "LONG" {
			tpPrice = math.Round((entry+lvl*R)*100) / 100
		} else {
			tpPrice = math.Round((entry-lvl*R)*100) / 100
		}
		// Each TP level uses its configured split. Remaining qty (e.g. 40%) is
		// managed by trailing stop + bounce TP, NOT placed as exchange orders.
		q := math.Floor(qty*splits[i]*1000) / 1000
		if q <= 0 { q = 0.001 }
		tps = append(tps, strategy.StagedTP{Price: tpPrice, Qty: q})
	}

	ok = ep.PlaceStagedTPOrders(s.cfg.Symbol, posSide, closeSide, pos.stopLoss, qty, tps)
	if ok {
		pos.stagedTPPlaced = true
		// Record TP levels for dynamic adjustment
		pos.stagedTPs = make([]stagedTPRecord, len(tps))
		for i, tp := range tps {
			pos.stagedTPs[i] = stagedTPRecord{
				Level: i + 1, Price: tp.Price, Qty: tp.Qty, Status: "pending",
			}
		}
		s.saveStagedTPsToRedis(pos)
		s.log.Info("AI: staged TP orders placed on exchange",
			zap.String("side", pos.side),
			zap.Float64("entry", entry), zap.Float64("R", R),
			zap.Float64("sl", pos.stopLoss),
			zap.Any("levels", levels), zap.Any("splits", splits),
		)
	}
}

// ─── Close Helpers ───────────────────────────────────────────────────────────

func (s *AIStrategy) closePos(ctx *strategy.Context, p *posState, pptr **posState, reason string) {
	qty := math.Floor(p.remainQty*1000) / 1000
	if qty <= 0 { *pptr = nil; return }

	// Check if the exchange already closed the position (algo SL/TP triggered via UDS).
	// In that case, skip placing a redundant close order — just clean up local state.
	// Verify with syncer that THIS specific side has no position, not just the global flag.
	if s.syncer != nil && s.syncer.PositionClosedExternally.Load() && !s.syncer.HasPosition(p.side) {
		s.log.Info("AI: position already closed by exchange — skipping close order",
			zap.String("side", p.side), zap.String("reason", reason))
		s.syncer.PositionClosedExternally.Store(false) // consume the flag
		s.syncRemove(p.side)
		*pptr = nil
		return
	}

	// If staged TP orders are on exchange, cancel them first (GPT reversal / manual close).
	if p.stagedTPPlaced {
		if ep, ok := ctx.Extra["staged_exit"].(strategy.StagedExitPlacer); ok {
			posSide := "LONG"
			if p.side == "SHORT" { posSide = "SHORT" }
			ep.CancelAllProtective(s.cfg.Symbol, posSide)
		}
		p.stagedTPPlaced = false
	}

	// Log grid orders being closed with base
	if len(p.gridOrders) > 0 {
		gridQty := 0.0
		for _, g := range p.gridOrders { if g.filled { gridQty += g.qty } }
		if gridQty > 0 {
			s.log.Info("AI: closing grid orders with base",
				zap.Int("layers", len(p.gridOrders)), zap.Float64("grid_qty", gridQty))
		}
	}

	// Protective closes use market order (must fill immediately).
	// Only GPT reversal uses limit (not time-critical, save on fees).
	useMarket := reason == "stop_loss" || reason == "trailing" || reason == "bounce_tp"
	s.placeCloseOrder(ctx, p.side, qty, useMarket)
	bars := s.primaryBars()
	closePrice := 0.0
	if len(bars) > 0 { closePrice = bars[len(bars)-1].Close }
	pnl := 0.0
	if p.side == "LONG" { pnl = (closePrice - p.entryPrice) * qty }
	if p.side == "SHORT" { pnl = (p.entryPrice - closePrice) * qty }
	s.log.Info("AI: CLOSE", zap.String("side", p.side), zap.String("reason", reason),
		zap.Float64("entry", p.entryPrice), zap.Float64("qty", qty), zap.Bool("market", useMarket),
		zap.Float64("est_pnl", pnl))
	s.logEvent("close", p.side, reason, closePrice, p.entryPrice, qty, 0, pnl, "")

	// Track hedge cooldown: if closing a Range position while opposite Trend exists
	if p.mode == modeRange {
		if (p.side == "LONG" && s.shortPos != nil && s.shortPos.mode == modeTrend) ||
			(p.side == "SHORT" && s.longPos != nil && s.longPos.mode == modeTrend) {
			s.lastHedgeClose = time.Now()
		}
	}

	// Reset signal accumulation on close so next entry requires fresh signals.
	s.accumLong = 0
	s.accumShort = 0

	s.syncRemove(p.side)
	*pptr = nil
}

func (s *AIStrategy) closePartial(ctx *strategy.Context, p *posState, pptr **posState, qty float64, reason string) {
	qty = math.Floor(qty*1000) / 1000
	if qty <= 0 { return }
	if qty > p.remainQty { qty = p.remainQty }

	s.placeCloseOrder(ctx, p.side, qty, false) // partial close always uses limit
	p.remainQty -= qty
	if p.remainQty <= 1e-10 {
		s.syncRemove(p.side)
		*pptr = nil
	} else {
		s.syncToRedis(p) // update qty in Redis
	}
}

// ─── Daily Risk ──────────────────────────────────────────────────────────────

func (s *AIStrategy) checkDayReset(ctx *strategy.Context, price float64) {
	now := time.Now()
	if now.YearDay() != s.dayStart.YearDay() || now.Year() != s.dayStart.Year() {
		s.dayStart = now
		if pf := ctx.Portfolio; pf != nil {
			s.dayStartEquity = pf.Equity(map[string]float64{s.cfg.Symbol: price})
		}
		s.dayHalted = false
		s.consecLoss = 0
		s.log.Info("AI: new day", zap.Float64("equity", s.dayStartEquity))
	}
	// Check daily loss limit
	if !s.dayHalted && s.dayStartEquity > 0 && s.cfg.MaxDailyLossPct > 0 {
		if pf := ctx.Portfolio; pf != nil {
			equity := pf.Equity(map[string]float64{s.cfg.Symbol: price})
			lossPct := (s.dayStartEquity - equity) / s.dayStartEquity
			if lossPct >= s.cfg.MaxDailyLossPct {
				s.dayHalted = true
				s.log.Warn("AI: daily loss limit reached — halting",
					zap.Float64("loss_pct", lossPct),
					zap.Float64("equity", equity),
					zap.Float64("start_equity", s.dayStartEquity))
			}
		}
	}
}

// canHedge returns true if the main position is in sufficient drawdown and cooldown has elapsed.
func (s *AIStrategy) canHedge(price float64, mainPos *posState) bool {
	if mainPos == nil || !mainPos.filled { return false }
	// Cooldown: don't hedge again too soon after last hedge closed
	if !s.lastHedgeClose.IsZero() && time.Since(s.lastHedgeClose) < s.cfg.HedgeCooldown {
		return false
	}
	// Check drawdown percentage
	var drawdownPct float64
	if mainPos.side == "LONG" {
		drawdownPct = (mainPos.entryPrice - price) / mainPos.entryPrice // positive when losing
	} else {
		drawdownPct = (price - mainPos.entryPrice) / mainPos.entryPrice // positive when losing
	}
	return drawdownPct >= s.cfg.HedgeDrawdownPct
}

// ─── Regime Detection ────────────────────────────────────────────────────────

// effectiveRisk returns risk-per-trade based on current exposure.
// Single direction: 2x configured risk (e.g. 4%); dual hedge: 1x (e.g. 2%).
func (s *AIStrategy) effectiveRisk(side string) float64 {
	hasOpposite := false
	if side == "LONG" && s.shortPos != nil && s.shortPos.filled { hasOpposite = true }
	if side == "SHORT" && s.longPos != nil && s.longPos.filled { hasOpposite = true }
	if hasOpposite {
		return s.cfg.RiskPerTrade // hedge mode: use base risk (2%)
	}
	return s.cfg.RiskPerTrade * 2 // single direction: double risk (4%)
}
