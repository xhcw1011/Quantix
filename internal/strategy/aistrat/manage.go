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
			s.log.Warn("AI: limit timeout — cancelling", zap.String("side", p.side), zap.String("id", p.orderID))
			if p.orderID != "" { ctx.CancelOrder(p.orderID) }
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

	if p.barsHeld < s.cfg.MinHoldBars { return } // minimum hold

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

	// ── Base position TP check ──
	if p.takeProfit > 0 {
		tpHit := false
		if p.side == "LONG" && price >= p.takeProfit { tpHit = true }
		if p.side == "SHORT" && price <= p.takeProfit { tpHit = true }
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
	if s.cfg.GridMaxLayers <= 0 { return }
	price := bar.Close

	// 1. Check pending grid orders for fill.
	// Filled layers stay in gridOrders until the base staged TP closes the whole position.
	// Per-layer market TP was removed: it raced with the base limit TP at the same price,
	// causing partial fills and 0.001 ETH residuals (~$2 tail).
	for _, g := range p.gridOrders {
		if g.filled { continue }
		if (p.side == "LONG" && price <= g.entryPrice) || (p.side == "SHORT" && price >= g.entryPrice) {
			g.filled = true
			g.filledAt = time.Now()
			p.remainQty += g.qty
			// Recalculate TP based on new weighted average entry
			totalQty := p.initQty
			weightedEntry := p.entryPrice * p.initQty
			for _, gg := range p.gridOrders {
				if gg.filled {
					totalQty += gg.qty
					weightedEntry += gg.entryPrice * gg.qty
				}
			}
			avgEntry := weightedEntry / totalQty
			maxTP := s.cfg.GridMaxTPDist
			if maxTP <= 0 { maxTP = 8.0 }
			if p.side == "LONG" {
				p.takeProfit = math.Round((avgEntry+maxTP)*100) / 100
			} else {
				p.takeProfit = math.Round((avgEntry-maxTP)*100) / 100
			}
			s.log.Info("AI: grid order filled — TP recalculated",
				zap.String("side", p.side), zap.Float64("entry", g.entryPrice),
				zap.Float64("avg_entry", math.Round(avgEntry*100)/100),
				zap.Float64("new_tp", p.takeProfit),
				zap.Float64("qty", g.qty))
			// Cancel old exchange TP and place new one at updated price + qty
			if p.stagedTPPlaced {
				if ep, ok := ctx.Extra["staged_exit"].(strategy.StagedExitPlacer); ok {
					posSide := "LONG"
					if p.side == "SHORT" { posSide = "SHORT" }
					ep.CancelAllProtective(s.cfg.Symbol, posSide)
					p.stagedTPPlaced = false
				}
			}
			s.placeGridTP(ctx, p)
			s.syncToRedis(p)
		}
	}

	// 2. Open new grid order if price moved far enough from last level
	if len(p.gridOrders) >= s.cfg.GridMaxLayers { return }
	if !p.filled { return }
	if s.cfg.ForceTrend { return }

	// Smart DCA: don't add layers if market conditions oppose the position.
	// 1. Regime not RANGE → trend confirmed, stop averaging into it
	// 2. Regime is RANGE but trend_dir opposes position → drift against us, pause
	if s.lastRegime != RegimeRange {
		return
	}
	if p.side == "LONG" && s.lastTrendDir < 0 {
		return // bearish drift, don't add long layers
	}
	if p.side == "SHORT" && s.lastTrendDir > 0 {
		return // bullish drift, don't add short layers
	}

	// Dynamic grid spacing with exponential increase per layer.
	// Layer 1: base_spacing × 1, Layer 2: base_spacing × 2, etc.
	// Wrong direction → layers get further apart → less capital committed.
	layerNum := len(p.gridOrders) + 1 // next layer number (1-based)
	spacing := p.entryPrice * s.cfg.GridSpacingPct
	closes := s.getCloses()
	if len(closes) >= 20 {
		bb := indicator.BollingerBands(closes, s.cfg.BBPeriod, s.cfg.BBStdDev)
		if len(bb.Upper) > 0 && len(bb.Lower) > 0 {
			bbWidth := bb.Upper[len(bb.Upper)-1] - bb.Lower[len(bb.Lower)-1]
			dynamicSpacing := bbWidth / float64(s.cfg.GridMaxLayers+1)
			if dynamicSpacing > 0 { spacing = dynamicSpacing }
		}
	}
	// Exponential spacing from BASE entry (not last layer).
	// layer1 = base - 1×spacing, layer2 = base - 3×spacing (1+2), layer3 = base - 6×spacing (1+2+3)
	// Cumulative: sum(1..N) × spacing from base entry.
	cumulativeMultiplier := float64(layerNum * (layerNum + 1) / 2)
	totalDist := spacing * cumulativeMultiplier

	refPrice := p.entryPrice // always measure from base entry

	shouldAdd := false
	var gridEntry, gridTP float64

	if p.side == "LONG" && price <= refPrice-totalDist {
		gridEntry = math.Round(price*100) / 100
		gridTP = p.takeProfit
		if gridTP <= gridEntry { gridTP = math.Round((gridEntry+spacing)*100) / 100 }
		shouldAdd = true
	}
	if p.side == "SHORT" && price >= refPrice+totalDist {
		gridEntry = math.Round(price*100) / 100
		gridTP = p.takeProfit
		if gridTP >= gridEntry { gridTP = math.Round((gridEntry-spacing)*100) / 100 }
		shouldAdd = true
	}

	if !shouldAdd { return }

	gridQty := math.Floor(p.initQty*s.cfg.GridQtyRatio*1000) / 1000
	if gridQty <= 0 { return }
	totalQty := p.remainQty + gridQty
	if totalQty > p.initQty*4 { return }

	// Use limit order for grid layers (maker fee, 60% cheaper than market)
	omsID := s.placeOrder(ctx, p.side, gridEntry, gridQty, true)
	if omsID == "" { return }

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
	if p.barsHeld < s.cfg.MinTrendBars { return }

	price := bar.Close
	liveATR := s.calcATR()

	// R-based profit measurement
	pnlR := 0.0
	if p.R > 0 {
		if p.side == "LONG" { pnlR = (price - p.entryPrice) / p.R }
		if p.side == "SHORT" { pnlR = (p.entryPrice - price) / p.R }
	}

	// ── 1h mode-based trailing (bar-level, mirrors tickManage) ──
	atr := math.Max(p.entryATR, liveATR)
	hMode := s.detectHourlyMode(p.side)

	// Time-based exit: close if held > 3h and NOT in strong trend.
	// Prevents slow grind losses in weak/range markets.
	if p.filled && hMode != hourlyTrendStrong && time.Since(p.filledAt) > 3*time.Hour {
		s.log.Warn("BAR: time exit — held >3h in weak/exit mode",
			zap.String("side", p.side), zap.Float64("pnlR", pnlR))
		s.closePos(ctx, p, pptr, "time_exit")
		if pnlR > 0 { s.consecLoss = 0 } else { s.consecLoss++ }
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
				if wideTrail < floor { wideTrail = floor }
			} else {
				wideTrail = p.peakPrice + trailDist
				if wideTrail > floor { wideTrail = floor }
			}
			p.trailing = math.Round(wideTrail*100) / 100
			s.syncToRedis(p)
		}
		p.trailTier = tier
	case hourlyExitMode:
		trailDist = atr * 1.0 // tighter than WEAK (1.5) but not so tight it jump-triggers
		if pnlR >= 0.3 {
			lockR := math.Max(pnlR*0.5, 0.02)
			if p.side == "LONG" { floor = p.entryPrice + lockR*p.R }
			if p.side == "SHORT" { floor = p.entryPrice - lockR*p.R }
		}
	default: // hourlyTrendWeak — profit < 1 ATR → skip trailing
		if pnlR > 0 && math.Abs(price-p.entryPrice) < atr {
			updateTrail = false
		}
		trailDist = atr * s.cfg.TrailingATRK * 0.5
		if s.cfg.BreakevenR > 0 && p.R > 0 {
			lockR := 0.0
			switch {
			case pnlR >= 0.8: lockR = 0.4
			case pnlR >= 0.5: lockR = 0.2
			case pnlR >= 0.3: lockR = 0.02
			}
			if lockR > 0 {
				if p.side == "LONG" { floor = p.entryPrice + lockR*p.R }
				if p.side == "SHORT" { floor = p.entryPrice - lockR*p.R }
			}
		}
	}

	if updateTrail {
		var newTrail float64
		if p.side == "LONG" {
			newTrail = p.peakPrice - trailDist
			if newTrail < floor { newTrail = floor }
		} else {
			newTrail = p.peakPrice + trailDist
			if newTrail > floor { newTrail = floor }
		}
		newTrail = math.Round(newTrail*100) / 100
		moved := false
		if p.side == "LONG" && newTrail > p.trailing { p.trailing = newTrail; moved = true }
		if p.side == "SHORT" && (p.trailing == 0 || newTrail < p.trailing) { p.trailing = newTrail; moved = true }
		if moved { s.syncToRedis(p) }
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
		if pnlR > 0 { s.consecLoss = 0 }
		return
	}
	if p.side == "SHORT" && p.trailing > 0 && p.trailing < p.stopLoss && price >= p.trailing {
		s.closePos(ctx, p, pptr, "trailing")
		if pnlR > 0 { s.consecLoss = 0 }
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

