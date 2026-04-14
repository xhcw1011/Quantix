package aistrat

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
	"github.com/Quantix/quantix/internal/position"
)

// ─── Technical Helpers ───────────────────────────────────────────────────────

func (s *AIStrategy) primaryBars() []exchange.Kline {
	return s.barsByInterval[s.cfg.PrimaryInterval]
}

func (s *AIStrategy) barsForInterval(iv string) []exchange.Kline {
	return s.barsByInterval[iv]
}

func (s *AIStrategy) getCloses() []float64 {
	bars := s.primaryBars()
	c := make([]float64, len(bars))
	for i, b := range bars { c[i] = b.Close }
	return c
}

func (s *AIStrategy) calcATR() float64 {
	n := s.cfg.ATRPeriod
	if len(s.primaryBars()) < n+1 { n = len(s.primaryBars()) - 1; if n < 5 { return 0 } }
	recent := s.primaryBars()[len(s.primaryBars())-n-1:]
	var sum float64
	for i := 1; i < len(recent); i++ {
		sum += math.Max(recent[i].High-recent[i].Low,
			math.Max(math.Abs(recent[i].High-recent[i-1].Close), math.Abs(recent[i].Low-recent[i-1].Close)))
	}
	return sum / float64(n)
}

func (s *AIStrategy) findSwingLow(n int) float64 {
	if len(s.primaryBars()) < n { n = len(s.primaryBars()) }
	low := math.MaxFloat64
	for i := len(s.primaryBars()) - n; i < len(s.primaryBars()); i++ { if s.primaryBars()[i].Low < low { low = s.primaryBars()[i].Low } }
	return low
}
func (s *AIStrategy) findSwingHigh(n int) float64 {
	high := 0.0; start := len(s.primaryBars()) - n; if start < 0 { start = 0 }
	for i := start; i < len(s.primaryBars()); i++ { if s.primaryBars()[i].High > high { high = s.primaryBars()[i].High } }
	return high
}

// ─── Regime Detection ────────────────────────────────────────────────────────

// detectRegime identifies the current market structure and sets s.lastTrendDir.
// lastTrendDir: +1 = bullish (price rising), -1 = bearish (price falling), 0 = neutral.
// Only affects new entries — existing positions are managed by their entryRegime.
func (s *AIStrategy) detectRegime() Regime {
	bars := s.primaryBars()
	atr := s.calcATR()
	if atr <= 0 || len(bars) < s.cfg.RegimeN+1 {
		s.lastTrendDir = 0
		return RegimeRange // not enough data, don't trade
	}

	lastBar := bars[len(bars)-1]
	prevBar := bars[len(bars)-2]
	price := lastBar.Close

	// ── Compute overall trend direction from N-bar window ──
	recentBars := bars[len(bars)-s.cfg.RegimeN:]
	priceChange := price - recentBars[0].Close
	trendDir := 0
	if priceChange > atr*0.5 { trendDir = 1 }   // bullish
	if priceChange < -atr*0.5 { trendDir = -1 }  // bearish
	s.lastTrendDir = trendDir

	// ── 1. Expansion check (breakout bar + confirmation + trend alignment) ──
	barRange := lastBar.High - lastBar.Low
	body := math.Abs(lastBar.Close - lastBar.Open)
	dirOK := (lastBar.Close > prevBar.Close && lastBar.Close > lastBar.Open) ||
		(lastBar.Close < prevBar.Close && lastBar.Close < lastBar.Open)
	prevBody := math.Abs(prevBar.Close - prevBar.Open)
	prevSameDir := (lastBar.Close-prevBar.Close)*(prevBar.Close-prevBar.Open) > 0
	confirmOK := prevBody > atr*0.5 || prevSameDir
	// Expansion bar must align with overall trend direction.
	// A big bullish bar in a bearish trend = bounce, not breakout.
	barDir := 0
	if lastBar.Close > lastBar.Open { barDir = 1 } else { barDir = -1 }
	trendAligned := trendDir == 0 || barDir == trendDir
	if barRange > atr*s.cfg.ExpansionATRK && body > atr*s.cfg.ExpansionBodyK && dirOK && confirmOK && trendAligned {
		return RegimeExpansion
	}

	// ── 2. Efficiency ratio = |net move| / sum(|bar moves|) ──
	// Low efficiency = choppy market (big moves cancel out). Force RANGE.
	totalMoves := 0.0
	for i := 1; i < len(recentBars); i++ {
		totalMoves += math.Abs(recentBars[i].Close - recentBars[i-1].Close)
	}
	efficiency := 0.0
	if totalMoves > 0 {
		efficiency = math.Abs(priceChange) / totalMoves
	}
	if efficiency < s.cfg.TrendEfficiencyMin {
		return RegimeRange
	}

	// ── 3. Trend strength = |close_now - close_N| / ATR ──
	trendStrength := math.Abs(priceChange) / atr

	// ── 4. Direction score ──
	dirScore := calcDirectionScore(recentBars)

	// ── 5. Classify ──
	if trendStrength > s.cfg.StrongTrendThreshold && atr/price > s.cfg.StrongTrendMinVol {
		return RegimeStrongTrend
	}
	if trendStrength > s.cfg.SlowTrendThreshold && dirScore > s.cfg.SlowTrendDirScore {
		return RegimeSlowTrend
	}

	return RegimeRange
}

// calcDirectionScore measures how consistently bars move in the overall direction.
// Returns 0.0-1.0; higher = more consistent trend.
func calcDirectionScore(bars []exchange.Kline) float64 {
	if len(bars) < 2 {
		return 0
	}
	overallDir := bars[len(bars)-1].Close - bars[0].Close
	if overallDir == 0 {
		return 0
	}
	sameDir := 0
	for i := 1; i < len(bars); i++ {
		delta := bars[i].Close - bars[i-1].Close
		if delta*overallDir > 0 {
			sameDir++
		}
	}
	return float64(sameDir) / float64(len(bars)-1)
}

// recoverFromSyncer loads positions from PositionSyncer (Redis/exchange).
func (s *AIStrategy) recoverFromSyncer(currentPrice float64) {
	if s.syncer == nil {
		return
	}

	atr := s.calcATR()

	// Recover LONG
	if lp := s.syncer.GetLong(); lp != nil && lp.Qty > 0 {
		entry := lp.EntryPrice
		if entry == 0 { entry = currentPrice }

		// Restore strategy-specific fields from syncer if available, else compute defaults
		sl := lp.StopLoss
		if sl == 0 {
			slDist := atr * s.cfg.ATRK
			minDist := entry * s.cfg.MinSLDistPct
			if slDist < minDist { slDist = minDist }
			sl = entry - slDist
		}
		tp := lp.TakeProfit
		if tp == 0 { tp = entry + entry*s.cfg.RangeTPPct }

		s.longPos = &posState{
			side: "LONG", mode: posMode(0), entryPrice: entry, entryATR: lp.EntryATR,
			gptTPPrice: lp.GptTPPrice,
			initQty: lp.InitQty, remainQty: lp.Qty,
			R: lp.R, stopLoss: sl, takeProfit: tp,
			trailing: lp.Trailing, peakPrice: lp.PeakPrice,
			tp1RHit: lp.TP1Hit, barsHeld: max(lp.BarsHeld, 10), filled: true, filledAt: time.Now(),
		}
		if s.longPos.initQty == 0 { s.longPos.initQty = lp.Qty }
		if s.longPos.R == 0 { s.longPos.R = math.Abs(entry - sl) }
		if s.longPos.entryATR == 0 { s.longPos.entryATR = atr } // fallback to current ATR
		// R = |entry - SL|, no cap. Consistent with "only ATR determines risk, never limits profit".
		if s.longPos.peakPrice == 0 { s.longPos.peakPrice = currentPrice }
		if s.longPos.trailing == 0 { s.longPos.trailing = sl }
		if lp.Mode == "trend" { s.longPos.mode = modeTrend }
		if lp.Mode == "range" { s.longPos.mode = modeRange }
		if lp.EntryRegime != "" {
			s.longPos.entryRegime = Regime(lp.EntryRegime)
		} else {
			s.longPos.entryRegime = s.detectRegime()
		}

		s.loadStagedTPsFromRedis(s.longPos)
		s.log.Info("AI: recovered LONG from syncer",
			zap.Float64("entry", entry), zap.Float64("qty", lp.Qty),
			zap.Float64("stop", sl), zap.Float64("R", s.longPos.R),
			zap.String("regime", string(s.longPos.entryRegime)),
			zap.Int("staged_tps", len(s.longPos.stagedTPs)))
	}

	// Recover SHORT
	if sp := s.syncer.GetShort(); sp != nil && sp.Qty > 0 {
		entry := sp.EntryPrice
		if entry == 0 { entry = currentPrice }

		sl := sp.StopLoss
		if sl == 0 {
			slDist := atr * s.cfg.ATRK
			minDist := entry * s.cfg.MinSLDistPct
			if slDist < minDist { slDist = minDist }
			sl = entry + slDist
		}
		tp := sp.TakeProfit
		if tp == 0 { tp = entry - entry*s.cfg.RangeTPPct }

		s.shortPos = &posState{
			side: "SHORT", mode: posMode(0), entryPrice: entry, entryATR: sp.EntryATR,
			gptTPPrice: sp.GptTPPrice,
			initQty: sp.InitQty, remainQty: sp.Qty,
			R: sp.R, stopLoss: sl, takeProfit: tp,
			trailing: sp.Trailing, peakPrice: sp.PeakPrice,
			tp1RHit: sp.TP1Hit, barsHeld: max(sp.BarsHeld, 10), filled: true, filledAt: time.Now(),
		}
		if s.shortPos.initQty == 0 { s.shortPos.initQty = sp.Qty }
		if s.shortPos.R == 0 { s.shortPos.R = math.Abs(entry - sl) }
		if s.shortPos.entryATR == 0 { s.shortPos.entryATR = atr } // fallback to current ATR
		if s.shortPos.peakPrice == 0 { s.shortPos.peakPrice = currentPrice }
		if s.shortPos.trailing == 0 { s.shortPos.trailing = sl }
		if sp.Mode == "trend" { s.shortPos.mode = modeTrend }
		if sp.Mode == "range" { s.shortPos.mode = modeRange }
		if sp.EntryRegime != "" {
			s.shortPos.entryRegime = Regime(sp.EntryRegime)
		} else {
			s.shortPos.entryRegime = s.detectRegime()
		}

		s.loadStagedTPsFromRedis(s.shortPos)
		s.log.Info("AI: recovered SHORT from syncer",
			zap.Float64("entry", entry), zap.Float64("qty", sp.Qty),
			zap.Float64("stop", sl), zap.Float64("R", s.shortPos.R),
			zap.String("regime", string(s.shortPos.entryRegime)),
			zap.Int("staged_tps", len(s.shortPos.stagedTPs)))
	}
}

// syncToRedis writes the current posState to the Syncer for persistence.
func (s *AIStrategy) syncToRedis(pos *posState) {
	if s.syncer == nil || pos == nil {
		return
	}
	modeStr := "range"
	if pos.mode == modeTrend { modeStr = "trend" }

	sp := &position.StrategyPosition{
		ExchangePosition: position.ExchangePosition{
			Symbol: s.cfg.Symbol, Side: pos.side,
			Qty: pos.remainQty, EntryPrice: pos.entryPrice,
		},
		Mode: modeStr, StopLoss: pos.stopLoss, TakeProfit: pos.takeProfit,
		Trailing: pos.trailing, PeakPrice: pos.peakPrice,
		R: pos.R, EntryATR: pos.entryATR, GptTPPrice: pos.gptTPPrice, InitQty: pos.initQty,
		TP1Hit: pos.tp1RHit, BarsHeld: pos.barsHeld,
		OrderID: pos.orderID, Filled: pos.filled,
		EntryRegime: string(pos.entryRegime),
	}
	s.syncer.UpdatePosition(context.Background(), sp)
}

// syncRemove clears a position from Syncer.
func (s *AIStrategy) syncRemove(side string) {
	if s.syncer == nil { return }
	s.syncer.RemovePosition(context.Background(), side)
	s.deleteStagedTPsFromRedis(side)
}

// stagedTPRedisKey returns the Redis key for staged TP records.
func (s *AIStrategy) stagedTPRedisKey(side string) string {
	return fmt.Sprintf("quantix:staged_tp:%s:%s:%s", s.engineID, s.cfg.Symbol, side)
}

// saveStagedTPsToRedis persists TP records for tracking and restart recovery.
func (s *AIStrategy) saveStagedTPsToRedis(pos *posState) {
	if s.rdb == nil || pos == nil || len(pos.stagedTPs) == 0 { return }
	data, err := json.Marshal(pos.stagedTPs)
	if err != nil { return }
	s.rdb.Set(context.Background(), s.stagedTPRedisKey(pos.side), string(data), 0)
}

// loadStagedTPsFromRedis loads TP records on recovery.
// Records are loaded for reference, but stagedTPPlaced is left FALSE so that
// the next OnBar re-places all protective orders on the exchange.
// The previous session's exchange orders are cancelled during engine shutdown,
// so relying on them still being active would leave the position unprotected.
func (s *AIStrategy) loadStagedTPsFromRedis(pos *posState) {
	if s.rdb == nil || pos == nil { return }
	val, err := s.rdb.Get(context.Background(), s.stagedTPRedisKey(pos.side)).Result()
	if err != nil || val == "" { return }
	var records []stagedTPRecord
	if err := json.Unmarshal([]byte(val), &records); err != nil { return }
	pos.stagedTPs = records
	// stagedTPPlaced stays false — exchange orders were cancelled on shutdown.
	// OnBar will detect !stagedTPPlaced and re-place SL+TP on the exchange.
}

// deleteStagedTPsFromRedis removes TP records when position is closed.
func (s *AIStrategy) deleteStagedTPsFromRedis(side string) {
	if s.rdb == nil { return }
	s.rdb.Del(context.Background(), s.stagedTPRedisKey(side))
}

// ─── Technical BUY Signal (replaces GPT for LONG direction) ─────────────────
// techBuySignal generates a confidence score for LONG entries using pure technicals.
// Returns confidence (0-1) and suggested entry price.
// Criteria: EMA9 crosses above EMA21, RSI above 40 and below 70, MACD histogram positive.
// This replaces GPT's BUY direction which had only 12.4% accuracy.
func (s *AIStrategy) techBuySignal() (conf float64, entry float64) {
	closes := s.getCloses()
	if len(closes) < 30 { return 0, 0 }

	price := closes[len(closes)-1]

	// EMA20 slope guard: if overall trend is falling, don't buy
	// This prevents techBuySignal from bypassing the EMA slope filter in signal.go
	ema20 := indicator.EMA(closes, 20)
	if len(ema20) >= 2 {
		slope := ema20[len(ema20)-1] - ema20[len(ema20)-2]
		if slope < 0 { return 0, 0 } // trend is down, skip LONG
	}

	// EMA crossover: 9-period crosses above 21-period
	ema9 := indicator.EMA(closes, 9)
	ema21 := indicator.EMA(closes, 21)
	if len(ema9) < 2 || len(ema21) < 2 { return 0, 0 }

	ema9Now := ema9[len(ema9)-1]
	ema9Prev := ema9[len(ema9)-2]
	ema21Now := ema21[len(ema21)-1]
	ema21Prev := ema21[len(ema21)-2]

	// Fresh crossover: was below, now above (or very recently crossed)
	freshCross := ema9Prev <= ema21Prev && ema9Now > ema21Now
	aboveEMA := ema9Now > ema21Now // already above (continuation)

	if !freshCross && !aboveEMA { return 0, 0 }

	// RSI filter: not overbought, not oversold
	rsiVals := indicator.RSI(closes, s.cfg.RSIPeriod)
	rsi := indicator.Last(rsiVals)
	if rsi < 40 || rsi > 70 { return 0, 0 }

	// MACD histogram must be positive (momentum confirmation)
	macd := indicator.MACD(closes, s.cfg.MACDFast, s.cfg.MACDSlow, s.cfg.MACDSignal)
	macdHist := indicator.Last(macd.Histogram)
	if macdHist <= 0 { return 0, 0 }

	// Build confidence based on signal strength
	conf = 0.70 // base confidence for technical signal

	if freshCross {
		conf += 0.10 // fresh crossover is stronger
	}
	if rsi > 50 && rsi < 65 {
		conf += 0.05 // sweet spot RSI
	}
	if macdHist > 0.5 {
		conf += 0.05 // strong momentum
	}

	// 15m trend confirmation
	bars15 := s.barsForInterval("15m")
	if len(bars15) >= 10 {
		c15 := make([]float64, len(bars15))
		for i, b := range bars15 { c15[i] = b.Close }
		ema10_15 := indicator.Last(indicator.EMA(c15, 10))
		ema30_15 := 0.0
		if len(c15) >= 30 { ema30_15 = indicator.Last(indicator.EMA(c15, 30)) }
		if ema30_15 > 0 && ema10_15 > ema30_15 {
			conf += 0.05 // 15m structure bullish
		}
	}

	if conf > 0.95 { conf = 0.95 }

	// Entry: previous bar's close - $1 buffer (real support level)
	bars := s.primaryBars()
	if len(bars) >= 2 {
		prevClose := bars[len(bars)-2].Close
		entry = math.Round((prevClose - prevClose*s.cfg.LongEntryOffsetPct) * 100) / 100
	} else {
		entry = math.Round((price - price*s.cfg.LongEntryOffsetPct) * 100) / 100
	}

	return conf, entry
}

func r2(v float64) float64 { return math.Round(v*100) / 100 }
func r3(v float64) float64 { return math.Round(v*1000) / 1000 }

// logEvent writes a trade event to DB for persistent analysis.
func (s *AIStrategy) logEvent(eventType, side, reason string, price, entryPrice, qty, confidence, pnl float64, details string) {
	if s.store == nil { return }
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.store.InsertTradeEvent(ctx, data.TradeEvent{
			UserID: s.userID, EngineID: s.engineID, Symbol: s.cfg.Symbol,
			EventType: eventType, Side: side, Price: price, EntryPrice: entryPrice,
			Qty: qty, Confidence: confidence, MTFScore: s.lastMTFScore,
			PnL: pnl, Reason: reason, Details: details,
		})
	}()
}

