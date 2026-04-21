package aistrat

import (
	"fmt"
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
)

// ─── Open Position ───────────────────────────────────────────────────────────

// openHedgeScalp opens a counter-trend Range scalp to hedge a losing main position.
// TP is capped at min(1U-equiv, mainSL_distance * HedgeTPRatio).
// Qty is reduced to HedgeQtyRatio of the main position.
func (s *AIStrategy) openHedgeScalp(ctx *strategy.Context, side string, currentPrice, entryPrice, atr float64, mainPos *posState) {
	entryPrice = math.Round(entryPrice*100) / 100

	// Qty: fraction of main position
	qty := math.Floor(mainPos.initQty*s.cfg.HedgeQtyRatio*1000) / 1000
	if qty <= 0 { return }

	// SL: ATR-based, same as Range (1.5× TP distance, capped)
	slDist := atr * 1.5
	if maxSL := entryPrice * s.cfg.RangeSLPct; slDist > maxSL { slDist = maxSL }
	if slDist <= 0 { return }

	// TP: min(1U price distance, main SL DISTANCE * HedgeTPRatio)
	// oneUPriceDist = price movement needed to make $1 profit at this qty
	oneUPriceDist := 1.0 / qty
	// mainSLPriceDist = absolute price distance from main entry to main SL (NOT the SL price itself)
	mainSLPriceDist := math.Abs(mainPos.entryPrice - mainPos.stopLoss)
	// TP capped so hedge doesn't overshoot main position's SL zone
	tpDist := mainSLPriceDist * s.cfg.HedgeTPRatio
	if oneUPriceDist < tpDist { tpDist = oneUPriceDist }
	// Also cap by BB width for range
	tpDist = math.Max(tpDist, entryPrice*0.003) // minimum 0.3% to avoid dust

	var stopLoss, takeProfit float64
	if side == "LONG" {
		takeProfit = math.Round((entryPrice+tpDist)*100) / 100
		stopLoss = math.Round((entryPrice-slDist)*100) / 100
	} else {
		takeProfit = math.Round((entryPrice-tpDist)*100) / 100
		stopLoss = math.Round((entryPrice+slDist)*100) / 100
	}

	useLimit := math.Abs(entryPrice-currentPrice) > 0.01
	omsID := s.placeOrder(ctx, side, entryPrice, qty, useLimit)
	if omsID == "" { return }

	filledAt := time.Time{}
	if !useLimit { filledAt = time.Now() }
	pos := &posState{
		side: side, mode: modeRange, entryPrice: entryPrice,
		initQty: qty, remainQty: qty,
		R: slDist, stopLoss: stopLoss, takeProfit: takeProfit,
		trailing: stopLoss, peakPrice: entryPrice,
		filled: !useLimit, filledAt: filledAt, orderID: omsID, limitBar: s.barCount,
	}
	if side == "LONG" { s.longPos = pos } else { s.shortPos = pos }

	s.log.Info("SIG: OPEN HEDGE SCALP",
		zap.String("side", side), zap.Float64("entry", entryPrice),
		zap.Float64("tp", takeProfit), zap.Float64("sl", stopLoss),
		zap.Float64("qty", qty),
		zap.Float64("main_entry", mainPos.entryPrice),
		zap.Float64("main_sl", mainPos.stopLoss),
		zap.Float64("tp_dist", tpDist))
	s.logEvent("open", side, "hedge_scalp", currentPrice, entryPrice, qty, 0, 0,
		fmt.Sprintf(`{"tp":%.2f,"sl":%.2f,"main_entry":%.2f}`, takeProfit, stopLoss, mainPos.entryPrice))
	if pos.filled { s.syncToRedis(pos) }
}


// openGrid opens a range/grid position. TP at BB middle, SL at range boundary.
// No direction prediction — simply fades the extreme.
func (s *AIStrategy) openGrid(ctx *strategy.Context, side string, currentPrice, entryPrice, atr float64) {
	entryPrice = math.Round(entryPrice*100) / 100

	// Guard: BB levels must be available (set by reversionBuy/SellSignal)
	if s.lastBBLower <= 0 || s.lastBBUpper <= 0 || s.lastBBMiddle <= 0 {
		s.log.Warn("SIG: openGrid skipped — BB levels not available")
		return
	}

	// SL: outside the BB range boundary + buffer.
	// Grid trades rely on the range holding — SL at range edge, not ATR.
	buffer := atr * 0.5
	minSL := entryPrice * s.cfg.MinSLDistPct
	// TP = clamp(BB middle, minTP, maxTP) from entry.
	// maxTP: caps profit target when BB is wide (default $8).
	// minTP: floor profit target when BB is narrow — avoids $1-3 TPs that
	//   barely cover fees. Default = 60% of maxTP so the TP is always
	//   "meaningful" (around $5 for ETH at current price).
	maxTP := s.cfg.GridMaxTPDist
	if maxTP <= 0 { maxTP = 8.0 }
	minTP := s.cfg.GridMinTPDist
	if minTP <= 0 { minTP = maxTP * 0.6 }

	var stopLoss, takeProfit float64
	if side == "LONG" {
		sl := s.lastBBLower - buffer
		if entryPrice-sl < minSL { sl = entryPrice - minSL }
		stopLoss = math.Round(sl*100) / 100
		if s.lastBBMiddle > entryPrice {
			takeProfit = math.Round(s.lastBBMiddle*100) / 100
		} else {
			takeProfit = math.Round((entryPrice+entryPrice*s.cfg.GridTPPct)*100) / 100
		}
		// Cap TP distance
		if takeProfit-entryPrice > maxTP {
			takeProfit = math.Round((entryPrice+maxTP)*100) / 100
		}
		// Floor TP distance
		if takeProfit-entryPrice < minTP {
			takeProfit = math.Round((entryPrice+minTP)*100) / 100
		}
	} else {
		sl := s.lastBBUpper + buffer
		if sl-entryPrice < minSL { sl = entryPrice + minSL }
		stopLoss = math.Round(sl*100) / 100
		if s.lastBBMiddle > 0 && s.lastBBMiddle < entryPrice {
			takeProfit = math.Round(s.lastBBMiddle*100) / 100
		} else {
			takeProfit = math.Round((entryPrice-entryPrice*s.cfg.GridTPPct)*100) / 100
		}
		if entryPrice-takeProfit > maxTP {
			takeProfit = math.Round((entryPrice-maxTP)*100) / 100
		}
		// Floor TP distance
		if entryPrice-takeProfit < minTP {
			takeProfit = math.Round((entryPrice-minTP)*100) / 100
		}
	}

	R := math.Abs(entryPrice - stopLoss)
	if R <= 0 { return }

	// Position sizing: grid uses dedicated equity allocation
	equity := 100.0
	if pf := ctx.Portfolio; pf != nil {
		equity = pf.Equity(map[string]float64{s.cfg.Symbol: currentPrice})
	}
	gridEquity := equity * s.cfg.GridEquityPct
	riskAmount := gridEquity * s.cfg.GridRiskPerLayer
	qty := math.Floor(riskAmount/R*1000) / 1000

	// Safety cap: grid margin must not exceed grid equity allocation
	leverage := s.cfg.Leverage; if leverage <= 0 { leverage = 10 }
	maxQty := math.Floor(gridEquity*leverage/entryPrice*1000) / 1000
	if qty > maxQty { qty = maxQty }
	if qty <= 0 { return }

	useLimit := math.Abs(entryPrice-currentPrice) > 0.01
	omsID := s.placeOrder(ctx, side, entryPrice, qty, useLimit)
	if omsID == "" { return }

	filledAt := time.Time{}
	if !useLimit { filledAt = time.Now() }
	pos := &posState{
		side: side, mode: modeRange, entryPrice: entryPrice, entryATR: atr,
		initQty: qty, remainQty: qty,
		R: R, stopLoss: stopLoss, takeProfit: takeProfit,
		trailing: stopLoss, peakPrice: entryPrice,
		filled: !useLimit, filledAt: filledAt, orderID: omsID, limitBar: s.barCount,
	}
	if side == "LONG" { s.longPos = pos } else { s.shortPos = pos }

	s.log.Info("SIG: OPEN GRID",
		zap.String("side", side), zap.Float64("entry", entryPrice),
		zap.Float64("tp", takeProfit), zap.Float64("sl", stopLoss),
		zap.Float64("R", R), zap.Float64("qty", qty))
	s.logEvent("open", side, "grid", currentPrice, entryPrice, qty, 0, 0,
		fmt.Sprintf(`{"tp":%.2f,"sl":%.2f,"R":%.2f}`, takeProfit, stopLoss, R))
	// Only sync to Redis after fill — unfilled limit orders cause phantom detection.
	if pos.filled { s.syncToRedis(pos) }
}

func (s *AIStrategy) openTrend(ctx *strategy.Context, side string, currentPrice, entryPrice, atr, gptTP float64) {
	entryPrice = math.Round(entryPrice*100) / 100

	// SL based on swing structure (real support/resistance), not just ATR.
	// STRONG_TREND/EXPANSION: use swing low/high as SL (wider, survives pullbacks).
	// SLOW_TREND: use ATR-based SL (tighter).
	var stopLoss float64
	atrDist := atr * s.cfg.ATRK // default ATR-based SL
	minDist := entryPrice * s.cfg.MinSLDistPct
	if atrDist < minDist { atrDist = minDist }

	if s.lastRegime == RegimeStrongTrend || s.lastRegime == RegimeExpansion {
		// Swing-based SL: set SL below swing low (LONG) or above swing high (SHORT).
		// This places SL at real support/resistance, not arbitrary ATR distance.
		buffer := atr * 0.5 // small buffer below/above swing
		if side == "LONG" {
			swLow := s.findSwingLow(20)
			stopLoss = math.Round((swLow-buffer)*100) / 100
			if entryPrice-stopLoss < atr*s.cfg.SwingSLMinATR { stopLoss = entryPrice - atr*s.cfg.SwingSLMinATR }
			if entryPrice-stopLoss > atr*s.cfg.SwingSLMaxATR { stopLoss = entryPrice - atr*s.cfg.SwingSLMaxATR }
		} else {
			swHigh := s.findSwingHigh(20)
			stopLoss = math.Round((swHigh+buffer)*100) / 100
			if stopLoss-entryPrice < atr*s.cfg.SwingSLMinATR { stopLoss = entryPrice + atr*s.cfg.SwingSLMinATR }
			if stopLoss-entryPrice > atr*s.cfg.SwingSLMaxATR { stopLoss = entryPrice + atr*s.cfg.SwingSLMaxATR }
		}
	} else {
		// ATR-based SL for non-trending regimes
		if side == "LONG" {
			stopLoss = entryPrice - atrDist
		} else {
			stopLoss = entryPrice + atrDist
		}
	}
	stopLoss = math.Round(stopLoss*100) / 100
	if side == "LONG" && stopLoss >= entryPrice { return }
	if side == "SHORT" && stopLoss <= entryPrice { return }

	R := math.Abs(entryPrice - stopLoss)
	if R <= 0 { return }

	// MaxRPercent: skip trade if SL is too wide relative to price
	if s.cfg.MaxRPercent > 0 && R/entryPrice > s.cfg.MaxRPercent {
		s.log.Info("SIG: skip — R too wide", zap.Float64("R", R), zap.Float64("max", entryPrice*s.cfg.MaxRPercent))
		return
	}

	equity := 100.0
	if pf := ctx.Portfolio; pf != nil {
		equity = pf.Equity(map[string]float64{s.cfg.Symbol: currentPrice})
	}

	// R-based position sizing: trend uses dedicated equity allocation.
	trendEquity := equity * s.cfg.TrendEquityPct
	riskAmount := trendEquity * s.cfg.TrendRiskPerTrade
	qty := math.Floor(riskAmount/R*1000) / 1000

	// MTF + trend exhaustion scaling: reduce qty when headwind detected.
	// Floor at 30% to avoid positions too small to matter.
	mtfScale := s.mtfLongScale
	if side == "SHORT" { mtfScale = s.mtfShortScale }
	if mtfScale < 0.3 { mtfScale = 0.3 } // never below 30%
	if mtfScale > 0 && mtfScale < 1.0 {
		qty = math.Floor(qty*mtfScale*1000) / 1000
	}

	// Safety cap: margin must not exceed trend equity allocation
	leverage := s.cfg.Leverage; if leverage <= 0 { leverage = 10 }
	maxQty := math.Floor(trendEquity*leverage/entryPrice*1000) / 1000
	if qty > maxQty { qty = maxQty }
	if qty <= 0 { return }

	useLimit := math.Abs(entryPrice-currentPrice) > 0.01
	omsID := s.placeOrder(ctx, side, entryPrice, qty, useLimit)
	if omsID == "" { return }

	filledAt := time.Time{}
	if !useLimit { filledAt = time.Now() }
	pos := &posState{
		side: side, mode: modeTrend, entryPrice: entryPrice, entryATR: atr,
		gptTPPrice: gptTP,
		initQty: qty, remainQty: qty,
		R: R, stopLoss: stopLoss, trailing: stopLoss, peakPrice: entryPrice,
		filled: !useLimit, filledAt: filledAt, orderID: omsID, limitBar: s.barCount,
	}
	if side == "LONG" { s.longPos = pos } else { s.shortPos = pos }

	s.log.Info("SIG: OPEN TREND",
		zap.String("side", side), zap.Float64("entry", entryPrice),
		zap.Float64("sl", stopLoss), zap.Float64("R", R), zap.Float64("qty", qty))
	s.logEvent("open", side, "trend", currentPrice, entryPrice, qty, 0, 0,
		fmt.Sprintf(`{"sl":%.2f,"R":%.2f}`, stopLoss, R))
	if pos.filled { s.syncToRedis(pos) }
}

func (s *AIStrategy) placeOrder(ctx *strategy.Context, side string, price, qty float64, useLimit bool) string {
	psSide := strategy.PositionSideLong
	orderSide := strategy.SideBuy
	if side == "SHORT" {
		psSide = strategy.PositionSideShort
		orderSide = strategy.SideSell
	}
	req := strategy.OrderRequest{
		Symbol: s.cfg.Symbol, Side: orderSide, PositionSide: psSide, Qty: qty,
	}
	if useLimit {
		req.Type = strategy.OrderLimit
		req.Price = price
	}
	return ctx.PlaceOrder(req)
}

// placeCloseOrder places a close order. Returns true if order was submitted.
// Uses limit order (maker fee) unless useMarket is true.
func (s *AIStrategy) placeCloseOrder(ctx *strategy.Context, side string, qty float64, useMarket bool) bool {
	closeSide := strategy.SideSell
	psSide := strategy.PositionSideLong
	if side == "SHORT" {
		closeSide = strategy.SideBuy
		psSide = strategy.PositionSideShort
	}
	req := strategy.OrderRequest{
		Symbol: s.cfg.Symbol, Side: closeSide, PositionSide: psSide, Qty: qty,
	}
	if !useMarket {
		// Close with Maker limit: SELL slightly above market, BUY slightly below market
		bars := s.primaryBars()
		if len(bars) > 0 {
			lastPrice := bars[len(bars)-1].Close
			if side == "LONG" {
				req.Price = math.Round((lastPrice+0.5)*100) / 100 // close LONG = SELL above market
			} else {
				req.Price = math.Round((lastPrice-0.5)*100) / 100 // close SHORT = BUY below market
			}
			req.Type = strategy.OrderLimit
		}
	}
	if id := ctx.PlaceOrder(req); id == "" {
		s.log.Error("placeCloseOrder failed", zap.String("side", side),
			zap.Float64("qty", qty), zap.Bool("market", useMarket))
		return false
	}
	return true
}

// addGPTGrid adds the GPT-suggested support/resistance price as a grid order for future fill.
func (s *AIStrategy) addGPTGrid(pos *posState, side string, gptEntry float64) {
	gridQty := math.Floor(pos.initQty*s.cfg.GridQtyRatio*1000) / 1000
	if gridQty <= 0 { return }
	// Cap: total qty must not exceed 2x initial
	if pos.remainQty+gridQty > pos.initQty*2 { return }

	var gridTP float64
	if side == "LONG" {
		gridTP = math.Round((gptEntry+gptEntry*s.cfg.GridTPPct)*100) / 100
	} else {
		gridTP = math.Round((gptEntry-gptEntry*s.cfg.GridTPPct)*100) / 100
	}

	g := &gridOrder{
		entryPrice: gptEntry, qty: gridQty, tp: gridTP,
		filled: false, limitBar: s.barCount,
	}
	pos.gridOrders = append(pos.gridOrders, g)
	s.log.Info("SIG: GPT entry as grid add-on",
		zap.String("side", side), zap.Float64("gpt_entry", gptEntry),
		zap.Float64("grid_qty", gridQty), zap.Float64("grid_tp", gridTP))
}
