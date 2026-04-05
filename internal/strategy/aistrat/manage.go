package aistrat

import (
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// ─── Position Management (every bar) ─────────────────────────────────────────

func (s *AIStrategy) managePos(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	price := bar.Close
	iv := bar.Interval; if iv == "" { iv = s.cfg.PrimaryInterval }
	isPrimary := iv == s.cfg.PrimaryInterval
	if isPrimary { p.barsHeld++ }

	// Limit order pending (check on primary bars only)
	if !p.filled {
		if isPrimary && s.barCount-p.limitBar > s.cfg.LimitTimeoutBars {
			s.log.Warn("AI: limit timeout — cancelling", zap.String("side", p.side), zap.String("id", p.orderID))
			if p.orderID != "" { ctx.CancelOrder(p.orderID) }
			// Check if OnFill already marked this as filled while we were cancelling.
			// If filled, keep the position (cancel only affects unfilled remainder).
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
	if p.side == "LONG" && price > p.peakPrice { p.peakPrice = price }
	if p.side == "SHORT" && price < p.peakPrice { p.peakPrice = price }

	// ── Stop-loss (always check locally — Trend has no exchange SL) ──
	if (p.side == "LONG" && price <= p.stopLoss) || (p.side == "SHORT" && price >= p.stopLoss) {
		s.log.Warn("STOP-LOSS", zap.String("side", p.side), zap.Float64("price", price), zap.Float64("stop", p.stopLoss))
		s.closePos(ctx, p, pptr, "stop_loss")
		s.consecLoss++
		s.stopBar = s.barCount
		return
	}

	if p.barsHeld < s.cfg.MinHoldBars { return } // minimum hold

	s.manageTrend(ctx, bar, p, pptr)
}


func (s *AIStrategy) manageGrid(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	if s.cfg.GridMaxLayers <= 0 { return }
	price := bar.Close

	// 1. Check existing grid orders for TP or fill
	for i := len(p.gridOrders) - 1; i >= 0; i-- {
		g := p.gridOrders[i]

		// Pending grid order — check if filled (limit order)
		if !g.filled {
			// Check if price reached the grid entry
			if (p.side == "LONG" && price <= g.entryPrice) || (p.side == "SHORT" && price >= g.entryPrice) {
				g.filled = true
				g.filledAt = time.Now()
				s.log.Info("AI: grid order filled",
					zap.String("side", p.side), zap.Float64("entry", g.entryPrice),
					zap.Float64("qty", g.qty), zap.Int("layer", i+1))
			}
			continue
		}

		// Filled grid order — check TP
		gridProfit := false
		if p.side == "LONG" && price >= g.tp { gridProfit = true }
		if p.side == "SHORT" && price <= g.tp { gridProfit = true }

		if gridProfit {
			s.log.Info("AI: grid TP hit",
				zap.String("side", p.side), zap.Float64("entry", g.entryPrice),
				zap.Float64("tp", g.tp), zap.Float64("price", price),
				zap.Float64("qty", g.qty), zap.Int("layer", i+1))
			s.placeCloseOrder(ctx, p.side, g.qty, false)
			p.remainQty -= g.qty
			// Remove this grid order
			p.gridOrders = append(p.gridOrders[:i], p.gridOrders[i+1:]...)
		}
	}

	// 2. Open new grid order if price moved far enough from last level
	if len(p.gridOrders) >= s.cfg.GridMaxLayers { return }
	if !p.filled { return } // base must be filled first

	// Grids only apply to Range regime (disabled in Trend-only mode)
	if s.cfg.ForceTrend { return }

	// Determine the reference price (last grid entry or base entry)
	refPrice := p.entryPrice
	if len(p.gridOrders) > 0 {
		last := p.gridOrders[len(p.gridOrders)-1]
		refPrice = last.entryPrice
	}

	spacing := p.entryPrice * s.cfg.GridSpacingPct
	shouldAdd := false
	var gridEntry, gridTP float64

	if p.side == "LONG" && price <= refPrice-spacing {
		gridEntry = math.Round(price*100) / 100
		gridTP = math.Round((gridEntry+gridEntry*s.cfg.GridTPPct)*100) / 100
		shouldAdd = true
	}
	if p.side == "SHORT" && price >= refPrice+spacing {
		gridEntry = math.Round(price*100) / 100
		gridTP = math.Round((gridEntry-gridEntry*s.cfg.GridTPPct)*100) / 100
		shouldAdd = true
	}

	if !shouldAdd { return }

	// Check total position won't exceed safe limits
	gridQty := math.Floor(p.initQty*s.cfg.GridQtyRatio*1000) / 1000
	if gridQty <= 0 { return }
	// Cap: total position (base + grids) must not exceed 2x initial qty
	totalQty := p.remainQty + gridQty
	if totalQty > p.initQty*2 { return }

	// Place grid order as market (price already at the level)
	omsID := s.placeOrder(ctx, p.side, gridEntry, gridQty, false)
	if omsID == "" { return }

	g := &gridOrder{
		entryPrice: gridEntry, qty: gridQty, tp: gridTP,
		filled: true, filledAt: time.Now(), orderID: omsID,
	}
	p.gridOrders = append(p.gridOrders, g)
	p.remainQty += gridQty

	s.log.Info("AI: grid order opened",
		zap.String("side", p.side), zap.Float64("entry", gridEntry),
		zap.Float64("tp", gridTP), zap.Float64("qty", gridQty),
		zap.Int("layer", len(p.gridOrders)))
}

func (s *AIStrategy) manageTrend(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	if p.barsHeld < s.cfg.MinTrendBars { return }

	price := bar.Close
	atr := s.calcATR()

	// R-based profit measurement
	pnlR := 0.0
	if p.R > 0 {
		if p.side == "LONG" { pnlR = (price - p.entryPrice) / p.R }
		if p.side == "SHORT" { pnlR = (p.entryPrice - price) / p.R }
	}

	// ── Trailing stop (R-based progressive tightening) ──
	// Phase 1: pnlR >= 1.5 → move SL to breakeven (entry price)
	// Phase 2: pnlR >= 2.0 → start ATR trailing (let trend run)
	var newTrail float64
	moved := false

	if pnlR >= 2.0 {
		// ATR trailing: follow peak with ATR×TrailATRK distance
		trailDist := atr * s.cfg.TrailingATRK
		if p.side == "LONG" {
			newTrail = p.peakPrice - trailDist
		} else {
			newTrail = p.peakPrice + trailDist
		}
		// Floor: never trail below breakeven once we're at 2R+
		if p.side == "LONG" && newTrail < p.entryPrice { newTrail = p.entryPrice }
		if p.side == "SHORT" && newTrail > p.entryPrice { newTrail = p.entryPrice }
		moved = true
	} else if pnlR >= 1.5 && !p.tp1RHit {
		// Move to breakeven
		newTrail = p.entryPrice
		p.tp1RHit = true
		moved = true
		s.log.Info("AI: trailing → breakeven", zap.String("side", p.side), zap.Float64("pnlR", pnlR))
	}

	if moved {
		newTrail = math.Round(newTrail*100) / 100
		// Only tighten, never widen
		if p.side == "LONG" && newTrail > p.trailing {
			p.trailing = newTrail
			if s.stagedEP != nil {
				s.stagedEP.ReplaceSLOrder(s.cfg.Symbol, "LONG", "SELL", p.remainQty, p.trailing)
			}
		}
		if p.side == "SHORT" && (p.trailing == 0 || newTrail < p.trailing) {
			p.trailing = newTrail
			if s.stagedEP != nil {
				s.stagedEP.ReplaceSLOrder(s.cfg.Symbol, "SHORT", "BUY", p.remainQty, p.trailing)
			}
		}
		s.syncToRedis(p)
	}

	// ── Bounce TP: if price bounced 0.5R from extreme, close remaining ──
	// Detects trend exhaustion — price made a new extreme then reversed.
	if p.remainQty < p.initQty && p.remainQty > 0 { // only after some TPs filled
		bounceThreshold := 0.5 * p.R
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
		if pnlR > 0 { s.consecLoss = 0 }
		return
	}
	if p.side == "SHORT" && p.trailing > 0 && p.trailing < p.stopLoss && price >= p.trailing {
		s.closePos(ctx, p, pptr, "trailing")
		if pnlR > 0 { s.consecLoss = 0 }
		return
	}

	// ── GPT reversal check — only when losing (pnlR < 1.0) ──
	// When profitable, let trailing/bounce handle exit. Don't cut winners.
	if pnlR < 1.0 && s.barCount-s.lastCallBar >= s.cfg.CallIntervalBars && p.barsHeld >= s.cfg.MinTrendBars {
		s.checkReversal(ctx, bar, p, pptr)
	}
}

func (s *AIStrategy) checkReversal(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	var signal gptSignal
	var err error
	if len(s.replaySignals) > 0 {
		signal, err = s.nextReplaySignal()
	} else {
		mktCtx := s.buildContext(ctx, bar)
		signal, err = s.callGPT(mktCtx)
		if err == nil { s.cacheSignal(bar, signal) }
	}
	if err != nil { s.lastCallBar = s.barCount; return }
	s.lastCallBar = s.barCount
	s.totalCall++

	// Update signal accumulation with this GPT call's results.
	// Otherwise reversal-check signals are wasted and don't contribute to future entry decisions.
	if signal.Long != nil && signal.Long.Confidence > 0.3 {
		s.accumLong += signal.Long.Confidence - 0.3
	}
	if signal.Short != nil && signal.Short.Confidence > 0.3 {
		s.accumShort += signal.Short.Confidence - 0.3
	}
	if s.accumLong > s.cfg.SignalAccumMax { s.accumLong = s.cfg.SignalAccumMax }
	if s.accumShort > s.cfg.SignalAccumMax { s.accumShort = s.cfg.SignalAccumMax }
	if s.accumLong > 0 && s.accumShort > 0 {
		diff := s.accumLong - s.accumShort
		if diff > 0 { s.accumLong = diff; s.accumShort = 0 } else { s.accumShort = -diff; s.accumLong = 0 }
	}

	// Extract reversal signal from new dual format
	reverseConf := 0.0
	reverseReason := ""
	if p.side == "LONG" && signal.Short != nil {
		reverseConf = signal.Short.Confidence
		reverseReason = signal.Short.Reasoning
	}
	if p.side == "SHORT" && signal.Long != nil {
		reverseConf = signal.Long.Confidence
		reverseReason = signal.Long.Reasoning
	}
	// Backward compat: old single-signal format
	if signal.Action != "" {
		if p.side == "LONG" && signal.Action == "SELL" { reverseConf = signal.Confidence; reverseReason = signal.Reasoning }
		if p.side == "SHORT" && signal.Action == "BUY" { reverseConf = signal.Confidence; reverseReason = signal.Reasoning }
	}

	s.log.Info("AI reversal check",
		zap.String("holding", p.side),
		zap.Float64("reverse_conf", reverseConf),
		zap.String("reasoning", reverseReason))

	// Reversal threshold lower than entry — exit faster when direction changes
	if reverseConf >= s.cfg.ReversalConf {
		closedSide := p.side
		s.log.Info("AI: reversal → close "+closedSide, zap.Float64("conf", reverseConf))
		s.closePos(ctx, p, pptr, "gpt_reversal")

		// Verify position was actually closed before attempting flip.
		if *pptr != nil {
			s.log.Warn("AI: flip aborted — close order may have failed")
			return
		}

		// Flip: immediately open the opposite direction using the GPT signal we already have.
		// Flip threshold: halfway between ReversalConf and ConfidenceThreshold.
		// Higher than reversal (not every close deserves a flip) but lower than normal entry
		// (the reversal itself provides directional confirmation).
		flipThreshold := (s.cfg.ReversalConf + s.cfg.ConfidenceThreshold) / 2 // e.g. (0.72+0.82)/2 = 0.77
		atr := s.calcATR()
		price := bar.Close
		// Flip uses MARKET price — the whole point is immediate direction change.
		// Don't wait for a limit fill that may never come.
		if closedSide == "LONG" && signal.Short != nil && signal.Short.Confidence >= flipThreshold && s.shortPos == nil {
			entry := math.Round(price*100) / 100 // market price
			s.lastConf = signal.Short.Confidence
			s.log.Info("AI: flip → open SHORT (market)",
				zap.Float64("conf", signal.Short.Confidence),
				zap.Float64("flip_threshold", flipThreshold),
				zap.Float64("price", price))
			s.openTrend(ctx, "SHORT", price, entry, atr)
			if s.shortPos != nil { s.shortPos.entryRegime = s.lastRegime }
		}
		if closedSide == "SHORT" && signal.Long != nil && signal.Long.Confidence >= flipThreshold && s.longPos == nil {
			entry := math.Round(price*100) / 100 // market price
			s.lastConf = signal.Long.Confidence
			s.log.Info("AI: flip → open LONG (market)",
				zap.Float64("conf", signal.Long.Confidence),
				zap.Float64("flip_threshold", flipThreshold),
				zap.Float64("price", price))
			s.openTrend(ctx, "LONG", price, entry, atr)
			if s.longPos != nil { s.longPos.entryRegime = s.lastRegime }
		}
	}
}
