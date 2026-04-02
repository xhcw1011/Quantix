package aistrat

import (
	"fmt"
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/indicator"
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

	// SL: tight, use range SL
	slDist := entryPrice * s.cfg.RangeSLPct
	if slDist <= 0 { return }

	// TP: min(1U equivalent, mainSL_distance * HedgeTPRatio)
	// 1U equivalent at this qty: 1.0 / qty = price distance for 1U profit
	oneUDist := 1.0 / qty
	// Distance from main entry to main SL
	mainSLDist := math.Abs(mainPos.entryPrice - mainPos.stopLoss)
	// TP capped by mainSL distance
	tpDist := mainSLDist * s.cfg.HedgeTPRatio
	if oneUDist < tpDist { tpDist = oneUDist }
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

func (s *AIStrategy) openRange(ctx *strategy.Context, side string, currentPrice, entryPrice, atr float64) {
	entryPrice = math.Round(entryPrice*100) / 100

	// Dynamic TP based on Bollinger Band width (recent volatility)
	// Use 60% of BB width as TP target, clamped between 0.6% and 1.5%
	tpPct := s.cfg.RangeTPPct
	closes := s.getCloses()
	if len(closes) >= 20 {
		bb := indicator.BollingerBands(closes, s.cfg.BBPeriod, s.cfg.BBStdDev)
		bbU, bbL := indicator.Last(bb.Upper), indicator.Last(bb.Lower)
		if bbU > bbL && currentPrice > 0 {
			bbWidthPct := (bbU - bbL) / currentPrice * 0.6 // 60% of BB width
			if bbWidthPct < s.cfg.BBWidthMin { bbWidthPct = s.cfg.BBWidthMin }
			if bbWidthPct > s.cfg.BBWidthMax { bbWidthPct = s.cfg.BBWidthMax }
			tpPct = bbWidthPct
		}
	}
	tpDist := entryPrice * tpPct
	slDist := entryPrice * s.cfg.RangeSLPct

	var stopLoss, takeProfit float64
	if side == "LONG" {
		takeProfit = entryPrice + tpDist
		stopLoss = entryPrice - slDist
	} else {
		takeProfit = entryPrice - tpDist
		stopLoss = entryPrice + slDist
	}
	if slDist <= 0 { return }

	equity := 100.0
	if pf := ctx.Portfolio; pf != nil {
		equity = pf.Equity(map[string]float64{s.cfg.Symbol: currentPrice})
	}
	risk := s.effectiveRisk(side)
	qty := math.Floor(equity*risk/slDist*1000) / 1000
	mtfScale := s.mtfLongScale; if side == "SHORT" { mtfScale = s.mtfShortScale }
	if mtfScale > 0 && mtfScale < 1.0 { qty = math.Floor(qty*mtfScale*1000) / 1000 }
	if qty <= 0 { return }

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

	s.log.Info("AI: OPEN RANGE",
		zap.String("side", side), zap.Float64("entry", entryPrice),
		zap.Float64("tp", takeProfit), zap.Float64("sl", stopLoss),
		zap.Float64("qty", qty))
	s.logEvent("open", side, "range", currentPrice, entryPrice, qty, 0, 0,
		fmt.Sprintf(`{"tp":%.2f,"sl":%.2f}`, takeProfit, stopLoss))
	s.syncToRedis(pos)
}

func (s *AIStrategy) openTrend(ctx *strategy.Context, side string, currentPrice, entryPrice, atr float64) {
	entryPrice = math.Round(entryPrice*100) / 100
	minDist := entryPrice * s.cfg.MinSLDistPct
	atrDist := atr * s.cfg.ATRK
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
	equity := 100.0
	if pf := ctx.Portfolio; pf != nil {
		equity = pf.Equity(map[string]float64{s.cfg.Symbol: currentPrice})
	}
	risk := s.effectiveRisk(side)
	qty := math.Floor(equity*risk/R*1000) / 1000
	mtfScale := s.mtfLongScale; if side == "SHORT" { mtfScale = s.mtfShortScale }
	if mtfScale > 0 && mtfScale < 1.0 { qty = math.Floor(qty*mtfScale*1000) / 1000 }
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
		// Get current price from latest primary bar
		bars := s.primaryBars()
		if len(bars) > 0 {
			lastPrice := bars[len(bars)-1].Close
			if side == "LONG" {
				req.Price = math.Round((lastPrice+0.01)*100) / 100 // sell slightly above
			} else {
				req.Price = math.Round((lastPrice-0.01)*100) / 100 // buy slightly below
			}
			req.Type = strategy.OrderLimit
		}
	}
	ctx.PlaceOrder(req)
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
