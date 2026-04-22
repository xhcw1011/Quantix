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

// ─── 1h Trend Direction ─────────────────────────────────────────────────────

// hourlyTrendDir returns the 1h EMA trend direction: +1 bullish, -1 bearish, 0 neutral.
// Uses 15m bars with EMA(80) ≈ 1h EMA(20).
// Requires 2 consecutive 15m bars (30 min) of consistent direction to confirm a trend flip.
// Filters out 15-minute fake pullbacks without being too slow for real reversals.
func (s *AIStrategy) hourlyTrendDir() int {
	bars15 := s.barsForInterval("15m")
	if len(bars15) < 82 { return 0 }

	closes := make([]float64, len(bars15))
	for i, b := range bars15 { closes[i] = b.Close }

	ema80 := indicator.EMA(closes, 80)
	if len(ema80) < 2 { return 0 }

	// Check last 2 EMA values: both must agree on direction.
	n := len(ema80)
	allBull, allBear := true, true
	for i := 0; i < 2; i++ {
		idx := n - 1 - i
		priceAt := closes[len(closes)-1-i]
		emaAt := ema80[idx]
		slopeAt := ema80[idx] - ema80[idx-1]

		if !(priceAt > emaAt && slopeAt > 0) { allBull = false }
		if !(priceAt < emaAt && slopeAt < 0) { allBear = false }
	}

	if allBull { return 1 }
	if allBear { return -1 }
	return 0
}

// detectHourlyMode determines the 1h management mode for an open position.
// TREND_STRONG: 1h EMA aligned with position + slope confirms → let profits run.
// EXIT_MODE: price crossed 1h EMA against position → prepare to exit.
// TREND_WEAK: everything else → normal management.
// Results are cached and recomputed only when new 15m bars arrive.
func (s *AIStrategy) detectHourlyMode(side string) hourlyMode {
	bars15 := s.barsForInterval("15m")
	nBars := len(bars15)

	// Return cached value if 15m bars haven't changed
	if nBars == s.hourlyModeBars {
		if side == "LONG" { return s.cachedHourlyLong }
		return s.cachedHourlyShort
	}

	// Recompute
	if nBars < 82 {
		s.hourlyModeBars = nBars
		s.cachedHourlyLong = hourlyTrendWeak
		s.cachedHourlyShort = hourlyTrendWeak
		if side == "LONG" { return s.cachedHourlyLong }
		return s.cachedHourlyShort
	}

	closes := make([]float64, nBars)
	for i, b := range bars15 { closes[i] = b.Close }

	ema80 := indicator.EMA(closes, 80)
	if len(ema80) < 2 {
		s.hourlyModeBars = nBars
		s.cachedHourlyLong = hourlyTrendWeak
		s.cachedHourlyShort = hourlyTrendWeak
		if side == "LONG" { return s.cachedHourlyLong }
		return s.cachedHourlyShort
	}

	price := closes[len(closes)-1]
	emaNow := ema80[len(ema80)-1]
	slope := ema80[len(ema80)-1] - ema80[len(ema80)-2]

	// Compute for both sides at once
	s.cachedHourlyLong = hourlyTrendWeak
	if price > emaNow && slope > 0 { s.cachedHourlyLong = hourlyTrendStrong }
	if price < emaNow { s.cachedHourlyLong = hourlyExitMode }

	s.cachedHourlyShort = hourlyTrendWeak
	if price < emaNow && slope < 0 { s.cachedHourlyShort = hourlyTrendStrong }
	if price > emaNow { s.cachedHourlyShort = hourlyExitMode }

	s.hourlyModeBars = nBars
	// Also sync lastHourlyDir for consistency with signal.go filter
	s.lastHourlyDir = s.hourlyTrendDir()

	if side == "LONG" { return s.cachedHourlyLong }
	return s.cachedHourlyShort
}

// ─── Regime Detection ────────────────────────────────────────────────────────

// detectRegime returns the confirmed regime after hysteresis smoothing.
// Raw regime is computed fresh every bar; transitions require 2 consecutive
// bars of the same new regime before committing. Prevents single-bar flicker
// from thrashing downstream decisions (GRID regime-flip exit, breakout
// shortcut, etc.). Sets s.lastTrendDir as a side effect (not hystereized —
// trend_dir is computed from stable 2h data).
// First bar after init bootstraps immediately (no 2-bar wait).
func (s *AIStrategy) detectRegime() Regime {
	raw := s.computeRawRegime()

	// Bootstrap: first call commits immediately.
	if s.confirmedRegime == "" {
		s.confirmedRegime = raw
		s.pendingRegime = raw
		s.pendingCount = 0
		return raw
	}

	// Same as confirmed → no transition in progress.
	if raw == s.confirmedRegime {
		s.pendingRegime = raw
		s.pendingCount = 0
		return s.confirmedRegime
	}

	// Different from confirmed — track pending transition.
	if raw == s.pendingRegime {
		s.pendingCount++
	} else {
		s.pendingRegime = raw
		s.pendingCount = 1
	}

	const hysteresisN = 2 // consecutive bars needed to confirm transition
	if s.pendingCount >= hysteresisN {
		s.log.Info("SIG: regime transition confirmed",
			zap.String("from", string(s.confirmedRegime)),
			zap.String("to", string(raw)),
			zap.Int("confirmations", s.pendingCount))
		s.confirmedRegime = raw
		s.pendingCount = 0
		return raw
	}

	// Pending but not yet confirmed — downstream sees stable prior regime.
	return s.confirmedRegime
}

// computeRawRegime runs the raw classification logic without hysteresis.
// Sets s.lastTrendDir as a side effect.
func (s *AIStrategy) computeRawRegime() Regime {
	bars := s.primaryBars()
	atr := s.calcATR()
	if atr <= 0 || len(bars) < s.cfg.RegimeN+1 {
		s.lastTrendDir = 0
		return RegimeRange // not enough data, don't trade
	}

	lastBar := bars[len(bars)-1]
	prevBar := bars[len(bars)-2]
	price := lastBar.Close

	// ── Compute overall trend direction ──
	// Prefer 15m bars (8 bars = 2h) for stable regime detection.
	// Falls back to primary (5m) bars if 15m not available.
	var recentBars []exchange.Kline
	bars15 := s.barsForInterval("15m")
	regimeN := 8 // 8 × 15m = 2 hours
	if len(bars15) >= regimeN+1 {
		recentBars = bars15[len(bars15)-regimeN:]
	} else {
		recentBars = bars[len(bars)-s.cfg.RegimeN:]
	}
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

	// Recover LONG — only if bot opened it (has strategy state in Redis: R > 0 or Mode set).
	// Manual positions (opened via exchange UI) have no strategy state → skip them.
	if lp := s.syncer.GetLong(); lp != nil && lp.Qty > 0 {
		if lp.R == 0 && lp.Mode == "" {
			s.log.Info("SIG: skipping LONG recovery — manual position (no strategy state)",
				zap.Float64("entry", lp.EntryPrice), zap.Float64("qty", lp.Qty))
			goto recoverShort
		}
		entry := lp.EntryPrice
		if entry == 0 { entry = currentPrice }

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

		// Recover grid layers
		for _, gr := range lp.GridOrders {
			s.longPos.gridOrders = append(s.longPos.gridOrders, &gridOrder{
				entryPrice: gr.EntryPrice, qty: gr.Qty, tp: gr.TP,
				filled: gr.Filled, orderID: gr.OrderID,
			})
		}

		// Fix TP for range positions: use syncer's weighted entry + GridMaxTPDist.
		// Handles cases where grid layers were lost or TP was set relative to base entry.
		if s.longPos.mode == modeRange {
			maxTP := s.cfg.GridMaxTPDist; if maxTP <= 0 { maxTP = 8.0 }
			newTP := math.Round((entry+maxTP)*100) / 100
			s.longPos.takeProfit = newTP
		}

		s.loadStagedTPsFromRedis(s.longPos)
		s.log.Info("SIG: recovered LONG from syncer",
			zap.Float64("entry", entry), zap.Float64("qty", lp.Qty),
			zap.Float64("tp", s.longPos.takeProfit),
			zap.Float64("stop", sl), zap.Float64("R", s.longPos.R),
			zap.String("regime", string(s.longPos.entryRegime)),
			zap.Int("staged_tps", len(s.longPos.stagedTPs)),
			zap.Int("grid_layers", len(s.longPos.gridOrders)))
	}

recoverShort:
	// Recover SHORT — only if bot opened it.
	if sp := s.syncer.GetShort(); sp != nil && sp.Qty > 0 {
		if sp.R == 0 && sp.Mode == "" {
			s.log.Info("SIG: skipping SHORT recovery — manual position (no strategy state)",
				zap.Float64("entry", sp.EntryPrice), zap.Float64("qty", sp.Qty))
			return
		}
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

		for _, gr := range sp.GridOrders {
			s.shortPos.gridOrders = append(s.shortPos.gridOrders, &gridOrder{
				entryPrice: gr.EntryPrice, qty: gr.Qty, tp: gr.TP,
				filled: gr.Filled, orderID: gr.OrderID,
			})
		}

		if s.shortPos.mode == modeRange {
			maxTP := s.cfg.GridMaxTPDist; if maxTP <= 0 { maxTP = 8.0 }
			newTP := math.Round((entry-maxTP)*100) / 100
			s.shortPos.takeProfit = newTP
		}

		s.loadStagedTPsFromRedis(s.shortPos)
		s.log.Info("SIG: recovered SHORT from syncer",
			zap.Float64("entry", entry), zap.Float64("qty", sp.Qty),
			zap.Float64("tp", s.shortPos.takeProfit),
			zap.Float64("stop", sl), zap.Float64("R", s.shortPos.R),
			zap.String("regime", string(s.shortPos.entryRegime)),
			zap.Int("staged_tps", len(s.shortPos.stagedTPs)),
			zap.Int("grid_layers", len(s.shortPos.gridOrders)))
	}
}

// syncToRedis writes the current posState to the Syncer for persistence.
func (s *AIStrategy) syncToRedis(pos *posState) {
	if s.syncer == nil || pos == nil {
		return
	}
	modeStr := "range"
	if pos.mode == modeTrend { modeStr = "trend" }

	// Persist grid layers for recovery
	var gridRecords []position.GridOrderRecord
	for _, g := range pos.gridOrders {
		gridRecords = append(gridRecords, position.GridOrderRecord{
			EntryPrice: g.entryPrice, Qty: g.qty, TP: g.tp,
			Filled: g.filled, OrderID: g.orderID,
		})
	}

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
		GridOrders: gridRecords,
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
// the next OnBar re-verifies protective orders on the exchange via recovery.
// Exchange TP/SL orders are preserved across restarts (not cancelled on shutdown).
func (s *AIStrategy) loadStagedTPsFromRedis(pos *posState) {
	if s.rdb == nil || pos == nil { return }
	val, err := s.rdb.Get(context.Background(), s.stagedTPRedisKey(pos.side)).Result()
	if err != nil || val == "" { return }
	var records []stagedTPRecord
	if err := json.Unmarshal([]byte(val), &records); err != nil { return }
	pos.stagedTPs = records
	// stagedTPPlaced stays false — recovery flow will re-verify exchange orders.
}

// deleteStagedTPsFromRedis removes TP records when position is closed.
func (s *AIStrategy) deleteStagedTPsFromRedis(side string) {
	if s.rdb == nil { return }
	s.rdb.Del(context.Background(), s.stagedTPRedisKey(side))
}

// ─── Technical BUY Signal (replaces GPT for LONG direction) ─────────────────
// techBuySignal dispatches based on regime:
// STRONG_TREND/EXPANSION/SLOW_TREND → breakout (directional markets need momentum signals)
// RANGE → reversion (sideways markets need mean-reversion signals)
func (s *AIStrategy) techBuySignal() (conf float64, entry float64) {
	if s.lastRegime == RegimeRange {
		return s.reversionBuySignal()
	}
	return s.breakoutBuySignal()
}

func (s *AIStrategy) techSellSignal() (conf float64, entry float64) {
	if s.lastRegime == RegimeRange {
		return s.reversionSellSignal()
	}
	return s.breakoutSellSignal()
}

// ─── Trend Breakout Signals (STRONG_TREND / EXPANSION) ──────────────────────
// Low-lag: price breaks N-bar high/low + momentum confirmation.
// No EMA crossover, no 15m confirmation — react to structure, not averages.

func (s *AIStrategy) breakoutBuySignal() (conf float64, entry float64) {
	bars := s.primaryBars()
	if len(bars) < 20 {
		s.log.Info("sig_reject", zap.String("fn", "breakoutBuy"), zap.String("reason", "insufficient_bars"), zap.Int("bars", len(bars)))
		return 0, 0
	}

	price := bars[len(bars)-1].Close
	curBar := bars[len(bars)-1]
	lookback := 10

	// Trend regime shortcut (EXPANSION/STRONG_TREND/SLOW_TREND with matching dir):
	// detectRegime has already validated direction over 2h × 8 bars of 15m data.
	// The 10-bar break + blowoff + RSI filters below would contradict that:
	//  - blowoff rejects "barRange > 2×ATR" but trend acceleration IS a big bar;
	//  - 10-bar break requires price > prior high but in持续单向 trend price often
	//    drifts within the 10-bar range without explicit break.
	// Trust the regime signal. Keep RSI safety net to avoid extreme-overbought FOMO.
	if s.lastTrendDir == 1 && (s.lastRegime == RegimeExpansion ||
		s.lastRegime == RegimeStrongTrend || s.lastRegime == RegimeSlowTrend) {
		closes := s.getCloses()
		rsi := indicator.Last(indicator.RSI(closes, s.cfg.RSIPeriod))
		if rsi > 80 {
			s.log.Info("sig_reject", zap.String("fn", "breakoutBuy"), zap.String("reason", "rsi_overbought_in_trend"),
				zap.String("regime", string(s.lastRegime)), zap.Float64("rsi", rsi))
			return 0, 0
		}
		s.log.Info("sig_accept", zap.String("fn", "breakoutBuy"), zap.String("reason", "trend_regime_with_dir"),
			zap.String("regime", string(s.lastRegime)), zap.Float64("price", price), zap.Float64("rsi", rsi))
		return 0.85, math.Round(price*100) / 100
	}

	highestHigh := 0.0
	for i := len(bars) - lookback - 1; i < len(bars)-1; i++ {
		if i < 0 { continue }
		if bars[i].High > highestHigh { highestHigh = bars[i].High }
	}

	if price <= highestHigh {
		s.log.Info("sig_reject", zap.String("fn", "breakoutBuy"), zap.String("reason", "no_breakout_long"),
			zap.Float64("price", price), zap.Float64("hi10", highestHigh),
			zap.Float64("gap_pct", (price-highestHigh)/highestHigh*100))
		return 0, 0
	}

	// Bar range filter: skip blow-off bars (range > 2× ATR = overextended move)
	atr := s.calcATR()
	barRange := curBar.High - curBar.Low
	if atr > 0 && barRange > atr*2 {
		s.log.Info("sig_reject", zap.String("fn", "breakoutBuy"), zap.String("reason", "bar_range_blowoff"),
			zap.Float64("bar_range_atr", barRange/atr))
		return 0, 0
	}

	closes := s.getCloses()
	rsiVals := indicator.RSI(closes, s.cfg.RSIPeriod)
	rsi := indicator.Last(rsiVals)
	if rsi > 80 {
		s.log.Info("sig_reject", zap.String("fn", "breakoutBuy"), zap.String("reason", "rsi_overbought"), zap.Float64("rsi", rsi))
		return 0, 0
	}

	conf = 0.75
	breakoutPct := (price - highestHigh) / highestHigh
	if breakoutPct > 0.001 { conf += 0.05 }
	if breakoutPct > 0.003 { conf += 0.05 }
	if curBar.Close > curBar.Open { conf += 0.05 }

	if len(bars) > 20 {
		avgVol := 0.0
		for i := len(bars) - 21; i < len(bars)-1; i++ { avgVol += bars[i].Volume }
		avgVol /= 20
		if curBar.Volume > avgVol*1.2 { conf += 0.05 }
	}

	if conf > 0.95 { conf = 0.95 }

	// Retest entry: enter at the breakout level (previous high), not current price.
	// Wait for price to pull back to the breakout point (resistance → support).
	// If price doesn't pull back, the limit order won't fill — better than chasing.
	entry = math.Round(highestHigh*100) / 100

	return conf, entry
}

func (s *AIStrategy) breakoutSellSignal() (conf float64, entry float64) {
	bars := s.primaryBars()
	if len(bars) < 20 {
		s.log.Info("sig_reject", zap.String("fn", "breakoutSell"), zap.String("reason", "insufficient_bars"), zap.Int("bars", len(bars)))
		return 0, 0
	}

	price := bars[len(bars)-1].Close
	curBar := bars[len(bars)-1]
	lookback := 10

	// Trend regime shortcut: see breakoutBuySignal for rationale.
	if s.lastTrendDir == -1 && (s.lastRegime == RegimeExpansion ||
		s.lastRegime == RegimeStrongTrend || s.lastRegime == RegimeSlowTrend) {
		closes := s.getCloses()
		rsi := indicator.Last(indicator.RSI(closes, s.cfg.RSIPeriod))
		if rsi < 20 {
			s.log.Info("sig_reject", zap.String("fn", "breakoutSell"), zap.String("reason", "rsi_oversold_in_trend"),
				zap.String("regime", string(s.lastRegime)), zap.Float64("rsi", rsi))
			return 0, 0
		}
		s.log.Info("sig_accept", zap.String("fn", "breakoutSell"), zap.String("reason", "trend_regime_with_dir"),
			zap.String("regime", string(s.lastRegime)), zap.Float64("price", price), zap.Float64("rsi", rsi))
		return 0.85, math.Round(price*100) / 100
	}

	lowestLow := math.MaxFloat64
	for i := len(bars) - lookback - 1; i < len(bars)-1; i++ {
		if i < 0 { continue }
		if bars[i].Low < lowestLow { lowestLow = bars[i].Low }
	}

	if price >= lowestLow {
		s.log.Info("sig_reject", zap.String("fn", "breakoutSell"), zap.String("reason", "no_breakout_short"),
			zap.Float64("price", price), zap.Float64("lo10", lowestLow),
			zap.Float64("gap_pct", (lowestLow-price)/lowestLow*100))
		return 0, 0
	}

	// Bar range filter: skip blow-off bars
	atr := s.calcATR()
	barRange := curBar.High - curBar.Low
	if atr > 0 && barRange > atr*2 {
		s.log.Info("sig_reject", zap.String("fn", "breakoutSell"), zap.String("reason", "bar_range_blowoff"),
			zap.Float64("bar_range_atr", barRange/atr))
		return 0, 0
	}

	closes := s.getCloses()
	rsiVals := indicator.RSI(closes, s.cfg.RSIPeriod)
	rsi := indicator.Last(rsiVals)
	if rsi < 20 {
		s.log.Info("sig_reject", zap.String("fn", "breakoutSell"), zap.String("reason", "rsi_oversold"), zap.Float64("rsi", rsi))
		return 0, 0
	}

	conf = 0.75
	breakoutPct := (lowestLow - price) / lowestLow
	if breakoutPct > 0.001 { conf += 0.05 }
	if breakoutPct > 0.003 { conf += 0.05 }
	if curBar.Close < curBar.Open { conf += 0.05 }

	if len(bars) > 20 {
		avgVol := 0.0
		for i := len(bars) - 21; i < len(bars)-1; i++ { avgVol += bars[i].Volume }
		avgVol /= 20
		if curBar.Volume > avgVol*1.2 { conf += 0.05 }
	}

	if conf > 0.95 { conf = 0.95 }

	// Retest entry: enter at the breakout level (previous low), not current price.
	entry = math.Round(lowestLow*100) / 100

	return conf, entry
}

// ─── Mean Reversion Signals (RANGE / SLOW_TREND) ────────────────────────────
// Fade extremes: RSI oversold/overbought + Bollinger Band touch.
// Counter-trend by design — profit from range oscillation.

func (s *AIStrategy) reversionBuySignal() (conf float64, entry float64) {
	closes := s.getCloses()
	if len(closes) < 30 {
		s.log.Info("sig_reject", zap.String("fn", "reversionBuy"), zap.String("reason", "insufficient_bars"), zap.Int("closes", len(closes)))
		return 0, 0
	}

	price := closes[len(closes)-1]

	bb := indicator.BollingerBands(closes, s.cfg.BBPeriod, s.cfg.BBStdDev)
	if len(bb.Lower) == 0 {
		s.log.Info("sig_reject", zap.String("fn", "reversionBuy"), zap.String("reason", "bb_unavailable"))
		return 0, 0
	}
	bbLower := bb.Lower[len(bb.Lower)-1]
	bbUpper := bb.Upper[len(bb.Upper)-1]
	bbMiddle := bb.Middle[len(bb.Middle)-1]

	// BB too narrow = no meaningful range to trade. Skip.
	bbWidth := bbUpper - bbLower
	if bbWidth < price*s.cfg.BBWidthMin {
		s.log.Info("sig_reject", zap.String("fn", "reversionBuy"), zap.String("reason", "bb_narrow"),
			zap.Float64("bb_width_pct", bbWidth/price*100),
			zap.Float64("min_pct", s.cfg.BBWidthMin*100))
		return 0, 0
	}

	if price > bbLower*1.005 {
		s.log.Info("sig_reject", zap.String("fn", "reversionBuy"), zap.String("reason", "not_at_bb_lower"),
			zap.Float64("price", price), zap.Float64("bb_lower", bbLower),
			zap.Float64("px_above_lower_pct", (price-bbLower)/bbLower*100))
		return 0, 0
	}

	// Base confidence: 0.76 ensures entry when price touches BB band (RangeEntryConf=0.75).
	// Bonuses from RSI and BB penetration push conf higher for stronger signals.
	conf = 0.76
	if price < bbLower { conf += 0.05 } // actually below band

	rsiVals := indicator.RSI(closes, s.cfg.RSIPeriod)
	rsi := indicator.Last(rsiVals)
	if rsi < 40 { conf += 0.03 }
	if rsi < 35 { conf += 0.03 }
	if rsi < 30 { conf += 0.03 }

	if conf > 0.95 { conf = 0.95 }

	atr := s.calcATR()
	entryBuf := atr * 0.2
	entry = math.Round((price-entryBuf)*100) / 100

	s.lastBBMiddle = bbMiddle
	s.lastBBLower = bbLower
	s.lastBBUpper = bbUpper

	return conf, entry
}

func (s *AIStrategy) reversionSellSignal() (conf float64, entry float64) {
	closes := s.getCloses()
	if len(closes) < 30 {
		s.log.Info("sig_reject", zap.String("fn", "reversionSell"), zap.String("reason", "insufficient_bars"), zap.Int("closes", len(closes)))
		return 0, 0
	}

	price := closes[len(closes)-1]

	bb := indicator.BollingerBands(closes, s.cfg.BBPeriod, s.cfg.BBStdDev)
	if len(bb.Upper) == 0 {
		s.log.Info("sig_reject", zap.String("fn", "reversionSell"), zap.String("reason", "bb_unavailable"))
		return 0, 0
	}
	bbLower := bb.Lower[len(bb.Lower)-1]
	bbUpper := bb.Upper[len(bb.Upper)-1]
	bbMiddle := bb.Middle[len(bb.Middle)-1]

	// BB too narrow = no meaningful range to trade. Skip.
	bbWidth := bbUpper - bbLower
	if bbWidth < price*s.cfg.BBWidthMin {
		s.log.Info("sig_reject", zap.String("fn", "reversionSell"), zap.String("reason", "bb_narrow"),
			zap.Float64("bb_width_pct", bbWidth/price*100),
			zap.Float64("min_pct", s.cfg.BBWidthMin*100))
		return 0, 0
	}

	if price < bbUpper*0.995 {
		s.log.Info("sig_reject", zap.String("fn", "reversionSell"), zap.String("reason", "not_at_bb_upper"),
			zap.Float64("price", price), zap.Float64("bb_upper", bbUpper),
			zap.Float64("px_below_upper_pct", (bbUpper-price)/bbUpper*100))
		return 0, 0
	}

	conf = 0.76
	if price > bbUpper { conf += 0.05 }

	rsiVals := indicator.RSI(closes, s.cfg.RSIPeriod)
	rsi := indicator.Last(rsiVals)
	if rsi > 60 { conf += 0.03 }
	if rsi > 65 { conf += 0.03 }
	if rsi > 70 { conf += 0.03 }

	if conf > 0.95 { conf = 0.95 }

	atr := s.calcATR()
	entryBuf := atr * 0.2
	entry = math.Round((price+entryBuf)*100) / 100

	s.lastBBMiddle = bbMiddle
	s.lastBBLower = bbLower
	s.lastBBUpper = bbUpper

	return conf, entry
}

func r2(v float64) float64 { return math.Round(v*100) / 100 }
func r3(v float64) float64 { return math.Round(v*1000) / 1000 }

// isAdverseMomentum returns (true, reason) if recent 5m price action moves
// strongly against the position direction. Used to pause grid layer additions
// during sudden moves that the slower regime/trend_dir signals haven't caught
// up to yet (e.g. 凌晨急跌埋 LONG grid).
//
// Three triggers, any one fires:
//  1. single_bar_shock: current bar net move > 1.5×ATR against position
//  2. cum3_shock: 3-bar net close-to-close move > 2.0×ATR against position
//  3. range_break: current bar extreme breaks prior 5-bar high/low
func (s *AIStrategy) isAdverseMomentum(p *posState) (bool, string) {
	bars := s.primaryBars()
	if len(bars) < 6 { return false, "" }
	atr := s.calcATR()
	if atr <= 0 { return false, "" }

	cur := bars[len(bars)-1]

	// 1. Single-bar shock
	barMove := cur.Close - cur.Open
	if p.side == "LONG" && barMove < -atr*1.5 { return true, "single_bar_shock" }
	if p.side == "SHORT" && barMove > atr*1.5 { return true, "single_bar_shock" }

	// 2. Cumulative 3-bar move
	cum3 := bars[len(bars)-1].Close - bars[len(bars)-3].Close
	if p.side == "LONG" && cum3 < -atr*2.0 { return true, "cum3_shock" }
	if p.side == "SHORT" && cum3 > atr*2.0 { return true, "cum3_shock" }

	// 3. Current bar breaks prior 5-bar extreme
	prior5Low := bars[len(bars)-6].Low
	prior5High := bars[len(bars)-6].High
	for i := len(bars) - 6; i < len(bars)-1; i++ {
		if bars[i].Low < prior5Low { prior5Low = bars[i].Low }
		if bars[i].High > prior5High { prior5High = bars[i].High }
	}
	if p.side == "LONG" && cur.Low < prior5Low { return true, "range_break" }
	if p.side == "SHORT" && cur.High > prior5High { return true, "range_break" }

	return false, ""
}

// buildDiagFields returns regime-appropriate diagnostic fields for the per-bar
// summary log. Values are recomputed each call — do not rely on cached
// s.lastBB* because those are only written when the reversion signal ran past
// the BB check (exactly the path we want to diagnose when it doesn't fire).
func (s *AIStrategy) buildDiagFields(regime Regime) []zap.Field {
	closes := s.getCloses()
	if len(closes) < 30 { return nil }
	price := closes[len(closes)-1]
	rsi := r2(indicator.Last(indicator.RSI(closes, s.cfg.RSIPeriod)))

	if regime == RegimeRange {
		bb := indicator.BollingerBands(closes, s.cfg.BBPeriod, s.cfg.BBStdDev)
		if len(bb.Lower) == 0 { return []zap.Field{zap.Float64("rsi", rsi)} }
		lo := bb.Lower[len(bb.Lower)-1]
		mid := bb.Middle[len(bb.Middle)-1]
		up := bb.Upper[len(bb.Upper)-1]
		return []zap.Field{
			zap.Float64("bb_lower", r2(lo)),
			zap.Float64("bb_middle", r2(mid)),
			zap.Float64("bb_upper", r2(up)),
			zap.Float64("bb_width_pct", r3((up-lo)/price*100)),
			zap.Float64("px_above_lo_pct", r3((price-lo)/lo*100)),
			zap.Float64("px_below_up_pct", r3((up-price)/up*100)),
			zap.Float64("rsi", rsi),
		}
	}

	// Trend path: 10-bar high/low + breakout gap + bar range / ATR
	bars := s.primaryBars()
	if len(bars) < 12 { return []zap.Field{zap.Float64("rsi", rsi)} }
	lookback := 10
	hi10 := 0.0
	lo10 := math.MaxFloat64
	for i := len(bars) - lookback - 1; i < len(bars)-1; i++ {
		if i < 0 { continue }
		if bars[i].High > hi10 { hi10 = bars[i].High }
		if bars[i].Low < lo10 { lo10 = bars[i].Low }
	}
	atr := s.calcATR()
	cur := bars[len(bars)-1]
	barRangeATR := 0.0
	if atr > 0 { barRangeATR = r3((cur.High-cur.Low)/atr) }
	return []zap.Field{
		zap.Float64("hi10", r2(hi10)),
		zap.Float64("lo10", r2(lo10)),
		zap.Float64("brkout_long_pct", r3((price-hi10)/hi10*100)),
		zap.Float64("brkout_short_pct", r3((lo10-price)/lo10*100)),
		zap.Float64("bar_range_atr", barRangeATR),
		zap.Float64("rsi", rsi),
	}
}

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

