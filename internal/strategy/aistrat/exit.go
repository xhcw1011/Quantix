package aistrat

import (
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
)

// placeStagedExitOrders places a single exchange-native TP limit order.
//
// Default: TP at 1.5R (50% qty). Trend: TP at 2.0R (50% qty).
// ATR-adaptive: min(R*level, ATR*3) — tightens in low volatility.
// Remaining 50% managed by trailing stop + bounce TP.
// Breakeven triggered by pnlR >= BreakevenR (code-level, not TP-dependent).
func (s *AIStrategy) placeStagedExitOrders(ctx *strategy.Context, pos *posState) {
	ep, ok := ctx.Extra["staged_exit"].(strategy.StagedExitPlacer)
	if !ok {
		s.log.Warn("staged exit placer not available (paper/backtest mode), using local management")
		return
	}
	s.stagedEP = ep

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

	// Select TP levels based on entry regime: wider targets for strong trends.
	levels := s.cfg.TPLevels
	splits := s.cfg.TPQtySplits
	tpMode := "default"
	if (pos.entryRegime == RegimeStrongTrend || pos.entryRegime == RegimeExpansion) &&
		len(s.cfg.TrendTPLevels) > 0 && len(s.cfg.TrendTPQtySplits) > 0 {
		levels = s.cfg.TrendTPLevels
		splits = s.cfg.TrendTPQtySplits
		tpMode = "trend"
	}
	if len(levels) == 0 || len(splits) == 0 || len(levels) != len(splits) {
		s.log.Error("staged TP: invalid TPLevels/TPQtySplits config")
		return
	}

	// TP distance: prefer GPT support/resistance level, fallback to R × level.
	tps := make([]strategy.StagedTP, 0, len(levels))
	for i, lvl := range levels {
		dist := lvl * R // default R-based
		// Use GPT TP if available and in valid range
		if pos.gptTPPrice > 0 {
			var gptDist float64
			if pos.side == "LONG" {
				gptDist = pos.gptTPPrice - entry
			} else {
				gptDist = entry - pos.gptTPPrice
			}
			if gptDist >= s.cfg.GptTPMinR*R && gptDist <= s.cfg.GptTPMaxR*R {
				dist = gptDist
			}
		}
		if dist < s.cfg.GptTPMinR*R { dist = s.cfg.GptTPMinR * R }

		var tpPrice float64
		if pos.side == "LONG" {
			tpPrice = math.Round((entry+dist)*100) / 100
		} else {
			tpPrice = math.Round((entry-dist)*100) / 100
		}
		q := math.Floor(qty*splits[i]*1000) / 1000
		if q <= 0 { q = 0.001 }
		tps = append(tps, strategy.StagedTP{Price: tpPrice, Qty: q})
	}

	s.log.Info("AI: TP calculation",
		zap.Float64("R", R), zap.Float64("entryATR", pos.entryATR),
		zap.Float64("gptTP", pos.gptTPPrice),
		zap.Any("levels", levels), zap.Any("tp_prices", tps))

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
			zap.String("tp_mode", tpMode),
			zap.String("regime", string(pos.entryRegime)),
			zap.Float64("entry", entry), zap.Float64("R", R),
			zap.Float64("sl", pos.stopLoss),
			zap.Any("levels", levels), zap.Any("splits", splits),
		)
	}
}

// ─── Close Helpers ───────────────────────────────────────────────────────────

func (s *AIStrategy) closePos(ctx *strategy.Context, p *posState, pptr **posState, reason string) {
	qty := math.Floor(p.remainQty*1000) / 1000
	// Reconcile the close qty to EXCHANGE truth (the position syncer). The
	// strategy-internal remainQty drifts from the real position (missed/async
	// fills, desync), so closing it can leave an orphan on the exchange (the
	// 31-ETH case: strategy thought 3.1, exchange had 9.6). Close what the
	// exchange actually holds on this side so the position is fully flattened.
	if s.syncer != nil {
		var exQty float64
		if p.side == "LONG" {
			if lp := s.syncer.GetLong(); lp != nil {
				exQty = math.Floor(lp.Qty*1000) / 1000
			}
		} else {
			if sp := s.syncer.GetShort(); sp != nil {
				exQty = math.Floor(sp.Qty*1000) / 1000
			}
		}
		if exQty > 0 && math.Abs(exQty-qty) > 0.001 {
			s.log.Warn("AI: closePos reconciling to exchange-truth qty (desync)",
				zap.String("side", p.side), zap.Float64("strategy_qty", qty),
				zap.Float64("exchange_qty", exQty), zap.String("reason", reason))
			qty = exQty
		}
	}
	if qty <= 0 { *pptr = nil; return }

	// Check if the exchange already closed the position (algo SL/TP triggered via UDS).
	// In that case, skip placing a redundant close order — just clean up local state.
	// Verify with syncer that THIS specific side has no position, not just the global flag.
	if s.syncer != nil && s.syncer.PositionClosedExternally.Load() && !s.syncer.HasPosition(p.side) {
		s.log.Info("AI: position already closed by exchange — skipping close order",
			zap.String("side", p.side), zap.String("reason", reason))
		s.syncer.PositionClosedExternally.Store(false)
		// Still cancel any TP/SL orders on exchange (they're now orphaned)
		if p.stagedTPPlaced {
			if ep, ok := ctx.Extra["staged_exit"].(strategy.StagedExitPlacer); ok {
				posSide := "LONG"
				if p.side == "SHORT" { posSide = "SHORT" }
				ep.CancelAllProtective(s.cfg.Symbol, posSide)
			}
		}
		s.accumLong = 0
		s.accumShort = 0
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

	// Cancel unfilled grid orders on exchange + log filled ones
	if len(p.gridOrders) > 0 {
		gridQty := 0.0
		for _, g := range p.gridOrders {
			if g.filled {
				gridQty += g.qty
			} else if g.orderID != "" {
				ctx.CancelOrder(g.orderID)
				s.log.Info("AI: cancelled unfilled grid order", zap.String("id", g.orderID))
			}
		}
		if gridQty > 0 {
			s.log.Info("AI: closing grid orders with base",
				zap.Int("layers", len(p.gridOrders)), zap.Float64("grid_qty", gridQty))
		}
	}

	// Grid TP: price is at our target, use limit order for maker fee (0.02% vs 0.05%).
	// All other exits (SL, trailing, regime_exit): use market for guaranteed fill.
	useMarket := true
	if reason == "grid_tp" {
		useMarket = false
	}
	if !s.placeCloseOrder(ctx, p.side, qty, useMarket) {
		// Close order FAILED. Check syncer: if exchange has no position,
		// the position was already closed (manual, liquidation, TP fill, etc.) — safe to clear state.
		if s.syncer != nil && !s.syncer.HasPosition(p.side) {
			s.log.Warn("AI: CLOSE FAILED but exchange has no position — clearing state",
				zap.String("side", p.side), zap.String("reason", reason))
			s.accumLong = 0
			s.accumShort = 0
			s.syncRemove(p.side)
			*pptr = nil
			return
		}
		// Syncer still shows position — could be stale (UDS delay).
		// Track consecutive failures. After 3 failures (30s), assume position is gone
		// to prevent ReduceOnly reject spam (Binance -2022).
		p.closeFailCount++
		if p.closeFailCount >= 3 {
			s.log.Warn("AI: CLOSE FAILED 3x — assuming position closed externally, clearing state",
				zap.String("side", p.side), zap.String("reason", reason))
			s.accumLong = 0
			s.accumShort = 0
			s.syncRemove(p.side)
			*pptr = nil
			return
		}
		s.log.Error("AI: CLOSE FAILED — will retry",
			zap.String("side", p.side), zap.String("reason", reason),
			zap.Int("fail_count", p.closeFailCount))
		s.lastCloseFailAt = time.Now()
		return
	}
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
	s.lastCloseFailAt = time.Time{} // clear throttle on success

	s.syncRemove(p.side)
	*pptr = nil
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
	// Check for transfer-related balance changes — adjust dayStart
	// to prevent false daily-loss halts when user moves funds in/out.
	if adj, ok := ctx.Extra["equity_adjusted"].(float64); ok && adj > 0 {
		old := s.dayStartEquity
		s.log.Info("AI: dayStartEquity adjusted for transfer",
			zap.Float64("old", old), zap.Float64("new", adj))
		s.dayStartEquity = adj
		// Only auto-clear halt if equity INCREASED (deposit), not on withdraw
		if s.dayHalted && adj > old {
			s.dayHalted = false
			s.log.Info("AI: daily halt cleared — equity increased (deposit)")
		}
		delete(ctx.Extra, "equity_adjusted")
	}
	// Sanity check: if equity is much lower than dayStartEquity but we haven't traded,
	// it's likely a transfer out or manual close — reset dayStart to avoid false halt.
	if pf := ctx.Portfolio; pf != nil && s.dayStartEquity > 0 {
		equity := pf.Equity(map[string]float64{s.cfg.Symbol: price})
		if equity > 0 && equity < s.dayStartEquity*0.5 && s.longPos == nil && s.shortPos == nil {
			s.log.Warn("AI: dayStartEquity seems stale (>50% drop with no positions) — resetting",
				zap.Float64("old_start", s.dayStartEquity), zap.Float64("current", equity))
			s.dayStartEquity = equity
		}
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

