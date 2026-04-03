package aistrat

import (
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
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
			s.log.Warn("AI: limit timeout", zap.String("side", p.side), zap.String("id", p.orderID))
			if p.orderID != "" { ctx.CancelOrder(p.orderID) }
			*pptr = nil
			return
		}
		return
	}

	// Update peak
	if p.side == "LONG" && price > p.peakPrice { p.peakPrice = price }
	if p.side == "SHORT" && price < p.peakPrice { p.peakPrice = price }

	// ── Stop-loss (skip for trend with staged orders — exchange handles it) ──
	if !(p.mode == modeTrend && p.stagedTPPlaced) {
		if (p.side == "LONG" && price <= p.stopLoss) || (p.side == "SHORT" && price >= p.stopLoss) {
			s.log.Warn("STOP-LOSS", zap.String("side", p.side), zap.Float64("price", price), zap.Float64("stop", p.stopLoss))
			s.closePos(ctx, p, pptr, "stop_loss")
			s.consecLoss++
			s.stopBar = s.barCount
			s.log.Info("AI: stop-loss hit")
			return
		}
	}

	if p.barsHeld < s.cfg.MinHoldBars { return } // minimum hold

	// Dynamic mode switch: re-evaluate regime every bar.
	// Range → Trend: if ATR is now expanding and MTF strongly directional
	// Trend → Range: if ATR contracted and position is small profit/loss
	if p.mode == modeRange && (s.lastMTFScore >= 2 || s.lastMTFScore <= -2) {
		atrNow := s.calcATR()
		bars5m := s.primaryBars()
		if len(bars5m) >= 20 {
			var atrSum float64
			for i := len(bars5m) - 20; i < len(bars5m); i++ {
				atrSum += bars5m[i].High - bars5m[i].Low
			}
			if atrNow > atrSum/20*1.2 {
				p.mode = modeTrend
				s.log.Info("AI: mode upgrade Range → Trend (ATR expanding + strong MTF)",
					zap.String("side", p.side), zap.Int("mtf", s.lastMTFScore))
				s.syncToRedis(p)
			}
		}
	}

	if p.mode == modeRange {
		s.manageRange(ctx, bar, p, pptr)
	} else {
		s.manageTrend(ctx, bar, p, pptr)
	}
}

func (s *AIStrategy) manageRange(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	price := bar.Close

	// ── Take-profit hit: decide whether to close or upgrade to trend ──
	tpHit := (p.side == "LONG" && price >= p.takeProfit) ||
		(p.side == "SHORT" && price <= p.takeProfit)

	if tpHit && !p.tp1RHit {
		// Check momentum: is the move continuing?
		closes := s.getCloses()
		rsi := indicator.Last(indicator.RSI(closes, s.cfg.RSIPeriod))
		macd := indicator.MACD(closes, s.cfg.MACDFast, s.cfg.MACDSlow, s.cfg.MACDSignal)
		macdHist := indicator.Last(macd.Histogram)

		strongMomentum := false
		if p.side == "LONG" && rsi > 55 && macdHist > 0 {
			strongMomentum = true
		}
		if p.side == "SHORT" && rsi < 45 && macdHist < 0 {
			strongMomentum = true
		}

		if strongMomentum {
			// Momentum continuing → close 50%, upgrade rest to trend mode
			halfQty := math.Floor(p.initQty*0.50*1000) / 1000
			if halfQty > 0 {
				s.log.Info("RANGE TP → UPGRADE to trend (momentum strong)",
					zap.String("side", p.side), zap.Float64("price", price),
					zap.Float64("rsi", rsi), zap.Float64("macd_hist", macdHist))
				s.closePartial(ctx, p, pptr, halfQty, "range_tp_partial")
			}
			if *pptr == nil { return } // fully closed

			// Convert to trend mode
			p.mode = modeTrend
			p.tp1RHit = true // treat the range TP as the 1R event
			atr := s.calcATR()
			// Move stop to breakeven
			buf := p.entryPrice * s.cfg.BreakevenBuf
			if p.side == "LONG" {
				p.stopLoss = p.entryPrice - buf
				p.trailing = p.peakPrice - atr*s.cfg.TrailingATRK
			} else {
				p.stopLoss = p.entryPrice + buf
				p.trailing = p.peakPrice + atr*s.cfg.TrailingATRK
			}
			p.R = math.Abs(price - p.stopLoss)
			s.consecLoss = 0
			return
		}

		// Momentum fading → close all, take the scalp profit
		s.log.Info("RANGE TP → close all (momentum fading)",
			zap.String("side", p.side), zap.Float64("price", price))
		s.closePos(ctx, p, pptr, "range_tp")
		s.consecLoss = 0
		return
	}

	// ── Range trailing: protect profits ──
	rangePnlPct := 0.0
	if p.side == "LONG" { rangePnlPct = (price - p.entryPrice) / p.entryPrice }
	if p.side == "SHORT" { rangePnlPct = (p.entryPrice - price) / p.entryPrice }

	// +0.3%: move SL to breakeven
	if rangePnlPct >= s.cfg.RangeBEPct && p.side == "LONG" && p.stopLoss < p.entryPrice {
		p.stopLoss = p.entryPrice + p.entryPrice*s.cfg.BreakevenBuf
		s.log.Info("AI: Range +0.3% → SL to breakeven", zap.Float64("sl", p.stopLoss))
	}
	if rangePnlPct >= s.cfg.RangeBEPct && p.side == "SHORT" && p.stopLoss > p.entryPrice {
		p.stopLoss = p.entryPrice - p.entryPrice*s.cfg.BreakevenBuf
		s.log.Info("AI: Range +0.3% → SL to breakeven", zap.Float64("sl", p.stopLoss))
	}
	// +0.6%: lock in +0.3% profit
	if rangePnlPct >= s.cfg.RangeLockPct {
		lockPrice := 0.0
		if p.side == "LONG" {
			lockPrice = p.entryPrice + p.entryPrice*s.cfg.RangeLockOffset
			if p.stopLoss < lockPrice { p.stopLoss = lockPrice }
		} else {
			lockPrice = p.entryPrice - p.entryPrice*s.cfg.RangeLockOffset
			if p.stopLoss > lockPrice { p.stopLoss = lockPrice }
		}
	}
	// +0.8%: trailing 0.3% from peak
	if rangePnlPct >= s.cfg.RangeTrailPct {
		trailDist := p.peakPrice * s.cfg.RangeTrailDist
		if p.side == "LONG" {
			nt := p.peakPrice - trailDist
			if nt > p.stopLoss { p.stopLoss = nt }
			if price <= p.stopLoss {
				s.log.Info("RANGE TRAILING", zap.Float64("price", price), zap.Float64("sl", p.stopLoss))
				s.closePos(ctx, p, pptr, "range_trailing")
				s.consecLoss = 0
				return
			}
		} else {
			nt := p.peakPrice + trailDist
			if nt < p.stopLoss { p.stopLoss = nt }
			if price >= p.stopLoss {
				s.log.Info("RANGE TRAILING", zap.Float64("price", price), zap.Float64("sl", p.stopLoss))
				s.closePos(ctx, p, pptr, "range_trailing")
				s.consecLoss = 0
				return
			}
		}
	}

	// Safety timeout: 4 hours max hold for Range positions.
	// Prevents overnight holds (funding rate), stale capital lock.
	// This is a last resort — SL/trailing should exit first in normal conditions.
	held := time.Since(p.filledAt)
	if !p.filledAt.IsZero() && held >= 4*time.Hour {
		s.log.Info("AI: Range safety timeout (4h)",
			zap.String("side", p.side), zap.Duration("held", held))
		s.closePos(ctx, p, pptr, "timeout_safety")
		return
	}

	// ── Grid orders: add on dip, take profit on bounce ──
	s.manageGrid(ctx, bar, p, pptr)
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

	// Only add grids in Range regime
	if !s.isRangeRegime(price) { return }

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

	// When staged TP orders are on the exchange, the exchange handles all exits.
	// Only run GPT reversal check (to cancel all orders and reverse if market flips).
	if p.stagedTPPlaced {
		if s.barCount-s.lastCallBar >= s.cfg.CallIntervalBars {
			s.checkReversal(ctx, bar, p, pptr)
		}
		return
	}

	// Fallback: local trailing for paper/backtest mode (no staged orders).
	price := bar.Close
	atr := s.calcATR()

	pnlR := 0.0
	if p.R > 0 {
		if p.side == "LONG" { pnlR = (price - p.entryPrice) / p.R }
		if p.side == "SHORT" { pnlR = (p.entryPrice - price) / p.R }
	}

	// Adaptive trailing based on 15m ATR + profit level
	atr15 := 0.0
	bars15 := s.barsForInterval("15m")
	if len(bars15) >= 15 {
		recent15 := bars15[len(bars15)-15:]
		var sum15 float64
		for i := 1; i < len(recent15); i++ {
			sum15 += math.Max(recent15[i].High-recent15[i].Low,
				math.Max(math.Abs(recent15[i].High-recent15[i-1].Close), math.Abs(recent15[i].Low-recent15[i-1].Close)))
		}
		atr15 = sum15 / float64(len(recent15)-1)
	}
	baseTrailPct := s.cfg.TrailBasePct
	if atr15 > 0 && p.peakPrice > 0 {
		atr15Pct := atr15 / p.peakPrice
		if atr15Pct < 0.005 { baseTrailPct = s.cfg.TrailLowVolPct }
		if atr15Pct >= 0.005 && atr15Pct < 0.01 { baseTrailPct = s.cfg.TrailBasePct }
		if atr15Pct >= 0.01 { baseTrailPct = s.cfg.TrailHighVolPct }
	}

	var trailDist float64
	trailFloor := p.peakPrice * s.cfg.TrailFloorPct
	if pnlR >= 2.0 {
		trailDist = p.peakPrice * baseTrailPct * 0.40
		if trailDist < trailFloor { trailDist = trailFloor }
	} else if pnlR >= 1.5 {
		d := p.peakPrice * baseTrailPct * 0.65
		if d < p.peakPrice*0.006 { d = p.peakPrice * 0.006 }
		trailDist = d
	} else if pnlR >= 1.0 {
		trailDist = p.peakPrice * baseTrailPct
	} else {
		trailDist = atr * s.cfg.TrailingATRK
		minTrailDist := p.peakPrice * s.cfg.TrailBasePct
		if trailDist < minTrailDist { trailDist = minTrailDist }
	}

	if p.side == "LONG" {
		nt := p.peakPrice - trailDist
		if pnlR >= 0.5 && nt < p.entryPrice { nt = p.entryPrice }
		if nt > p.trailing { p.trailing = nt }
		if price <= p.trailing && p.trailing > p.stopLoss {
			s.closePos(ctx, p, pptr, "trailing")
			if pnlR > 0 { s.consecLoss = 0 }
			return
		}
	} else {
		nt := p.peakPrice + trailDist
		if pnlR >= 0.5 && nt > p.entryPrice { nt = p.entryPrice }
		if nt < p.trailing { p.trailing = nt }
		if price >= p.trailing && p.trailing > 0 && p.trailing < p.stopLoss {
			s.closePos(ctx, p, pptr, "trailing")
			if pnlR > 0 { s.consecLoss = 0 }
			return
		}
	}

	if s.barCount-s.lastCallBar >= s.cfg.CallIntervalBars && p.barsHeld >= s.cfg.MinTrendBars {
		s.checkReversal(ctx, bar, p, pptr)
	}
}

func (s *AIStrategy) checkReversal(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	mktCtx := s.buildContext(ctx, bar)
	signal, err := s.callGPT(mktCtx)
	if err != nil { s.lastCallBar = s.barCount; return }
	s.lastCallBar = s.barCount
	s.totalCall++
	s.cacheSignal(bar, signal)

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
			s.log.Info("AI: flip → open SHORT (market)",
				zap.Float64("conf", signal.Short.Confidence),
				zap.Float64("flip_threshold", flipThreshold),
				zap.Float64("price", price))
			s.openTrend(ctx, "SHORT", price, entry, atr)
		}
		if closedSide == "SHORT" && signal.Long != nil && signal.Long.Confidence >= flipThreshold && s.longPos == nil {
			entry := math.Round(price*100) / 100 // market price
			s.log.Info("AI: flip → open LONG (market)",
				zap.Float64("conf", signal.Long.Confidence),
				zap.Float64("flip_threshold", flipThreshold),
				zap.Float64("price", price))
			s.openTrend(ctx, "LONG", price, entry, atr)
		}
	}
}
