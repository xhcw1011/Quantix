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

	s.log.Info("AI: OPEN HEDGE SCALP",
		zap.String("side", side), zap.Float64("entry", entryPrice),
		zap.Float64("tp", takeProfit), zap.Float64("sl", stopLoss),
		zap.Float64("qty", qty),
		zap.Float64("main_entry", mainPos.entryPrice),
		zap.Float64("main_sl", mainPos.stopLoss),
		zap.Float64("tp_dist", tpDist))
	s.logEvent("open", side, "hedge_scalp", currentPrice, entryPrice, qty, 0, 0,
		fmt.Sprintf(`{"tp":%.2f,"sl":%.2f,"main_entry":%.2f}`, takeProfit, stopLoss, mainPos.entryPrice))
	s.syncToRedis(pos)
}


func (s *AIStrategy) openTrend(ctx *strategy.Context, side string, currentPrice, entryPrice, atr float64) {
	entryPrice = math.Round(entryPrice*100) / 100
	atrDist := atr * s.cfg.ATRK
	minDist := entryPrice * s.cfg.MinSLDistPct
	if atrDist < minDist { atrDist = minDist }

	var stopLoss float64
	if side == "LONG" {
		stopLoss = entryPrice - atrDist
		if stopLoss >= entryPrice { return }
	} else {
		stopLoss = entryPrice + atrDist
		if stopLoss <= entryPrice { return }
	}

	R := math.Abs(entryPrice - stopLoss)
	if R <= 0 { return }

	// MaxRPercent: skip trade if SL is too wide relative to price
	if s.cfg.MaxRPercent > 0 && R/entryPrice > s.cfg.MaxRPercent {
		s.log.Info("AI: skip — R too wide", zap.Float64("R", R), zap.Float64("max", entryPrice*s.cfg.MaxRPercent))
		return
	}

	equity := 100.0
	if pf := ctx.Portfolio; pf != nil {
		equity = pf.Equity(map[string]float64{s.cfg.Symbol: currentPrice})
	}
	risk := s.effectiveRisk(side)
	// Deduct round-trip fee drag from effective R so sizing accounts for costs.
	// Entry taker ~0.05%, exit maker ~0.02%, total ~0.07% per side = ~0.14% round trip.
	feeDrag := entryPrice * s.cfg.FeeDragPct
	effectiveR := R - feeDrag
	if effectiveR <= 0 { effectiveR = R * 0.5 } // floor: never let fees eliminate R entirely
	qty := math.Floor(equity*risk/effectiveR*1000) / 1000
	mtfScale := s.mtfLongScale; if side == "SHORT" { mtfScale = s.mtfShortScale }
	if mtfScale > 0 && mtfScale < 1.0 { qty = math.Floor(qty*mtfScale*1000) / 1000 }
	// Confidence-weighted sizing: lower conf → smaller position (min 50%)
	// Use regime-aware threshold: STRONG_TREND uses lower entryConf,
	// so conf 0.65 in STRONG_TREND should scale higher than in SLOW_TREND.
	if s.cfg.ConfQtyScale && s.lastConf > 0 {
		baseConf := s.cfg.ConfidenceThreshold
		if s.lastRegime == RegimeStrongTrend || s.lastRegime == RegimeExpansion {
			baseConf = s.cfg.RegimeEntryConf
		}
		confScale := (s.lastConf - baseConf) / (1.0 - baseConf)
		if confScale < 0.5 { confScale = 0.5 }
		if confScale > 1.0 { confScale = 1.0 }
		qty = math.Floor(qty*confScale*1000) / 1000
	}
	// Cap qty so margin needed doesn't exceed 60% of equity
	maxQty := math.Floor(equity*0.6*10/entryPrice*1000) / 1000
	if qty > maxQty { qty = maxQty }
	if qty <= 0 { return }

	useLimit := math.Abs(entryPrice-currentPrice) > 0.01
	omsID := s.placeOrder(ctx, side, entryPrice, qty, useLimit)
	if omsID == "" { return }

	filledAt := time.Time{}
	if !useLimit { filledAt = time.Now() }
	pos := &posState{
		side: side, mode: modeTrend, entryPrice: entryPrice,
		initQty: qty, remainQty: qty,
		R: R, stopLoss: stopLoss, trailing: stopLoss, peakPrice: entryPrice,
		filled: !useLimit, filledAt: filledAt, orderID: omsID, limitBar: s.barCount,
	}
	if side == "LONG" { s.longPos = pos } else { s.shortPos = pos }

	s.log.Info("AI: OPEN TREND",
		zap.String("side", side), zap.Float64("entry", entryPrice),
		zap.Float64("sl", stopLoss), zap.Float64("R", R), zap.Float64("qty", qty))
	s.logEvent("open", side, "trend", currentPrice, entryPrice, qty, 0, 0,
		fmt.Sprintf(`{"sl":%.2f,"R":%.2f}`, stopLoss, R))
	s.syncToRedis(pos)
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

// placeCloseOrder places a close order. Uses limit order (maker fee) unless useMarket is true.
// Limit close price: sell at current+$0.01 (LONG), buy at current-$0.01 (SHORT) to get maker fee.
func (s *AIStrategy) placeCloseOrder(ctx *strategy.Context, side string, qty float64, useMarket bool) {
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
	}
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
	s.log.Info("AI: GPT entry as grid add-on",
		zap.String("side", side), zap.Float64("gpt_entry", gptEntry),
		zap.Float64("grid_qty", gridQty), zap.Float64("grid_tp", gridTP))
}
