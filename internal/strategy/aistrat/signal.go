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

	// Check syncer for externally closed positions (only for FILLED positions).
	// Pending (unfilled) limit orders don't show as exchange positions — skip them
	// to avoid false "externally closed" that creates ghost positions.
	if s.syncer != nil {
		if s.longPos != nil && s.longPos.filled && !s.syncer.HasPosition("LONG") {
			s.log.Warn("AI: LONG externally closed — cancelling orphan orders + clearing state")
			if s.longPos.stagedTPPlaced || s.longPos.safetyNetSL {
				if ep, ok := ctx.Extra["staged_exit"].(strategy.StagedExitPlacer); ok {
					ep.CancelAllProtective(s.cfg.Symbol, "LONG")
				}
			}
			s.accumLong = 0
			s.accumShort = 0
			s.syncRemove("LONG")
			s.longPos = nil
		}
		if s.shortPos != nil && s.shortPos.filled && !s.syncer.HasPosition("SHORT") {
			s.log.Warn("AI: SHORT externally closed — cancelling orphan orders + clearing state")
			if s.shortPos.stagedTPPlaced || s.shortPos.safetyNetSL {
				if ep, ok := ctx.Extra["staged_exit"].(strategy.StagedExitPlacer); ok {
					ep.CancelAllProtective(s.cfg.Symbol, "SHORT")
				}
			}
			s.accumLong = 0
			s.accumShort = 0
			s.syncRemove("SHORT")
			s.shortPos = nil
		}
	}

	// Auto-place exchange orders for recovered positions (runs once per position).
	// Trend: staged TP limit orders + temporary safety-net exchange SL (until local trailing resumes).
	// Range: exchange algo SL (no staged TP).
	if s.longPos != nil && s.longPos.filled && s.longPos.mode == modeTrend && !s.longPos.stagedTPPlaced {
		s.placeStagedExitOrders(ctx, s.longPos)
		// Safety-net: place temporary exchange SL to protect during warmup (no local trailing yet).
		// Cancelled by first tickManage call once local trailing is active.
		if ep, ok := ctx.Extra["staged_exit"].(strategy.StagedExitPlacer); ok {
			if ep.PlaceExchangeSL(s.cfg.Symbol, "LONG", "SELL", s.longPos.remainQty, s.longPos.stopLoss) {
				s.longPos.safetyNetSL = true
				s.log.Info("AI: safety-net SL placed for recovered LONG", zap.Float64("sl", s.longPos.stopLoss))
			}
		}
	}
	if s.shortPos != nil && s.shortPos.filled && s.shortPos.mode == modeTrend && !s.shortPos.stagedTPPlaced {
		s.placeStagedExitOrders(ctx, s.shortPos)
		if ep, ok := ctx.Extra["staged_exit"].(strategy.StagedExitPlacer); ok {
			if ep.PlaceExchangeSL(s.cfg.Symbol, "SHORT", "BUY", s.shortPos.remainQty, s.shortPos.stopLoss) {
				s.shortPos.safetyNetSL = true
				s.log.Info("AI: safety-net SL placed for recovered SHORT", zap.Float64("sl", s.shortPos.stopLoss))
			}
		}
	}
	// Range/grid positions: no exchange SL (grid rides the range, exits only via TP).

	// Manage positions on primary bar too (SL, trailing, TP checks must run even if halted)
	if s.longPos != nil { s.managePos(ctx, bar, s.longPos, &s.longPos) }
	if s.shortPos != nil { s.managePos(ctx, bar, s.shortPos, &s.shortPos) }

	// Log position state every primary bar (essential for debugging trailing/exit behavior)
	if s.longPos != nil && s.longPos.filled {
		p := s.longPos
		pnlR := 0.0; if p.R > 0 { pnlR = (price - p.entryPrice) / p.R }
		s.log.Info("POS LONG",
			zap.Float64("entry", p.entryPrice), zap.Float64("price", price),
			zap.Float64("pnlR", r2(pnlR)), zap.Float64("peak", p.peakPrice),
			zap.Float64("trailing", p.trailing), zap.Float64("sl", p.stopLoss),
			zap.Int("tier", p.trailTier), zap.String("1h_mode", s.hourlyModeStr(p.side)),
			zap.Int("bars", p.barsHeld))
	}
	if s.shortPos != nil && s.shortPos.filled {
		p := s.shortPos
		pnlR := 0.0; if p.R > 0 { pnlR = (p.entryPrice - price) / p.R }
		s.log.Info("POS SHORT",
			zap.Float64("entry", p.entryPrice), zap.Float64("price", price),
			zap.Float64("pnlR", r2(pnlR)), zap.Float64("peak", p.peakPrice),
			zap.Float64("trailing", p.trailing), zap.Float64("sl", p.stopLoss),
			zap.Int("tier", p.trailTier), zap.String("1h_mode", s.hourlyModeStr(p.side)),
			zap.Int("bars", p.barsHeld))
	}

	// dayHalted blocks NEW entries only — existing position management above must still run.
	if s.dayHalted { return }

	// Track if we have pending orders (for post-GPT cancel logic)
	hasPendingLong := s.longPos != nil && !s.longPos.filled
	hasPendingShort := s.shortPos != nil && !s.shortPos.filled

	// Post-SL: skip this bar, let next bar handle fresh signal evaluation.
	if s.postSLReeval {
		s.postSLReeval = false
		s.log.Info("AI: post-SL cooldown — skip this bar",
			zap.String("closed_side", s.postSLSide), zap.Float64("sl_price", s.postSLPrice))
		return
	}

	// Don't open new positions on the same bar as a stop-loss (normal path)
	if s.stopBar == s.barCount { return }

	// ── Regime detection (every bar — sliding window, not batch) ──
	atr := s.calcATR()
	if price > 0 && atr/price > 0.05 {
		return // extreme volatility guard
	}
	regime := s.detectRegime()
	s.lastRegime = regime
	s.lastHourlyDir = s.hourlyTrendDir()

	// Grid positions: NO regime-based exit. Grid trades close via TP only.
	// Risk is managed by small qty per layer + max 2 layers cap.
	// Regime exit was actively harmful: it triggered on small moves ($5) that are
	// normal range oscillation, locking in losses at the worst price.

	// ── EXPANSION cooldown: wait 3 bars (15min) after breakout bar before entry ──
	// EXPANSION bars trigger FOMO entries at the worst price; let the retracement play out.
	if regime == RegimeExpansion {
		s.expansionBar = s.barCount
	}
	expansionCooldown := 3
	if s.expansionBar > 0 && s.barCount-s.expansionBar < expansionCooldown && s.longPos == nil && s.shortPos == nil {
		if s.barCount%6 == 0 || s.barCount-s.expansionBar == 0 {
			s.log.Info("AI: skip — EXPANSION cooldown",
				zap.Int("bars_since", s.barCount-s.expansionBar),
				zap.Int("cooldown", expansionCooldown),
				zap.Float64("price", price))
		}
		return
	}

	// Night session block removed — ETH trades 24h, US session (01:00-06:00 UTC+8) is often active.

	// ── TP cooldown: block TREND entries for 3 bars after TPs fill ──
	// Grid is exempt — TP fill means range is active, next reversion should be acted on promptly.
	tpCooldown := 3
	if s.tpCooldownBar > 0 && s.barCount-s.tpCooldownBar < tpCooldown && s.longPos == nil && s.shortPos == nil {
		if regime != RegimeRange { return }
	}

	// Clear external close flag (no longer needed for GPT forcing, but still consumed).
	if s.syncer != nil {
		s.syncer.PositionClosedExternally.CompareAndSwap(true, false)
	}

	// RANGE regime: skip entry if disabled.
	if regime == RegimeRange && s.longPos == nil && s.shortPos == nil && s.cfg.RangeEntryConf <= 0 {
		return
	}

	// ConsecLoss halt: only block trend entries. Grid mode is high-frequency
	// small-profit — occasional losses are normal cost, not signal failure.
	if s.consecLoss >= s.cfg.MaxConsecLoss && regime != RegimeRange && regime != RegimeSlowTrend {
		s.log.Warn("AI: trend halted — consecutive losses", zap.Int("consec", s.consecLoss))
		s.lastCallBar = s.barCount
		return
	}

	// ── Pure technical signals (GPT removed — zero latency, zero cost) ──
	s.lastCallBar = s.barCount
	s.totalCall++

	longConf, longEntry := s.techBuySignal()
	shortConf, shortEntry := s.techSellSignal()

	// Set reason string based on signal type
	var longReason, shortReason string
	if regime == RegimeStrongTrend || regime == RegimeExpansion {
		longReason = "breakout: price > 10-bar high"
		shortReason = "breakout: price < 10-bar low"
	} else {
		longReason = "reversion: RSI oversold + BB lower"
		shortReason = "reversion: RSI overbought + BB upper"
	}

	// Counter-trend hard clamp (trend mode only — reversion IS counter-trend)
	if regime == RegimeStrongTrend || regime == RegimeExpansion {
		ctCap := 0.20
		if s.lastTrendDir < 0 && longConf > ctCap { longConf = ctCap }
		if s.lastTrendDir > 0 && shortConf > ctCap { shortConf = ctCap }
	}

	// ── Entry thresholds: regime determines required confidence ──
	// STRONG_TREND/EXPANSION: with-trend uses lower threshold (easier to enter).
	// SLOW_TREND: midpoint threshold.
	// RANGE: disabled (RangeEntryConf=0 blocks entry).
	entryConfLong := s.cfg.ConfidenceThreshold
	entryConfShort := s.cfg.ConfidenceThreshold
	if regime == RegimeStrongTrend || regime == RegimeExpansion {
		if s.lastTrendDir >= 0 { entryConfLong = s.cfg.RegimeEntryConf }
		if s.lastTrendDir <= 0 { entryConfShort = s.cfg.RegimeEntryConf }
	} else if regime == RegimeSlowTrend {
		slowConf := (s.cfg.RegimeEntryConf + s.cfg.ConfidenceThreshold) / 2
		if s.lastTrendDir >= 0 { entryConfLong = slowConf }
		if s.lastTrendDir <= 0 { entryConfShort = slowConf }
	} else if regime == RegimeRange {
		if s.cfg.RangeEntryConf > 0 {
			entryConfLong = s.cfg.RangeEntryConf
			entryConfShort = s.cfg.RangeEntryConf
		}
	}

	// Raw confidence for logging
	rawLongConf := longConf
	rawShortConf := shortConf

	longExhausted, shortExhausted := false, false
	// ── Trend exhaustion filter: skip with-trend entry if 2h move is too large ──
	// Prevents chasing into extended trends that are likely to reverse.
	// Uses 15m bars (8 bars = 2 hours) to measure trend distance.
	if s.cfg.TrendExhaustPct > 0 {
		bars15 := s.barsForInterval("15m")
		if len(bars15) >= 8 {
			low4h := bars15[len(bars15)-8].Close
			high4h := bars15[len(bars15)-8].Close
			for _, b := range bars15[len(bars15)-8:] {
				if b.Low < low4h { low4h = b.Low }
				if b.High > high4h { high4h = b.High }
			}
			move4h := (high4h - low4h) / price
			current := bars15[len(bars15)-1].Close
			// Track exhaustion flags — applied after MTF scoring to avoid being overwritten.
			if move4h > s.cfg.TrendExhaustPct && (current-low4h)/(high4h-low4h) > 0.8 && longConf > 0 {
				s.log.Info("AI: BUY soft-limited — trend exhausted (near 2h high)",
					zap.Float64("move_pct", r3(move4h*100)))
				longExhausted = true
			}
			if move4h > s.cfg.TrendExhaustPct && (high4h-current)/(high4h-low4h) > 0.8 && shortConf > 0 {
				s.log.Info("AI: SELL soft-limited — trend exhausted (near 2h low)",
					zap.Float64("move_pct", r3(move4h*100)))
				shortExhausted = true
			}
		}
	}

	// ── 1h EMA direction filter ──
	// Trend mode: only trade with the hourly trend (counter-1h trades historically lost money).
	// Range/reversion mode: SKIP this filter — reversion signals ARE counter-trend by design.
	// Only RANGE uses reversion/grid mode. SLOW_TREND uses breakout/trend (it has direction).
	isReversion := regime == RegimeRange
	if !isReversion {
		if s.lastHourlyDir == -1 && longConf > 0 {
			s.log.Info("AI: BUY blocked — 1h EMA bearish", zap.Float64("conf", longConf))
			longConf = 0
			s.accumLong = 0
		}
		if s.lastHourlyDir == 1 && shortConf > 0 {
			s.log.Info("AI: SELL blocked — 1h EMA bullish", zap.Float64("conf", shortConf))
			shortConf = 0
			s.accumShort = 0
		}
	}

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
		s.log.Info("  BUY reason: "+longReason)
	}
	if shortConf >= entryConfShort {
		s.log.Info("  SELL reason: "+shortReason)
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

		// 15m EMA structure confirmation (±1): uses EMA10/30 (same as GPT buildContext).
		if len(c15) >= 30 {
			ema10_15 := indicator.Last(indicator.EMA(c15, 10))
			ema30_15 := indicator.Last(indicator.EMA(c15, 30))
			if ema10_15 > ema30_15 { mtfScore++ }  // bullish structure
			if ema10_15 < ema30_15 { mtfScore-- }  // bearish structure
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

	// MTF only scales qty, never blocks. Keeps the door open for reversal entries.
	switch {
	case mtfScore <= -3:
		longQtyScale = 0.3 // strong headwind → 30% qty
	case mtfScore == -2:
		longQtyScale = s.cfg.MTFQtyScaleHard // 70%
	case mtfScore == -1:
		longQtyScale = s.cfg.MTFQtyScaleSoft // 85%
	}

	switch {
	case mtfScore >= 3:
		shortQtyScale = 0.3 // strong headwind → 30% qty
	case mtfScore == 2:
		shortQtyScale = s.cfg.MTFQtyScaleHard
	case mtfScore == 1:
		shortQtyScale = s.cfg.MTFQtyScaleSoft
	}

	s.mtfLongScale = longQtyScale
	s.mtfShortScale = shortQtyScale

	// Apply trend exhaustion AFTER MTF scoring (otherwise MTF overwrites it).
	if longExhausted { s.mtfLongScale *= 0.5 }
	if shortExhausted { s.mtfShortScale *= 0.5 }

	// ── Rule-based boost (after MTF scoring, respects MTF + trend direction) ──
	swLow := s.findSwingLow(10)
	swHigh := s.findSwingHigh(10)
	// Swing/MTF boosts must respect trend direction:
	// In bearish STRONG_TREND, don't boost LONG; in bullish STRONG_TREND, don't boost SHORT.
	swingLongMTFOk := mtfScore >= -1 && s.lastTrendDir >= 0
	if price > 0 && swLow > 0 && (price-swLow)/price < s.cfg.SwingProximity && longConf >= s.cfg.BoostMinConf && longConf < entryConfLong && s.longPos == nil && swingLongMTFOk {
		s.log.Info("AI: boost long — price near swing low",
			zap.Float64("price", price), zap.Float64("swing_low", swLow), zap.Int("mtf", mtfScore))
		longConf = entryConfLong
		if longEntry <= 0 { longEntry = swLow }
	}
	swingShortMTFOk := mtfScore <= 1 && s.lastTrendDir <= 0
	if price > 0 && swHigh > 0 && (swHigh-price)/price < s.cfg.SwingProximity && shortConf >= s.cfg.BoostMinConf && shortConf < entryConfShort && s.shortPos == nil && swingShortMTFOk {
		s.log.Info("AI: boost short — price near swing high",
			zap.Float64("price", price), zap.Float64("swing_high", swHigh), zap.Int("mtf", mtfScore))
		shortConf = entryConfShort
		if shortEntry <= 0 { shortEntry = swHigh }
	}

	// ── MTF momentum boost (Trend only, with-trend direction only) ──
	if mtfScore >= 2 && s.lastTrendDir >= 0 && longConf > 0 && longConf >= s.cfg.BoostMinConf && longConf < entryConfLong && s.longPos == nil {
		s.log.Info("AI: MTF momentum boost → LONG",
			zap.Float64("conf_before", longConf), zap.Int("mtf", mtfScore))
		longConf = entryConfLong
		if longEntry <= 0 { longEntry = price - atr*s.cfg.EntryATRK }
	}
	if mtfScore <= -2 && s.lastTrendDir <= 0 && shortConf > 0 && shortConf >= s.cfg.BoostMinConf && shortConf < entryConfShort && s.shortPos == nil {
		s.log.Info("AI: MTF momentum boost → SHORT",
			zap.Float64("conf_before", shortConf), zap.Int("mtf", mtfScore))
		shortConf = entryConfShort
		if shortEntry <= 0 { shortEntry = price + atr*s.cfg.EntryATRK }
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

	// ── Direction conflict resolution ──
	// Grid/reversion mode: allow simultaneous long+short (hedge) — grid needs both sides.
	// Trend mode: single-direction only (strongest signal wins).
	hedgeAllowed := false
	if isReversion {
		// Grid mode: both directions allowed simultaneously.
		// Only block if both signals fire on the same bar (pick stronger).
		if longConf >= entryConfLong && shortConf >= entryConfShort {
			if longConf >= shortConf {
				shortConf = 0
			} else {
				longConf = 0
			}
		}
		// Existing opposite-side position does NOT block new entry in grid mode.
	} else if !s.cfg.HedgeMode {
		// Trend mode: single direction.
		if longConf >= entryConfLong && shortConf >= entryConfShort {
			if longConf >= shortConf {
				shortConf = 0
			} else {
				longConf = 0
			}
		}
		if s.longPos != nil && shortConf >= entryConfShort {
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

	// Entry price is now determined per-mode in the entry block below:
	// - Reversion: signal's entry price (near BB extreme)
	// - Breakout: market price (urgency)

	// ── Update pending limit orders: cancel if new signal provides better entry ──
	if s.longPos != nil && !s.longPos.filled && longConf >= entryConfLong && longEntry > 0 {
		if longEntry < s.longPos.entryPrice {
			s.log.Info("AI: updating pending LONG — better entry",
				zap.Float64("old", s.longPos.entryPrice), zap.Float64("new", longEntry))
			if s.longPos.orderID != "" { ctx.CancelOrder(s.longPos.orderID) }
			s.longPos = nil
		}
	}
	if s.shortPos != nil && !s.shortPos.filled && shortConf >= entryConfShort && shortEntry > 0 {
		if shortEntry > s.shortPos.entryPrice {
			s.log.Info("AI: updating pending SHORT — better entry",
				zap.Float64("old", s.shortPos.entryPrice), zap.Float64("new", shortEntry))
			if s.shortPos.orderID != "" { ctx.CancelOrder(s.shortPos.orderID) }
			s.shortPos = nil
		}
	}

	// ── Open LONG if confident ──
	if longConf >= entryConfLong && s.longPos == nil {
		// Grid mode: allow long even if short exists (hedge/grid needs both sides).
		// Trend mode: block if opposite side too close (avoid churn).
		if isReversion {
			// Grid: only block if short is modeRange and very close (same grid level)
			if s.shortPos != nil && s.shortPos.mode == modeRange {
				if math.Abs(s.shortPos.entryPrice-price) < minSpread { longConf = 0 }
			}
		} else {
			if s.shortPos != nil {
				if math.Abs(s.shortPos.entryPrice-price) < minSpread { longConf = 0 }
			}
		}
		if longConf > 0 {
			var entry float64
			if isReversion {
				// Reversion: use signal's entry directly (near BB extreme)
				entry = longEntry
				if entry <= 0 { entry = math.Round(price*100) / 100 }
				s.lastConf = longConf
				s.openGrid(ctx, "LONG", price, entry, atr)
				if s.longPos != nil { s.longPos.entryRegime = regime }
			} else {
				// Breakout: market price entry (urgency, no pullback blending)
				entry = math.Round(price*100) / 100
				gptTP := shortEntry
				if hedgeAllowed && s.shortPos != nil {
					s.openHedgeScalp(ctx, "LONG", price, entry, atr, s.shortPos)
				} else {
					s.lastConf = longConf
					s.openTrend(ctx, "LONG", price, entry, atr, gptTP)
					if s.longPos != nil { s.longPos.entryRegime = regime }
				}
			}
		}
	}

	// ── Open SHORT if confident ──
	if shortConf >= entryConfShort && s.cfg.EnableShort && s.shortPos == nil {
		if isReversion {
			if s.longPos != nil && s.longPos.mode == modeRange {
				if math.Abs(s.longPos.entryPrice-price) < minSpread { shortConf = 0 }
			}
		} else {
			if s.longPos != nil {
				if math.Abs(s.longPos.entryPrice-price) < minSpread { shortConf = 0 }
			}
		}
		if shortConf > 0 {
			var entry float64
			if isReversion {
				entry = shortEntry
				if entry <= 0 { entry = math.Round(price*100) / 100 }
				s.lastConf = shortConf
				s.openGrid(ctx, "SHORT", price, entry, atr)
				if s.shortPos != nil { s.shortPos.entryRegime = regime }
			} else {
				entry = math.Round(price*100) / 100
				gptTP := longEntry
				if hedgeAllowed && s.longPos != nil {
					s.openHedgeScalp(ctx, "SHORT", price, entry, atr, s.longPos)
				} else {
					s.lastConf = shortConf
					s.openTrend(ctx, "SHORT", price, entry, atr, gptTP)
					if s.shortPos != nil { s.shortPos.entryRegime = regime }
				}
			}
		}
	}
}
