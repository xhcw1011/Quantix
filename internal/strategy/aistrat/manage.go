package aistrat

import (
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
	"github.com/Quantix/quantix/internal/strategy"
)

// trendCutTriggered reports whether a range/grid position should be cut early on
// trend confirmation instead of riding to the -3R catastrophic stop. Two gates:
// (a) underwater past trendCutR (a negative R threshold; >=0 disables the feature),
// and (b) the 1h trend (hourlyDir: +1 bull, -1 bear, 0 neutral) confirmed AGAINST
// the position's side. A dip with no confirmed trend (hourlyDir==0) keeps full -3R
// room — this cuts only genuinely-wrong-direction trades, not normal reversion dips.
func trendCutTriggered(side string, pnlR, trendCutR float64, hourlyDir int) bool {
	if trendCutR >= 0 {
		return false // disabled
	}
	if pnlR > trendCutR {
		return false // not underwater past the threshold
	}
	switch side {
	case "LONG":
		return hourlyDir < 0 // downtrend confirmed against the long
	case "SHORT":
		return hourlyDir > 0 // uptrend confirmed against the short
	}
	return false
}

// ─── Position Management (every bar) ─────────────────────────────────────────

func (s *AIStrategy) managePos(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	price := bar.Close
	s.lastPrice = price // latest close (live+backtest); used to unwind hedge on any close path
	iv := bar.Interval
	if iv == "" {
		iv = s.cfg.PrimaryInterval
	}
	isPrimary := iv == s.cfg.PrimaryInterval
	if isPrimary {
		p.barsHeld++
	}

	// Limit order pending (check on primary bars only)
	if !p.filled {
		if isPrimary && s.barCount-p.limitBar > s.cfg.LimitTimeoutBars {
			s.log.Warn("AI: limit timeout — cancelling", zap.String("side", p.side), zap.String("id", p.orderID))
			if p.orderID != "" {
				ctx.CancelOrder(p.orderID)
			}
			if p.filled {
				s.log.Info("AI: limit order partially/fully filled before cancel, keeping position")
				return
			}
			s.syncRemove(p.side)
			*pptr = nil
			return
		}
		return
	}

	// Update peak
	if p.side == "LONG" && price > p.peakPrice {
		p.peakPrice = price
	}
	if p.side == "SHORT" && price < p.peakPrice {
		p.peakPrice = price
	}

	// ── Catastrophic stop (ALL modes incl range/grid/hedge) ──
	// Range/grid/hedge positions have no normal SL (they ride the range), but must
	// still be cut when price blows far past the range into a sustained trend.
	// Caps any single position's loss near CatastrophicStopR instead of riding to
	// -15~20R until the staleness timer dumps it. Negative cfg value enables it.
	if p.filled && p.R > 0 && s.cfg.CatastrophicStopR < 0 {
		catPnlR := 0.0
		if p.side == "LONG" {
			catPnlR = (price - p.entryPrice) / p.R
		} else {
			catPnlR = (p.entryPrice - price) / p.R
		}
		if catPnlR <= s.cfg.CatastrophicStopR {
			s.log.Warn("CATASTROPHIC STOP — cutting position to cap loss",
				zap.String("side", p.side), zap.Int("mode", int(p.mode)),
				zap.Float64("price", price), zap.Float64("entry", p.entryPrice),
				zap.Float64("pnl_r", catPnlR), zap.Float64("limit_r", s.cfg.CatastrophicStopR))
			closedSide := p.side
			s.closePos(ctx, p, pptr, "catastrophic_stop")
			s.consecLoss++
			s.stopBar = s.barCount
			s.postSLReeval = true
			s.postSLSide = closedSide
			s.postSLPrice = price
			return
		}
	}

	// ── Trend-confirmed early cut (range/grid positions only) ──
	// Range/grid positions have no normal SL — they ride the range waiting for
	// reversion. If one is underwater past TrendCutR AND the 1h trend is now
	// confirmed AGAINST it, the reversion thesis is broken: cut now instead of
	// riding to the -3R catastrophic backstop. The trend gate keeps normal
	// pre-reversion dips (hourlyTrendDir==0) on full -3R room, so only genuinely
	// wrong-direction trades are cut. Enabled per-engine via TrendCutR (0 = off).
	if p.filled && p.R > 0 && p.mode == modeRange && s.cfg.TrendCutR < 0 {
		pnlR := (price - p.entryPrice) / p.R
		if p.side == "SHORT" {
			pnlR = (p.entryPrice - price) / p.R
		}
		dir := s.hourlyTrendDir()
		if trendCutTriggered(p.side, pnlR, s.cfg.TrendCutR, dir) {
			s.log.Warn("TREND CUT — reversion thesis broken, cutting on confirmed adverse trend",
				zap.String("side", p.side), zap.Int("mode", int(p.mode)),
				zap.Float64("price", price), zap.Float64("entry", p.entryPrice),
				zap.Float64("pnl_r", pnlR), zap.Float64("cut_r", s.cfg.TrendCutR),
				zap.Int("hourly_dir", dir))
			closedSide := p.side
			s.closePos(ctx, p, pptr, "trend_cut")
			s.consecLoss++
			s.stopBar = s.barCount
			s.postSLReeval = true
			s.postSLSide = closedSide
			s.postSLPrice = price
			return
		}
	}

	// ── Stop-loss (trend only — grid positions have no SL, ride the range) ──
	if p.mode != modeRange {
		if (p.side == "LONG" && price <= p.stopLoss) || (p.side == "SHORT" && price >= p.stopLoss) {
			s.log.Warn("STOP-LOSS", zap.String("side", p.side), zap.Float64("price", price), zap.Float64("stop", p.stopLoss))
			closedSide := p.side
			s.closePos(ctx, p, pptr, "stop_loss")
			s.consecLoss++
			s.stopBar = s.barCount
			s.postSLReeval = true
			s.postSLSide = closedSide
			s.postSLPrice = price
			return
		}
	}

	if p.barsHeld < s.cfg.MinHoldBars {
		return
	} // minimum hold

	// Range/grid positions: check TP + manage grid layers (no trailing)
	if p.mode == modeRange {
		s.manageRange(ctx, bar, p, pptr)
		return
	}
	s.manageTrend(ctx, bar, p, pptr)
}

// manageRange handles Range/grid positions: fixed TP at BB middle, grid layers on drawdown.
// No SL — grid rides the range. Risk managed by small qty per layer + max layers cap + regime exit.
func (s *AIStrategy) manageRange(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	price := bar.Close

	// ── Staleness exit: release slot when grid can't recover ──
	// Range positions have no SL by design. If a grid stays stuck for
	// GridStaleBars market-time bars at deeper than GridStalePnlR drawdown,
	// it blocks new signals and effectively halts the engine. Force-close
	// to release the slot. Uses barsHeld (works in both live and backtest;
	// initialized from DB created_at on restart, see helpers.go).
	staleBars := s.cfg.GridStaleBars
	stalePnlR := s.cfg.GridStalePnlR
	if staleBars > 0 && stalePnlR < 0 && p.filled && p.R > 0 && p.barsHeld > staleBars {
		pnlR := 0.0
		if p.side == "LONG" {
			pnlR = (price - p.entryPrice) / p.R
		} else {
			pnlR = (p.entryPrice - price) / p.R
		}
		if pnlR < stalePnlR {
			s.log.Warn("AI: GRID staleness exit — slot held too long at deep drawdown",
				zap.String("side", p.side),
				zap.Int("bars_held", p.barsHeld),
				zap.Float64("pnl_r", pnlR),
				zap.Float64("entry", p.entryPrice),
				zap.Float64("price", price))
			s.closePos(ctx, p, pptr, "stale_exit")
			s.consecLoss++
			return
		}
	}

	// ── feat/aistrat-hedge additive hooks (all default OFF → zero behavior change) ──
	// Run before the base TP check. The first two may close the position (return early);
	// the tiered hedge only adds/manages opposite-side delta and never closes the main.
	if s.manageGridTrail(ctx, bar, p, pptr) {
		return
	}
	if s.manageGridRegimeExit(ctx, bar, p, pptr) {
		return
	}
	s.manageTieredHedge(ctx, bar, p)

	// ── Base position TP check ──
	if p.takeProfit > 0 {
		tpHit := false
		if p.side == "LONG" && price >= p.takeProfit {
			tpHit = true
		}
		if p.side == "SHORT" && price <= p.takeProfit {
			tpHit = true
		}
		if tpHit {
			s.log.Info("AI: GRID TP hit",
				zap.String("side", p.side), zap.Float64("entry", p.entryPrice),
				zap.Float64("tp", p.takeProfit), zap.Float64("price", price))
			s.closePos(ctx, p, pptr, "grid_tp")
			s.consecLoss = 0
			return
		}
	}

	// ── Grid layer management: add layers on drawdown, close on layer TP ──
	s.manageGrid(ctx, bar, p, pptr)
}

func (s *AIStrategy) manageGrid(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	if s.cfg.GridMaxLayers <= 0 {
		return
	}
	price := bar.Close

	// 1. Check existing grid orders for TP or fill
	for i := len(p.gridOrders) - 1; i >= 0; i-- {
		g := p.gridOrders[i]

		// Pending grid order — check if filled (limit order hit price level)
		if !g.filled {
			if (p.side == "LONG" && price <= g.entryPrice) || (p.side == "SHORT" && price >= g.entryPrice) {
				g.filled = true
				g.filledAt = s.now()
				p.remainQty += g.qty // only count qty after confirmed fill
				s.log.Info("AI: grid order filled",
					zap.String("side", p.side), zap.Float64("entry", g.entryPrice),
					zap.Float64("qty", g.qty), zap.Int("layer", i+1))
			}
			continue
		}

		// Filled grid order — check TP
		gridProfit := false
		if p.side == "LONG" && price >= g.tp {
			gridProfit = true
		}
		if p.side == "SHORT" && price <= g.tp {
			gridProfit = true
		}

		if gridProfit {
			s.log.Info("AI: grid TP hit",
				zap.String("side", p.side), zap.Float64("entry", g.entryPrice),
				zap.Float64("tp", g.tp), zap.Float64("price", price),
				zap.Float64("qty", g.qty), zap.Int("layer", i+1))
			if !s.placeCloseOrder(ctx, p.side, g.qty, false) {
				s.log.Warn("AI: grid close order failed", zap.Int("layer", i+1))
				continue // skip removal, retry next bar
			}
			p.remainQty -= g.qty
			// Remove this grid order
			p.gridOrders = append(p.gridOrders[:i], p.gridOrders[i+1:]...)
		}
	}

	// 2. Open new grid order if price moved far enough from last level
	if len(p.gridOrders) >= s.cfg.GridMaxLayers {
		return
	}
	if !p.filled {
		return
	} // base must be filled first
	if s.cfg.ForceTrend {
		return
	}

	// Dynamic grid spacing: compute FRESH BB each bar (not stale lastBB values).
	// Floor at configured GridSpacingPct to prevent layers from stacking in tight
	// consolidation (low BB width) — when BB collapses, dynamic spacing would
	// drop to $1-2 and fill every layer in a single bar.
	fixedSpacing := p.entryPrice * s.cfg.GridSpacingPct
	spacing := fixedSpacing
	closes := s.getCloses()
	if len(closes) >= 20 {
		bb := indicator.BollingerBands(closes, s.cfg.BBPeriod, s.cfg.BBStdDev)
		if len(bb.Upper) > 0 && len(bb.Lower) > 0 {
			bbWidth := bb.Upper[len(bb.Upper)-1] - bb.Lower[len(bb.Lower)-1]
			dynamicSpacing := bbWidth / float64(s.cfg.GridMaxLayers+1)
			if dynamicSpacing > fixedSpacing {
				spacing = dynamicSpacing
			}
		}
	}

	refPrice := p.entryPrice
	if len(p.gridOrders) > 0 {
		last := p.gridOrders[len(p.gridOrders)-1]
		refPrice = last.entryPrice
	}

	shouldAdd := false
	var gridEntry, gridTP float64

	// Pyramid (add only on the WINNING side) — never average into a loser.
	// LONG: add when price has risen a step ABOVE the last entry (thesis confirming).
	// SHORT: add when price has fallen a step BELOW the last entry.
	if p.side == "LONG" && price >= refPrice+spacing {
		gridEntry = math.Round(price*100) / 100
		// Grid layer TP: base position's TP (BB middle), not fixed percentage
		gridTP = p.takeProfit
		if gridTP <= gridEntry {
			gridTP = math.Round((gridEntry+spacing)*100) / 100
		}
		shouldAdd = true
	}
	if p.side == "SHORT" && price <= refPrice-spacing {
		gridEntry = math.Round(price*100) / 100
		gridTP = p.takeProfit
		if gridTP >= gridEntry {
			gridTP = math.Round((gridEntry-spacing)*100) / 100
		}
		shouldAdd = true
	}

	if !shouldAdd {
		return
	}

	gridQty := math.Floor(p.initQty*s.cfg.GridQtyRatio*1000) / 1000
	if gridQty <= 0 {
		return
	}
	totalQty := p.remainQty + gridQty
	if totalQty > p.initQty*2 {
		return
	}

	// Use limit order for grid layers (maker fee, 60% cheaper than market)
	omsID := s.placeOrder(ctx, p.side, gridEntry, gridQty, true)
	if omsID == "" {
		return
	}

	g := &gridOrder{
		entryPrice: gridEntry, qty: gridQty, tp: gridTP,
		filled: false, orderID: omsID, limitBar: s.barCount,
	}
	p.gridOrders = append(p.gridOrders, g)
	// Don't add to remainQty yet — wait for fill confirmation in the price check loop above.

	s.log.Info("AI: grid order placed (limit)",
		zap.String("side", p.side), zap.Float64("entry", gridEntry),
		zap.Float64("tp", gridTP), zap.Float64("qty", gridQty),
		zap.Int("layer", len(p.gridOrders)))
}

func (s *AIStrategy) manageTrend(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	if p.barsHeld < s.cfg.MinTrendBars {
		return
	}

	price := bar.Close
	liveATR := s.calcATR()

	// R-based profit measurement
	pnlR := 0.0
	if p.R > 0 {
		if p.side == "LONG" {
			pnlR = (price - p.entryPrice) / p.R
		}
		if p.side == "SHORT" {
			pnlR = (p.entryPrice - price) / p.R
		}
	}

	// ── 1h mode-based trailing (bar-level, mirrors tickManage) ──
	atr := math.Max(p.entryATR, liveATR)
	hMode := s.detectHourlyMode(p.side)

	// Time-based exit: close if held > 3h and NOT in strong trend.
	// Prevents slow grind losses in weak/range markets.
	if p.filled && hMode != hourlyTrendStrong && s.now().Sub(p.filledAt) > 3*time.Hour {
		s.log.Warn("BAR: time exit — held >3h in weak/exit mode",
			zap.String("side", p.side), zap.Float64("pnlR", pnlR))
		s.closePos(ctx, p, pptr, "time_exit")
		if pnlR > 0 {
			s.consecLoss = 0
		} else {
			s.consecLoss++
		}
		return
	}

	updateTrail := true
	var trailDist float64
	floor := p.stopLoss

	switch hMode {
	case hourlyTrendStrong:
		// Two-tier trailing: tight (1ATR) when pnlR<2, wide (3ATR) when pnlR≥2.
		if pnlR > 0 && math.Abs(price-p.entryPrice) < atr {
			updateTrail = false
		}
		tier := 1
		trailDist = atr
		if pnlR >= 2.0 {
			tier = 2
			trailDist = atr * s.cfg.TrailingATRK
		}
		// Use SL as floor initially; only upgrade to breakeven after 0.3R profit.
		// Prevents immediate breakeven stop when position hasn't moved in favor yet.
		floor = p.stopLoss
		if pnlR >= 0.3 {
			floor = p.entryPrice // breakeven floor
		}
		// Tier upgrade: allow trailing to widen.
		if tier > p.trailTier && p.trailTier > 0 {
			// Reset trailing to wide value (computed in block below).
			var wideTrail float64
			if p.side == "LONG" {
				wideTrail = p.peakPrice - trailDist
				if wideTrail < floor {
					wideTrail = floor
				}
			} else {
				wideTrail = p.peakPrice + trailDist
				if wideTrail > floor {
					wideTrail = floor
				}
			}
			p.trailing = math.Round(wideTrail*100) / 100
			s.syncToRedis(p)
		}
		p.trailTier = tier
	case hourlyExitMode:
		trailDist = atr * 1.0 // tighter than WEAK (1.5) but not so tight it jump-triggers
		if pnlR >= 0.3 {
			lockR := math.Max(pnlR*0.5, 0.02)
			if p.side == "LONG" {
				floor = p.entryPrice + lockR*p.R
			}
			if p.side == "SHORT" {
				floor = p.entryPrice - lockR*p.R
			}
		}
	default: // hourlyTrendWeak — profit < 1 ATR → skip trailing
		if pnlR > 0 && math.Abs(price-p.entryPrice) < atr {
			updateTrail = false
		}
		trailDist = atr * s.cfg.TrailingATRK * 0.5
		if s.cfg.BreakevenR > 0 && p.R > 0 {
			lockR := 0.0
			switch {
			case pnlR >= 0.8:
				lockR = 0.4
			case pnlR >= 0.5:
				lockR = 0.2
			case pnlR >= 0.3:
				lockR = 0.02
			}
			if lockR > 0 {
				if p.side == "LONG" {
					floor = p.entryPrice + lockR*p.R
				}
				if p.side == "SHORT" {
					floor = p.entryPrice - lockR*p.R
				}
			}
		}
	}

	if updateTrail {
		var newTrail float64
		if p.side == "LONG" {
			newTrail = p.peakPrice - trailDist
			if newTrail < floor {
				newTrail = floor
			}
		} else {
			newTrail = p.peakPrice + trailDist
			if newTrail > floor {
				newTrail = floor
			}
		}
		newTrail = math.Round(newTrail*100) / 100
		moved := false
		if p.side == "LONG" && newTrail > p.trailing {
			p.trailing = newTrail
			moved = true
		}
		if p.side == "SHORT" && (p.trailing == 0 || newTrail < p.trailing) {
			p.trailing = newTrail
			moved = true
		}
		if moved {
			s.syncToRedis(p)
		}
	}

	// ── Bounce TP: if price bounced 0.8R from extreme, close remaining ──
	// Detects trend exhaustion — price made a new extreme then reversed.
	// 0.8R (~14 points ETH) gives enough room for normal oscillation without premature exit.
	if p.remainQty < p.initQty && p.remainQty > 0 { // only after some TPs filled
		bounceThreshold := s.cfg.BounceTPR * p.R
		if p.side == "LONG" && p.peakPrice-price >= bounceThreshold && pnlR > 0 {
			s.log.Info("AI: bounce TP — price retreated from peak",
				zap.Float64("peak", p.peakPrice), zap.Float64("price", price), zap.Float64("pnlR", pnlR))
			s.closePos(ctx, p, pptr, "bounce_tp")
			s.consecLoss = 0
			return
		}
		if p.side == "SHORT" && price-p.peakPrice >= bounceThreshold && pnlR > 0 {
			s.log.Info("AI: bounce TP — price bounced from low",
				zap.Float64("peak", p.peakPrice), zap.Float64("price", price), zap.Float64("pnlR", pnlR))
			s.closePos(ctx, p, pptr, "bounce_tp")
			s.consecLoss = 0
			return
		}
	}

	// ── Local SL check (backup for exchange SL) ──
	if p.side == "LONG" && p.trailing > p.stopLoss && price <= p.trailing {
		s.closePos(ctx, p, pptr, "trailing")
		if pnlR > 0 {
			s.consecLoss = 0
		}
		return
	}
	if p.side == "SHORT" && p.trailing > 0 && p.trailing < p.stopLoss && price >= p.trailing {
		s.closePos(ctx, p, pptr, "trailing")
		if pnlR > 0 {
			s.consecLoss = 0
		}
		return
	}

	// ── Tech reversal check — only when losing, and not on first few bars after restart ──
	// barCount < 3 after restart: regime/1h mode not yet stable, skip reversal.
	if pnlR < 1.0 && s.barCount >= 3 && p.barsHeld >= s.cfg.MinTrendBars {
		s.checkReversal(ctx, bar, p, pptr)
	}
}

func (s *AIStrategy) checkReversal(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	// Technical reversal: check if the opposite direction's tech signal is strong.
	reverseConf := 0.0
	if p.side == "LONG" {
		reverseConf, _ = s.techSellSignal()
	} else {
		reverseConf, _ = s.techBuySignal()
	}

	// Also check 1h mode: EXIT_MODE is a strong reversal signal.
	hMode := s.detectHourlyMode(p.side)
	if hMode == hourlyExitMode && reverseConf < s.cfg.ReversalConf {
		reverseConf = s.cfg.ReversalConf // 1h opposes → treat as reversal
	}

	if reverseConf >= s.cfg.ReversalConf {
		s.log.Info("AI: tech reversal → close "+p.side,
			zap.Float64("conf", reverseConf))
		s.closePos(ctx, p, pptr, "tech_reversal")
		s.lastCallBar = s.barCount - s.cfg.CallIntervalBars
	}
}
