package aistrat

import (
	"fmt"
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/redis/go-redis/v9"
)

// ─── OnBar ───────────────────────────────────────────────────────────────────

func (s *AIStrategy) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if bar.Symbol != s.cfg.Symbol { return }

	// ── Buffer bar by interval ──
	iv := bar.Interval
	if iv == "" { iv = s.cfg.PrimaryInterval }
	s.barsByInterval[iv] = append(s.barsByInterval[iv], bar)
	maxBuf := s.cfg.LookbackBars * 2
	if len(s.barsByInterval[iv]) > maxBuf {
		s.barsByInterval[iv] = s.barsByInterval[iv][len(s.barsByInterval[iv])-maxBuf:]
	}

	// ── Early Redis init (needed before warmup for backtest replay detection) ──
	if s.rdb == nil {
		if v, ok := ctx.Extra["redis_client"]; ok {
			if rc, ok := v.(*redis.Client); ok { s.rdb = rc }
		}
	}
	// Pre-load replay signals once — ONLY in explicit backtest mode.
	// Detected by ctx.Extra["backtest_replay"]=true (set by backtest engine, never by live engine).
	if s.rdb != nil && len(s.replaySignals) == 0 && !s.warmedUp {
		if replay, ok := ctx.Extra["backtest_replay"].(bool); ok && replay {
			if s.hasCachedSignals() {
				s.loadReplaySignals()
			}
		}
	}

	// ── Warmup: need enough primary-interval bars ──
	primaryBars := s.barsByInterval[s.cfg.PrimaryInterval]
	if !s.warmedUp {
		if len(primaryBars) >= s.cfg.LookbackBars {
			s.warmedUp = true
			s.dayStart = time.Now()
			if pf := ctx.Portfolio; pf != nil {
				s.dayStartEquity = pf.Equity(map[string]float64{s.cfg.Symbol: bar.Close})
			}
			if v, ok := ctx.Extra["position_syncer"]; ok {
				if ps, ok := v.(*position.Syncer); ok {
					s.syncer = ps
				}
			}
			if v, ok := ctx.Extra["redis_client"]; ok {
				if rc, ok := v.(*redis.Client); ok {
					s.rdb = rc
				}
			}
			if v, ok := ctx.Extra["data_store"]; ok {
				if st, ok := v.(*data.Store); ok { s.store = st }
			}
			if v, ok := ctx.Extra["user_id"]; ok {
				if uid, ok := v.(int); ok { s.userID = uid }
			}
			if v, ok := ctx.Extra["engine_id"]; ok {
				if eid, ok := v.(string); ok { s.engineID = eid }
			}
			s.recoverFromSyncer(bar.Close)
			s.log.Info("AI warmed up",
				zap.Int("primary_bars", len(primaryBars)),
				zap.String("primary", s.cfg.PrimaryInterval),
				zap.Bool("syncer", s.syncer != nil),
				zap.Bool("long", s.longPos != nil),
				zap.Bool("short", s.shortPos != nil))
		}
		return
	}

	price := bar.Close
	isStaleBar := len(s.replaySignals) == 0 && time.Since(bar.CloseTime) > 2*time.Minute

	// ── 1m bars: precision stop/timeout management only ──
	// Skip stale 1m bars to prevent false stop-loss on backfill.
	if iv != s.cfg.PrimaryInterval {
		if !isStaleBar {
			if s.longPos != nil { s.managePos(ctx, bar, s.longPos, &s.longPos) }
			if s.shortPos != nil { s.managePos(ctx, bar, s.shortPos, &s.shortPos) }
		}
		return
	}

	// ── Primary interval bars: full logic below ──
	s.barCount++
	// Skip GPT calls on stale backfill bars; wait for first real-time bar.
	// Exception: backtest replay mode uses cached signals.
	if !s.liveReady {
		if time.Since(bar.CloseTime) < 2*time.Minute {
			s.liveReady = true
			s.log.Info("AI: live ready — first real-time bar")
		} else if len(s.replaySignals) > 0 {
			s.liveReady = true
			s.log.Info("AI: backtest replay mode — using cached signals", zap.Int("signals", len(s.replaySignals)))
		} else {
			return
		}
	}
	s.checkDayReset(ctx, price)
	if s.dayHalted { return }

	// Check syncer for externally closed positions
	if s.syncer != nil {
		if s.longPos != nil && !s.syncer.HasPosition("LONG") {
			s.log.Warn("AI: LONG externally closed — clearing posState")
			s.longPos = nil
		}
		if s.shortPos != nil && !s.syncer.HasPosition("SHORT") {
			s.log.Warn("AI: SHORT externally closed — clearing posState")
			s.shortPos = nil
		}
	}

	// Auto-place exchange orders for recovered positions (runs once per position).
	// Trend: staged TP limit orders (no exchange SL).
	// Range: exchange algo SL (no staged TP).
	if s.longPos != nil && s.longPos.filled && s.longPos.mode == modeTrend && !s.longPos.stagedTPPlaced {
		s.placeStagedExitOrders(ctx, s.longPos)
	}
	if s.shortPos != nil && s.shortPos.filled && s.shortPos.mode == modeTrend && !s.shortPos.stagedTPPlaced {
		s.placeStagedExitOrders(ctx, s.shortPos)
	}
	// Range exchange SL: use stagedTPPlaced as flag to prevent duplicate SL placement.
	// Reusing the flag is OK because Range never has staged TP (only exchange SL).
	if s.longPos != nil && s.longPos.filled && s.longPos.mode == modeRange && !s.longPos.stagedTPPlaced {
		if ep, ok := ctx.Extra["staged_exit"].(strategy.StagedExitPlacer); ok {
			if ep.PlaceExchangeSL(s.cfg.Symbol, "LONG", "SELL", s.longPos.remainQty, s.longPos.stopLoss) {
				s.longPos.stagedTPPlaced = true // reuse flag to prevent re-placement
			}
		}
	}
	if s.shortPos != nil && s.shortPos.filled && s.shortPos.mode == modeRange && !s.shortPos.stagedTPPlaced {
		if ep, ok := ctx.Extra["staged_exit"].(strategy.StagedExitPlacer); ok {
			if ep.PlaceExchangeSL(s.cfg.Symbol, "SHORT", "BUY", s.shortPos.remainQty, s.shortPos.stopLoss) {
				s.shortPos.stagedTPPlaced = true
			}
		}
	}

	// Manage positions on primary bar too
	if s.longPos != nil { s.managePos(ctx, bar, s.longPos, &s.longPos) }
	if s.shortPos != nil { s.managePos(ctx, bar, s.shortPos, &s.shortPos) }

	// Track if we have pending orders (for post-GPT cancel logic)
	hasPendingLong := s.longPos != nil && !s.longPos.filled
	hasPendingShort := s.shortPos != nil && !s.shortPos.filled

	// Don't open new positions on the same bar as a stop-loss
	if s.stopBar == s.barCount { return }

	// ── Regime detection (every bar — sliding window, not batch) ──
	atr := s.calcATR()
	if price > 0 && atr/price > 0.05 {
		return // extreme volatility guard
	}
	regime := s.detectRegime()
	s.lastRegime = regime

	// Decay signal accumulation every bar (not just on GPT call),
	// so stale signals fade even when regime blocks GPT calls.
	s.accumLong *= s.cfg.SignalDecay
	s.accumShort *= s.cfg.SignalDecay
	if s.accumLong < 0.01 { s.accumLong = 0 }
	if s.accumShort < 0.01 { s.accumShort = 0 }

	if regime == RegimeRange && s.longPos == nil && s.shortPos == nil {
		if s.barCount%12 == 0 { // log every hour (12 × 5min)
			bars := s.primaryBars()
			ts := 0.0
			if len(bars) > s.cfg.RegimeN && atr > 0 {
				ts = math.Abs(price-bars[len(bars)-s.cfg.RegimeN].Close) / atr
			}
			s.log.Info("AI: skip — RANGE regime (no trend)",
				zap.Float64("atr", atr), zap.Float64("trend_strength", ts),
				zap.Float64("price", price))
		}
		return
	}

	// Force immediate GPT check if a position was closed externally (SL hit, manual close).
	forceCheck := false
	if s.syncer != nil && s.syncer.PositionClosedExternally.CompareAndSwap(true, false) {
		forceCheck = true
		s.log.Info("AI: position closed externally — forcing immediate signal check")
	}

	// GPT signal check (every N primary bars, or immediately if forced)
	interval := s.cfg.CallIntervalBars
	if interval < 1 { interval = 1 }
	if !forceCheck && s.barCount-s.lastCallBar < interval { return }

	if s.consecLoss >= s.cfg.MaxConsecLoss {
		s.log.Warn("AI: halted — consecutive losses", zap.Int("consec", s.consecLoss))
		s.lastCallBar = s.barCount
		return
	}

	var signal gptSignal
	var err error
	if len(s.replaySignals) > 0 {
		// Backtest replay: use cached signal
		signal, err = s.nextReplaySignal()
		if err != nil {
			s.lastCallBar = s.barCount
			return
		}
	} else {
		// Live mode: call GPT
		mktCtx := s.buildContext(ctx, bar)
		signal, err = s.callGPT(mktCtx)
		if err != nil {
			// Retry once on transient failure (empty response, EOF, timeout)
			time.Sleep(500 * time.Millisecond)
			signal, err = s.callGPT(mktCtx)
			if err != nil {
				s.log.Warn("AI: GPT failed (after retry)", zap.Error(err))
				s.lastCallBar = s.barCount
				return
			}
		}
		s.cacheSignal(bar, signal)
	}
	s.lastCallBar = s.barCount
	s.totalCall++

	// Log both signals
	longConf, shortConf := 0.0, 0.0
	longEntry, shortEntry := 0.0, 0.0
	longReason, shortReason := "", ""
	if signal.Long != nil {
		longConf = signal.Long.Confidence
		longEntry = signal.Long.EntryPrice
		longReason = signal.Long.Reasoning
	}
	if signal.Short != nil {
		shortConf = signal.Short.Confidence
		shortEntry = signal.Short.EntryPrice
		shortReason = signal.Short.Reasoning
	}

	// Backward compat: if GPT returns old format (action/confidence), convert
	if signal.Long == nil && signal.Short == nil && signal.Action != "" {
		if signal.Action == "BUY" {
			longConf = signal.Confidence
			longEntry = signal.EntryPrice
			longReason = signal.Reasoning
		} else if signal.Action == "SELL" {
			shortConf = signal.Confidence
			shortEntry = signal.EntryPrice
			shortReason = signal.Reasoning
		}
	}

	// ── Signal accumulation: rolling confidence across bars ──
	// Decay already applied at regime detection (every bar).
	// Here we only ADD current GPT signal contribution.
	cap := s.cfg.SignalAccumMax
	if longConf > 0.3 { s.accumLong += longConf - 0.3 }
	if shortConf > 0.3 { s.accumShort += shortConf - 0.3 }
	// Cap
	if s.accumLong > cap { s.accumLong = cap }
	if s.accumShort > cap { s.accumShort = cap }
	// Conflicting signals cancel each other
	if s.accumLong > 0 && s.accumShort > 0 {
		diff := s.accumLong - s.accumShort
		if diff > 0 { s.accumLong = diff; s.accumShort = 0 } else { s.accumShort = -diff; s.accumLong = 0 }
	}

	// Effective confidence = max(raw GPT, accumulated signal)
	effectiveLong := math.Max(longConf, s.accumLong)
	effectiveShort := math.Max(shortConf, s.accumShort)

	// ── Two-layer decision: regime determines conditions, GPT adds weight ──
	// The lower threshold (RegimeEntryConf=0.60) only applies to the WITH-trend direction.
	// Counter-trend entries must still meet the full ConfidenceThreshold (0.82).
	// This prevents opening LONG in a bearish STRONG_TREND (which caused the 15:04 bad trade).
	entryConfLong := s.cfg.ConfidenceThreshold
	entryConfShort := s.cfg.ConfidenceThreshold
	if regime == RegimeStrongTrend || regime == RegimeExpansion {
		if s.lastTrendDir >= 0 { entryConfLong = s.cfg.RegimeEntryConf }   // bullish → lower long threshold
		if s.lastTrendDir <= 0 { entryConfShort = s.cfg.RegimeEntryConf }  // bearish → lower short threshold
	}
	// entryConfLong / entryConfShort used throughout below for directional entry decisions.

	// Use effective (accumulated) confidence for entry decisions.
	// If accumulated signal boosted confidence above raw GPT, entry price may be stale.
	if effectiveLong > longConf+0.05 && longEntry > 0 {
		longEntry = 0
	}
	if effectiveShort > shortConf+0.05 && shortEntry > 0 {
		shortEntry = 0
	}
	// Keep raw GPT confidence for accurate logging before overwriting with effective values.
	rawLongConf := longConf
	rawShortConf := shortConf
	longConf = effectiveLong
	shortConf = effectiveShort

	// Summary line for quick scanning
	action := "HOLD"
	if longConf >= entryConfLong && shortConf >= entryConfShort {
		action = "BOTH"
	} else if longConf >= entryConfLong {
		action = "BUY"
	} else if shortConf >= entryConfShort {
		action = "SELL"
	}
	s.log.Info("AI signal → "+action,
		zap.Float64("price", price), zap.String("regime", string(regime)),
		zap.Int("trend_dir", s.lastTrendDir),
		zap.Float64("raw_L", rawLongConf), zap.Float64("raw_S", rawShortConf),
		zap.Float64("eff_L", longConf), zap.Float64("eff_S", shortConf),
		zap.Float64("L_entry", longEntry), zap.Float64("S_entry", shortEntry),
		zap.Float64("accum_L", s.accumLong), zap.Float64("accum_S", s.accumShort),
		zap.Int("call", s.totalCall),
	)
	if longConf >= entryConfLong {
		src := "GPT"
		if rawLongConf < entryConfLong { src = "accum" }
		s.log.Info("  BUY reason ("+src+"): "+longReason)
	}
	if shortConf >= entryConfShort {
		src := "GPT"
		if rawShortConf < entryConfShort { src = "accum" }
		s.log.Info("  SELL reason ("+src+"): "+shortReason)
	}
	s.logEvent("signal", action, "", price, 0, 0, math.Max(longConf, shortConf), 0,
		fmt.Sprintf(`{"raw_L":%.2f,"raw_S":%.2f,"eff_L":%.2f,"eff_S":%.2f,"L_entry":%.2f,"S_entry":%.2f}`,
			rawLongConf, rawShortConf, longConf, shortConf, longEntry, shortEntry))

	// Minimum spread between long and short to avoid self-hedging.
	// Use ATR-based spread: entries must be at least 1×ATR apart.
	minSpread := atr
	if minSpread < price*0.002 { minSpread = price * 0.002 } // floor: 0.2% of price

	// ── Multi-timeframe scoring (must run before single-direction check and boost) (replaces hard block) ──
	// Score: positive = bullish, negative = bearish. Range -5 to +5.
	// Components: 15m return (±2) + 15m EMA structure (±1) + 5m momentum (±1) + 1m change (±1)
	mtfScore := 0

	// 15m trend score (±2): based on 8-bar return
	bars15 := s.barsForInterval("15m")
	if len(bars15) >= 8 {
		c15 := make([]float64, len(bars15))
		for i, b := range bars15 { c15[i] = b.Close }
		ret15 := (c15[len(c15)-1] - c15[len(c15)-8]) / c15[len(c15)-8]
		if ret15 > s.cfg.MTFStrongTrend { mtfScore += 2 } else if ret15 > s.cfg.MTFWeakTrend { mtfScore += 1 }
		if ret15 < -s.cfg.MTFStrongTrend { mtfScore -= 2 } else if ret15 < -s.cfg.MTFWeakTrend { mtfScore -= 1 }

		// 15m EMA structure confirmation (±1): prevents bounces from flipping the score.
		// If EMA20 < EMA50, the macro trend is bearish regardless of short-term return.
		if len(c15) >= s.cfg.EMASlow {
			ema20_15 := indicator.Last(indicator.EMA(c15, s.cfg.EMAFast))
			ema50_15 := indicator.Last(indicator.EMA(c15, s.cfg.EMASlow))
			if ema20_15 > ema50_15 { mtfScore++ }  // bullish structure
			if ema20_15 < ema50_15 { mtfScore-- }  // bearish structure
		}
	}

	// 5m momentum score (±1): MACD AND RSI must agree (prevents conflicting signals from cancelling)
	closes5m := s.getCloses()
	if len(closes5m) >= 14 {
		rsi5m := indicator.Last(indicator.RSI(closes5m, s.cfg.RSIPeriod))
		macd5m := indicator.MACD(closes5m, s.cfg.MACDFast, s.cfg.MACDSlow, s.cfg.MACDSignal)
		macdHist5m := indicator.Last(macd5m.Histogram)
		if macdHist5m > 0 && rsi5m > s.cfg.MTFBearRSI { mtfScore++ }      // bullish: MACD positive + RSI not oversold
		if macdHist5m < 0 && rsi5m < s.cfg.MTFBullRSI { mtfScore-- }      // bearish: MACD negative + RSI not overbought
	}

	// 1m short-term score (±1): net change over last 3 bars
	bars1m := s.barsForInterval("1m")
	if len(bars1m) >= 3 {
		last3 := bars1m[len(bars1m)-3:]
		netChange := (last3[2].Close - last3[0].Close) / last3[0].Close
		if netChange > s.cfg.MTF1mThreshold { mtfScore++ }   // > +0.1%
		if netChange < -s.cfg.MTF1mThreshold { mtfScore-- }  // < -0.1%
	}

	s.lastMTFScore = mtfScore
	s.log.Info("AI: MTF score", zap.Int("score", mtfScore))

	// Strong MTF trend overrides Range regime — but only if ATR confirms trend expansion.
	// ── Trend: MTF momentum filter ──
	// Positive MTF = bullish (headwind for SHORT), negative = bearish (headwind for LONG)
	longQtyScale, shortQtyScale := 1.0, 1.0

	switch {
	case mtfScore <= -3:
		if longConf > 0 {
			s.log.Info("AI: BUY blocked — MTF strongly bearish", zap.Int("score", mtfScore))
			longConf = 0
		}
	case mtfScore == -2:
		longQtyScale = s.cfg.MTFQtyScaleHard
	case mtfScore == -1:
		longQtyScale = s.cfg.MTFQtyScaleSoft
	}

	switch {
	case mtfScore >= 3:
		if shortConf > 0 {
			s.log.Info("AI: SELL blocked — MTF strongly bullish", zap.Int("score", mtfScore))
			shortConf = 0
		}
	case mtfScore == 2:
		shortQtyScale = s.cfg.MTFQtyScaleHard
	case mtfScore == 1:
		shortQtyScale = s.cfg.MTFQtyScaleSoft
	}

	s.mtfLongScale = longQtyScale
	s.mtfShortScale = shortQtyScale

	// ── Rule-based boost (after MTF scoring, respects MTF + trend direction) ──
	swLow := s.findSwingLow(10)
	swHigh := s.findSwingHigh(10)
	// Swing/MTF boosts must respect trend direction:
	// In bearish STRONG_TREND, don't boost LONG; in bullish STRONG_TREND, don't boost SHORT.
	swingLongMTFOk := mtfScore >= -1 && s.lastTrendDir >= 0
	swingConfMin := 0.60
	if price > 0 && swLow > 0 && (price-swLow)/price < s.cfg.SwingProximity && longConf >= swingConfMin && longConf < entryConfLong && s.longPos == nil && swingLongMTFOk {
		s.log.Info("AI: boost long — price near swing low",
			zap.Float64("price", price), zap.Float64("swing_low", swLow), zap.Int("mtf", mtfScore))
		longConf = entryConfLong
		if longEntry <= 0 { longEntry = swLow }
	}
	swingShortMTFOk := mtfScore <= 1 && s.lastTrendDir <= 0
	if price > 0 && swHigh > 0 && (swHigh-price)/price < s.cfg.SwingProximity && shortConf >= swingConfMin && shortConf < entryConfShort && s.shortPos == nil && swingShortMTFOk {
		s.log.Info("AI: boost short — price near swing high",
			zap.Float64("price", price), zap.Float64("swing_high", swHigh), zap.Int("mtf", mtfScore))
		shortConf = entryConfShort
		if shortEntry <= 0 { shortEntry = swHigh }
	}

	// ── MTF momentum boost (Trend only, with-trend direction only) ──
	if mtfScore >= 2 && s.lastTrendDir >= 0 && longConf > 0 && longConf >= 0.50 && longConf < entryConfLong && s.longPos == nil {
		s.log.Info("AI: MTF momentum boost → LONG",
			zap.Float64("conf_before", longConf), zap.Int("mtf", mtfScore))
		longConf = entryConfLong
		if longEntry <= 0 { longEntry = price - price*s.cfg.EntryOffsetPct }
	}
	if mtfScore <= -2 && s.lastTrendDir <= 0 && shortConf > 0 && shortConf >= 0.50 && shortConf < entryConfShort && s.shortPos == nil {
		s.log.Info("AI: MTF momentum boost → SHORT",
			zap.Float64("conf_before", shortConf), zap.Int("mtf", mtfScore))
		shortConf = entryConfShort
		if shortEntry <= 0 { shortEntry = price + price*s.cfg.EntryOffsetPct }
	}

	// ── Cancel pending orders if GPT signal reversed ──
	if hasPendingLong && shortConf >= s.cfg.ReversalConf {
		s.log.Info("AI: cancelling pending LONG — signal reversed to SHORT")
		if s.longPos.orderID != "" { ctx.CancelOrder(s.longPos.orderID) }
		s.longPos = nil
	}
	if hasPendingShort && longConf >= s.cfg.ReversalConf {
		s.log.Info("AI: cancelling pending SHORT — signal reversed to LONG")
		if s.shortPos.orderID != "" { ctx.CancelOrder(s.shortPos.orderID) }
		s.shortPos = nil
	}

	// ── Single-direction mode: only open the strongest signal (after MTF + boost) ──
	// Exception: HedgeOnDrawdown allows counter-trend Range scalp when main position is losing.
	hedgeAllowed := false
	if !s.cfg.HedgeMode {
		if longConf >= entryConfLong && shortConf >= entryConfShort {
			if longConf >= shortConf {
				shortConf = 0
			} else {
				longConf = 0
			}
		}
		if s.longPos != nil && shortConf >= entryConfShort {
			// LONG main losing + SHORT signal → hedge?
			if s.cfg.HedgeOnDrawdown && s.canHedge(price, s.longPos) {
				hedgeAllowed = true
				s.log.Info("AI: hedge-on-drawdown → SHORT scalp",
					zap.Float64("main_entry", s.longPos.entryPrice),
					zap.Float64("price", price))
			} else {
				shortConf = 0
			}
		}
		if s.shortPos != nil && longConf >= entryConfLong {
			// SHORT main losing + LONG signal → hedge?
			if s.cfg.HedgeOnDrawdown && s.canHedge(price, s.shortPos) {
				hedgeAllowed = true
				s.log.Info("AI: hedge-on-drawdown → LONG scalp",
					zap.Float64("main_entry", s.shortPos.entryPrice),
					zap.Float64("price", price))
			} else {
				longConf = 0
			}
		}
	}

	// Entry: pick the better of GPT price vs 0.10% offset
	// LONG: lower is better; SHORT: higher is better
	entryOffset := price * s.cfg.EntryOffsetPct
	maxDev := price * s.cfg.MaxEntryDevPct // cap GPT entry within configured % of current price

	// ── Update pending limit orders if better entry available ──
	if s.longPos != nil && !s.longPos.filled && longConf >= entryConfLong {
		newEntry := price - entryOffset
		if longEntry > 0 && longEntry < newEntry && (price-longEntry) <= maxDev { newEntry = longEntry }
		newEntry = math.Round(newEntry*100) / 100
		// Replace if new entry is closer to current price (more likely to fill)
		if math.Abs(price-newEntry) < math.Abs(price-s.longPos.entryPrice) {
			s.log.Info("AI: updating pending LONG — better entry",
				zap.Float64("old_entry", s.longPos.entryPrice), zap.Float64("new_entry", newEntry))
			if s.longPos.orderID != "" { ctx.CancelOrder(s.longPos.orderID) }
			s.longPos = nil // will be re-created below
		}
	}
	if s.shortPos != nil && !s.shortPos.filled && shortConf >= entryConfShort {
		newEntry := price + entryOffset
		if shortEntry > 0 && shortEntry > newEntry && (shortEntry-price) <= maxDev { newEntry = shortEntry }
		newEntry = math.Round(newEntry*100) / 100
		if math.Abs(price-newEntry) < math.Abs(price-s.shortPos.entryPrice) {
			s.log.Info("AI: updating pending SHORT — better entry",
				zap.Float64("old_entry", s.shortPos.entryPrice), zap.Float64("new_entry", newEntry))
			if s.shortPos.orderID != "" { ctx.CancelOrder(s.shortPos.orderID) }
			s.shortPos = nil
		}
	}

	// ── Open LONG if confident ──
	if longConf >= entryConfLong && s.longPos == nil {
		if s.shortPos != nil {
			if math.Abs(s.shortPos.entryPrice-price) < minSpread { longConf = 0 }
		}
		if longConf > 0 {
			var entry float64
			// LONG wants to buy LOW. GPT entry (support) is typically below current price.
			// Use GPT entry if it's below current price (better deal); otherwise market entry.
			offsetEntry := price - entryOffset
			entry = offsetEntry
			if longEntry > 0 && longEntry < entry && (price-longEntry) <= maxDev {
				entry = longEntry // GPT found a better support level
			}
			// Regime-based entry mode
			switch regime {
			case RegimeStrongTrend, RegimeExpansion:
				entry = price // market entry — trend/breakout won't wait
				s.log.Info("AI: market entry", zap.String("side", "LONG"),
					zap.String("regime", string(regime)), zap.Float64("conf", longConf))
			default: // SLOW_TREND
				// Use limit entry; strong MTF or high conf → market
				if longConf >= s.cfg.MarketEntryConf || (mtfScore >= 3 && longConf >= entryConfLong) {
					entry = price
					s.log.Info("AI: market entry (conf/MTF)", zap.String("side", "LONG"), zap.Float64("conf", longConf), zap.Int("mtf", mtfScore))
				}
			}
			entry = math.Round(entry*100) / 100
			if hedgeAllowed && s.shortPos != nil {
				s.openHedgeScalp(ctx, "LONG", price, entry, atr, s.shortPos)
			} else {
				s.lastConf = longConf
				s.openTrend(ctx, "LONG", price, entry, atr)
				if s.longPos != nil { s.longPos.entryRegime = regime }
			}
			// GPT entry as grid add-on if significantly better than actual entry
			if !hedgeAllowed && s.longPos != nil && s.longPos.filled && longEntry > 0 && longEntry < entry && (entry-longEntry)/entry > 0.002 {
				s.addGPTGrid(s.longPos, "LONG", longEntry)
			}
		}
	}

	// ── Open SHORT if confident ──
	if shortConf >= entryConfShort && s.cfg.EnableShort && s.shortPos == nil {
		if s.longPos != nil {
			if math.Abs(s.longPos.entryPrice-price) < minSpread { shortConf = 0 }
		}
		if shortConf > 0 {
			var entry float64
			// SHORT wants to sell HIGH. GPT entry (resistance) is typically above current price.
			// Use GPT entry if it's above current price (better deal); otherwise market entry.
			offsetEntry := price + entryOffset
			entry = offsetEntry
			if shortEntry > 0 && shortEntry > entry && (shortEntry-price) <= maxDev {
				entry = shortEntry // GPT found a better resistance level
			}
			// Regime-based entry mode
			switch regime {
			case RegimeStrongTrend, RegimeExpansion:
				entry = price // market entry — trend/breakout won't wait
				s.log.Info("AI: market entry", zap.String("side", "SHORT"),
					zap.String("regime", string(regime)), zap.Float64("conf", shortConf))
			default: // SLOW_TREND
				// Use limit entry; strong MTF or high conf → market
				if shortConf >= s.cfg.MarketEntryConf || (mtfScore <= -3 && shortConf >= entryConfShort) {
					entry = price
					s.log.Info("AI: market entry (conf/MTF)", zap.String("side", "SHORT"), zap.Float64("conf", shortConf), zap.Int("mtf", mtfScore))
				}
			}
			entry = math.Round(entry*100) / 100
			if hedgeAllowed && s.longPos != nil {
				s.openHedgeScalp(ctx, "SHORT", price, entry, atr, s.longPos)
			} else {
				s.lastConf = shortConf
				s.openTrend(ctx, "SHORT", price, entry, atr)
				if s.shortPos != nil { s.shortPos.entryRegime = regime }
			}
			// GPT entry as grid add-on if significantly better than actual entry
			if !hedgeAllowed && s.shortPos != nil && s.shortPos.filled && shortEntry > 0 && shortEntry > entry && (shortEntry-entry)/entry > 0.002 {
				s.addGPTGrid(s.shortPos, "SHORT", shortEntry)
			}
		}
	}
}
