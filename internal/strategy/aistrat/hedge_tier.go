package aistrat

import (
	"math"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// ─── TODO #8: Tiered hedge (opposite-side delta compression) ────────────────
// When the main grid position's pnlR worsens past a tier threshold, ADD
// opposite-side hedge to reach the tier's target ratio. Tiers only upgrade:
// once a tier is claimed, it stays (pnlR recovery does NOT drop the tier).
//
// Hedge CLOSES based on either:
//   - Hedge-side trailing: hedge's unrealized profit has retreated >= HedgeTrailPeakPct
//     from its peak (lock gains near the bottom, avoid v-recovery loss).
//   - Main pnlR recovery: main recovered past HedgeMainRecoverPnlR (insurance
//     mission complete, let hedge go even at modest loss).
//
// This replaces the old tier-downgrade design which structurally locked a
// 0.2R × hedge_qty loss on every tier cycle.

// computePnlR returns pnlR from the ORIGINAL entry price (matches signal.go:180
// log convention). Note: after grid layers fill, this is NOT the true MTM per-R
// — true MTM would use weighted avg entry. But keeping original-entry convention:
//
//	(a) consistent with signal log and user mental model
//	(b) all tier/regime thresholds were calibrated against this formula in backtest
//
// Switching to avg-entry requires re-tuning thresholds (-1R → ~-0.7R, etc.).
func computePnlR(p *posState, price float64) float64 {
	if p.R <= 0 {
		return 0
	}
	if p.side == "LONG" {
		return (price - p.entryPrice) / p.R
	}
	return (p.entryPrice - price) / p.R
}

// computeHedgeFavor returns the hedge's unrealized profit in $ at given price.
// Positive = hedge is profitable.
func computeHedgeFavor(p *posState, price float64) float64 {
	if p.hedgeQty <= 0 {
		return 0
	}
	if p.side == "LONG" {
		// hedge is SHORT: profit when price < hedgeEntry
		return (p.hedgeEntry - price) * p.hedgeQty
	}
	// hedge is LONG: profit when price > hedgeEntry
	return (price - p.hedgeEntry) * p.hedgeQty
}

// selectHedgeTierForOpen returns the highest tier the current pnlR warrants,
// but never below currentTier (tier is a high-water mark).
// Returns (tier, targetRatio).
func selectHedgeTierForOpen(pnlR float64, currentTier int, cfg *Config) (int, float64) {
	if pnlR <= cfg.HedgeTier3PnlR && currentTier < 3 {
		return 3, cfg.HedgeTier3Ratio
	}
	if pnlR <= cfg.HedgeTier2PnlR && currentTier < 2 {
		return 2, cfg.HedgeTier2Ratio
	}
	if pnlR <= cfg.HedgeTier1PnlR && currentTier < 1 {
		return 1, cfg.HedgeTier1Ratio
	}
	// Keep current tier (with its ratio)
	switch currentTier {
	case 3:
		return 3, cfg.HedgeTier3Ratio
	case 2:
		return 2, cfg.HedgeTier2Ratio
	case 1:
		return 1, cfg.HedgeTier1Ratio
	}
	return 0, 0
}

// manageTieredHedge handles hedge escalation (open more) and close triggers.
// Called each primary bar from manageRange before the base TP check.
func (s *AIStrategy) manageTieredHedge(ctx *strategy.Context, bar exchange.Kline, p *posState) {
	if !s.cfg.GridHedgeEnabled {
		return
	}
	if p.mode != modeRange || p.R <= 0 {
		return
	}
	if !p.filled {
		return
	}
	// ── Problem 5 guard: main position already drained but hedge still open ──
	// If remainQty hit 0 via any path (external close, partial fills not routed
	// through closePos), force-close the orphaned hedge at market.
	if p.remainQty <= 0 {
		if p.hedgeQty > 0 {
			s.closeHedgeAll(ctx, p, bar.Close)
		}
		return
	}
	// ── Problem 4 guard: don't open hedge if opposite-side strategy position exists ──
	// On Binance hedge mode, hedge qty would merge into strategy's qty on the same
	// side. State tracking would conflict (we wouldn't know which qty is hedge vs
	// strategy on recovery). Only open hedge when that side is clear of strategy.
	// This is conservative: if user runs bidirectional grid, hedges won't fire.
	if p.hedgeQty <= 0 {
		if p.side == "LONG" && s.shortPos != nil {
			return
		}
		if p.side == "SHORT" && s.longPos != nil {
			return
		}
	}

	price := bar.Close
	pnlR := computePnlR(p, price)

	// ── Reopen cooldown: bar-count based. Handles engine restart gracefully:
	// if barCount < hedgeClosedBar (restarted, counter reset), treat as expired
	// cooldown (delta < 0 means we lost tracking, safer to allow reopen).
	// Using bar count (not wall clock) so backtest's simulated time works.
	delta := s.barCount - p.hedgeClosedBar
	cooldownActive := p.hedgeQty <= 0 && p.hedgeClosedBar > 0 && delta >= 0 &&
		s.cfg.HedgeReopenCooldownBars > 0 &&
		delta < s.cfg.HedgeReopenCooldownBars

	// ── Step 1: Tier escalation OR rebalance on layer fill ──
	// Handles two cases:
	//   (a) pnlR worsened past next tier → upgrade tier, bring hedge to new ratio
	//   (b) same tier but main grew (layer fill) → top up hedge to maintain ratio
	// Cooldown only blocks case (a) fresh opens from 0 qty; if hedge already exists,
	// rebalance is allowed (no cooldown check) because we're not "re-opening".
	newTier, targetRatio := selectHedgeTierForOpen(pnlR, p.hedgeTier, &s.cfg)
	currentTargetQty := math.Floor(p.remainQty*targetRatio*1000) / 1000
	tierUp := newTier > p.hedgeTier
	rebalanceUp := p.hedgeTier > 0 && p.hedgeQty > 0 && currentTargetQty > p.hedgeQty+0.0005
	if (tierUp && !cooldownActive) || rebalanceUp {
		targetQty := currentTargetQty
		if targetQty > p.hedgeQty+0.0005 {
			delta := math.Floor((targetQty-p.hedgeQty)*1000) / 1000
			if delta > 0 {
				hedgeSide := oppositeSide(p.side)
				orderID := s.placeOrder(ctx, hedgeSide, price, delta, false) // market
				if orderID == "" {
					s.log.Warn("AI: hedge open failed",
						zap.String("main_side", p.side), zap.Float64("delta", delta))
					return
				}
				// Update avg entry (optimistic; exchange fill confirms later).
				if p.hedgeQty <= 0 {
					p.hedgeEntry = price
				} else {
					p.hedgeEntry = (p.hedgeEntry*p.hedgeQty + price*delta) / (p.hedgeQty + delta)
				}
				p.hedgeQty += delta
				p.hedgeActive = true
				p.hedgeOrderID = orderID
				p.hedgeTier = newTier
				// Reset peak favor on size change — new baseline for trailing.
				p.hedgePeakFavor = computeHedgeFavor(p, price)
				s.log.Info("AI: tiered hedge ↑",
					zap.String("main_side", p.side),
					zap.Int("new_tier", newTier),
					zap.Float64("pnlR", pnlR),
					zap.Float64("delta_qty", delta),
					zap.Float64("hedge_total", p.hedgeQty),
					zap.Float64("hedge_avg", p.hedgeEntry),
					zap.Float64("price", price))
			}
		}
	}

	// ── Step 2: Track hedge profit peak ──
	if p.hedgeQty <= 0 {
		return
	}
	currentFavor := computeHedgeFavor(p, price)
	if currentFavor > p.hedgePeakFavor {
		p.hedgePeakFavor = currentFavor
	}

	// ── Step 3: Close criteria ──
	closeReason := ""

	// (a) Hedge-side trailing: peak profit retreated meaningfully.
	// Only arm when peak was material (avoid noise-triggered closes).
	if p.hedgePeakFavor >= s.cfg.HedgeMinPeakFavor {
		retreat := p.hedgePeakFavor - currentFavor
		retreatPct := retreat / p.hedgePeakFavor
		if retreatPct >= s.cfg.HedgeTrailPeakPct {
			closeReason = "hedge_trail"
		}
	}

	// (b) Main pnlR recovered past threshold — insurance no longer needed.
	// BUT only close at main_recover when hedge is at breakeven or profit; otherwise
	// a fast-drop-then-bounce (tier escalated at bottom, price reversed before favor
	// accumulated) would force a big hedge loss. Let hedge ride until either (a) fires
	// with profit, or main eventually closes (then closeHedgeAll handles it).
	if pnlR >= s.cfg.HedgeMainRecoverPnlR && currentFavor >= 0 {
		closeReason = "main_recover"
	}

	if closeReason != "" {
		qty := math.Floor(p.hedgeQty*1000) / 1000
		if qty <= 0 {
			return
		}
		hedgeSide := oppositeSide(p.side)
		if !s.placeCloseOrder(ctx, hedgeSide, qty, true, "hedge_"+closeReason) {
			s.log.Warn("AI: hedge close failed",
				zap.String("reason", closeReason), zap.Float64("qty", qty))
			return
		}
		// Locked P&L at this price.
		lockedPnl := currentFavor
		s.log.Info("AI: hedge closed",
			zap.String("reason", closeReason),
			zap.String("main_side", p.side),
			zap.Int("tier_at_close", p.hedgeTier),
			zap.Float64("pnlR", pnlR),
			zap.Float64("qty", qty),
			zap.Float64("peak_favor", p.hedgePeakFavor),
			zap.Float64("locked_pnl", lockedPnl),
			zap.Float64("price", price))
		p.hedgeQty = 0
		p.hedgeActive = false
		p.hedgeEntry = 0
		p.hedgeTier = 0
		p.hedgePeakFavor = 0
		p.hedgeOrderID = ""
		p.hedgeClosedBar = s.barCount
	}
}

// closeHedgeAll liquidates any open hedge. Called when main position fully closes
// via any path (grid TP, trail lock, external close, etc.).
func (s *AIStrategy) closeHedgeAll(ctx *strategy.Context, p *posState, price float64) {
	if p.hedgeQty <= 0 {
		return
	}
	hedgeSide := oppositeSide(p.side)
	qty := math.Floor(p.hedgeQty*1000) / 1000
	if qty <= 0 {
		return
	}
	if !s.placeCloseOrder(ctx, hedgeSide, qty, true, "hedge_on_main_close") {
		s.log.Warn("AI: hedge final close failed",
			zap.String("hedge_side", hedgeSide), zap.Float64("qty", qty))
		return
	}
	lockedPnl := computeHedgeFavor(p, price)
	s.log.Info("AI: hedge fully unwound on main close",
		zap.String("main_side", p.side),
		zap.Float64("qty", qty),
		zap.Float64("locked_pnl", lockedPnl))
	p.hedgeQty = 0
	p.hedgeActive = false
	p.hedgeEntry = 0
	p.hedgeTier = 0
	p.hedgePeakFavor = 0
	p.hedgeOrderID = ""
	p.hedgeClosedBar = s.barCount
}

func oppositeSide(side string) string {
	if side == "LONG" {
		return "SHORT"
	}
	return "LONG"
}

// ─── TODO #11: Grid profit-lock trailing ─────────────────────────────────────
// Activates ONCE pnlR first reaches GridTrailActivatePnlR. After activation,
// tracks peak pnlR and closes the main position if pnlR retreats by
// GridTrailPullbackR from the peak. Never triggers while pnlR < 0.

// manageGridTrail checks profit-lock trailing and closes the main position if triggered.
// Returns true if a close was initiated (caller should skip further processing).
func (s *AIStrategy) manageGridTrail(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) bool {
	if !s.cfg.GridTrailEnabled {
		return false
	}
	if p.mode != modeRange || p.R <= 0 {
		return false
	}
	if !p.filled || p.remainQty <= 0 {
		return false
	}

	price := bar.Close
	pnlR := computePnlR(p, price)

	// Activation: first time crossing activate threshold.
	if !p.gridTrailActive {
		if pnlR >= s.cfg.GridTrailActivatePnlR {
			p.gridTrailActive = true
			p.gridTrailPeakPnlR = pnlR
			s.log.Info("AI: grid profit-lock trail activated",
				zap.String("side", p.side),
				zap.Float64("pnlR", pnlR),
				zap.Float64("price", price))
		}
		return false
	}

	// Track peak.
	if pnlR > p.gridTrailPeakPnlR {
		p.gridTrailPeakPnlR = pnlR
	}

	// Trigger on pullback from peak.
	pullback := p.gridTrailPeakPnlR - pnlR
	if pullback >= s.cfg.GridTrailPullbackR {
		s.log.Info("AI: grid profit-lock trail triggered",
			zap.String("side", p.side),
			zap.Float64("peak_pnlR", p.gridTrailPeakPnlR),
			zap.Float64("current_pnlR", pnlR),
			zap.Float64("pullback", pullback),
			zap.Float64("price", price))
		// closePos unwinds hedge internally.
		s.closePos(ctx, p, pptr, "grid_trail_lock")
		return true
	}
	return false
}

// ─── Grid regime-break exit ──────────────────────────────────────────────────
// A grid/fade position bleeds when the market TRENDS against it. Track consecutive
// primary bars where the regime opposes the position; once that streak reaches
// GridRegimeExitBars AND pnlR is at/below GridRegimeExitPnlR, bail. The streak
// resets whenever the regime is neutral or aligned — only a sustained adverse trend
// triggers the exit.

// regimeOpposesGrid reports whether the current regime is a trend running against p.
// Only trending regimes (STRONG_TREND / EXPANSION) count; a trend toward the position
// is aligned (helps the grid), a trend away is adverse.
func (s *AIStrategy) regimeOpposesGrid(p *posState) bool {
	if s.lastRegime != RegimeStrongTrend && s.lastRegime != RegimeExpansion {
		return false
	}
	if p.side == "LONG" {
		return s.lastTrendDir < 0 // downtrend vs a long grid
	}
	return s.lastTrendDir > 0 // uptrend vs a short grid
}

// manageGridRegimeExit closes the main position after a sustained adverse-regime
// streak at a loss. Returns true if a close was initiated (caller skips further).
func (s *AIStrategy) manageGridRegimeExit(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) bool {
	if !s.cfg.GridRegimeExitEnabled || s.cfg.GridRegimeExitBars <= 0 {
		return false
	}
	if p.mode != modeRange || p.R <= 0 {
		return false
	}
	if !p.filled || p.remainQty <= 0 {
		return false
	}

	if s.regimeOpposesGrid(p) {
		p.adverseRegimeBars++
	} else {
		p.adverseRegimeBars = 0 // neutral/aligned → reset the streak
		return false
	}

	pnlR := computePnlR(p, bar.Close)
	if p.adverseRegimeBars >= s.cfg.GridRegimeExitBars && pnlR <= s.cfg.GridRegimeExitPnlR {
		s.log.Info("AI: grid regime-break exit",
			zap.String("side", p.side),
			zap.String("regime", string(s.lastRegime)),
			zap.Int("trend_dir", s.lastTrendDir),
			zap.Int("adverse_bars", p.adverseRegimeBars),
			zap.Float64("pnlR", pnlR),
			zap.Float64("price", bar.Close))
		s.closePos(ctx, p, pptr, "grid_regime_exit")
		return true
	}
	return false
}
