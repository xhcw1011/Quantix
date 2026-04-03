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

	// ── Warmup: need enough primary-interval bars ──
	primaryBars := s.barsByInterval[s.cfg.PrimaryInterval]
	if !s.warmedUp {
		if len(primaryBars) >= s.cfg.LookbackBars && time.Since(bar.CloseTime) < 10*time.Minute {
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

	// ── Skip stale bars for position management (prevent false stop-loss on backfill) ──
	if time.Since(bar.CloseTime) > 2*time.Minute {
		return
	}

	// ── 1m bars: precision stop/timeout management only ──
	if iv != s.cfg.PrimaryInterval {
		if s.longPos != nil { s.managePos(ctx, bar, s.longPos, &s.longPos) }
		if s.shortPos != nil { s.managePos(ctx, bar, s.shortPos, &s.shortPos) }
		return
	}

	// ── Primary interval bars: full logic below ──
	s.barCount++
	// Skip GPT calls on stale backfill bars; wait for first real-time bar
	if !s.liveReady {
		if time.Since(bar.CloseTime) < 2*time.Minute {
			s.liveReady = true
			s.log.Info("AI: live ready — first real-time bar")
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

	// Auto-place staged TP for recovered Trend positions that don't have them yet.
	if s.longPos != nil && s.longPos.filled && s.longPos.mode == modeTrend && !s.longPos.stagedTPPlaced {
		s.placeStagedExitOrders(ctx, s.longPos)
	}
	if s.shortPos != nil && s.shortPos.filled && s.shortPos.mode == modeTrend && !s.shortPos.stagedTPPlaced {
		s.placeStagedExitOrders(ctx, s.shortPos)
	}

	// Manage positions on primary bar too
	if s.longPos != nil { s.managePos(ctx, bar, s.longPos, &s.longPos) }
	if s.shortPos != nil { s.managePos(ctx, bar, s.shortPos, &s.shortPos) }

	// Track if we have pending orders (for post-GPT cancel logic)
	hasPendingLong := s.longPos != nil && !s.longPos.filled
	hasPendingShort := s.shortPos != nil && !s.shortPos.filled

	// Don't open new positions on the same bar as a stop-loss
	if s.stopBar == s.barCount { return }

	// Force immediate GPT check if a position was closed externally (SL hit, manual close).
	// This allows the strategy to react quickly to direction changes.
	forceCheck := false
	if s.syncer != nil && s.syncer.PositionClosedExternally.CompareAndSwap(true, false) {
		forceCheck = true
		s.log.Info("AI: position closed externally — forcing immediate signal check")
	}

	// GPT signal check (every N primary bars, or immediately if forced)
	interval := s.cfg.CallIntervalBars
	if interval < 1 { interval = 1 }
	if !forceCheck && s.barCount-s.lastCallBar < interval { return }

	atr := s.calcATR()
	if price > 0 && atr/price > 0.05 {
		s.lastCallBar = s.barCount
		return
	}
	if s.consecLoss >= s.cfg.MaxConsecLoss {
		s.log.Warn("AI: halted — consecutive losses", zap.Int("consec", s.consecLoss))
		s.lastCallBar = s.barCount
		return
	}

	mktCtx := s.buildContext(ctx, bar)
	signal, err := s.callGPT(mktCtx)
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
	s.lastCallBar = s.barCount
	s.totalCall++
	s.cacheSignal(bar, signal)

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

	// Summary line for quick scanning
	action := "HOLD"
	if longConf >= s.cfg.ConfidenceThreshold && shortConf >= s.cfg.ConfidenceThreshold {
		action = "BOTH"
	} else if longConf >= s.cfg.ConfidenceThreshold {
		action = "BUY"
	} else if shortConf >= s.cfg.ConfidenceThreshold {
		action = "SELL"
	}
	s.log.Info("AI signal → "+action,
		zap.Float64("price", price),
		zap.Float64("L", longConf), zap.Float64("L_entry", longEntry),
		zap.Float64("S", shortConf), zap.Float64("S_entry", shortEntry),
		zap.Int("call", s.totalCall),
	)
	if longConf >= s.cfg.ConfidenceThreshold { s.log.Info("  BUY reason: "+longReason) }
	if shortConf >= s.cfg.ConfidenceThreshold { s.log.Info("  SELL reason: "+shortReason) }
	s.logEvent("signal", action, "", price, 0, 0, math.Max(longConf, shortConf), 0,
		fmt.Sprintf(`{"L":%.2f,"S":%.2f,"L_entry":%.2f,"S_entry":%.2f}`, longConf, shortConf, longEntry, shortEntry))

	isRange := s.isRangeRegime(price)

	// Minimum spread between long and short to avoid self-hedging
	// Only open opposite direction if entries are at least 0.5% apart
	minSpread := price * 0.0035 // ~$7 at ETH $2000

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
	// Without ATR confirmation, a high MTF score in wide-range oscillation would wrongly
	// force Trend mode (wide SL, unreachable TP).
	if isRange && (mtfScore >= 2 || mtfScore <= -2) {
		atrNow := s.calcATR()
		bars5m := s.primaryBars()
		atrExpanding := true // default to allow if not enough data
		if len(bars5m) >= 20 && atrNow > 0 {
			// Compare current ATR to 20-bar ATR mean
			var atrSum float64
			for i := len(bars5m) - 20; i < len(bars5m); i++ {
				atrSum += bars5m[i].High - bars5m[i].Low
			}
			atrMean := atrSum / 20
			atrExpanding = atrNow > atrMean*1.2 // ATR must be 20% above average
		}
		if atrExpanding {
			isRange = false
		}
	}

	// Apply score: only block on extreme disagreement (±3/±4).
	// Otherwise adjust position size via mtfQtyScale.
	longQtyScale, shortQtyScale := 1.0, 1.0

	// For LONG: negative score = headwind
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

	// For SHORT: positive score = headwind
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

	// ── Rule-based boost (after MTF scoring, respects MTF direction) ──
	swLow := s.findSwingLow(10)
	swHigh := s.findSwingHigh(10)
	if price > 0 && swLow > 0 && (price-swLow)/price < s.cfg.SwingProximity && longConf >= 0.60 && longConf < s.cfg.ConfidenceThreshold && s.longPos == nil && mtfScore >= -1 {
		s.log.Info("AI: boost long — price near swing low",
			zap.Float64("price", price), zap.Float64("swing_low", swLow), zap.Int("mtf", mtfScore))
		longConf = s.cfg.ConfidenceThreshold
		if longEntry <= 0 { longEntry = swLow }
	}
	if price > 0 && swHigh > 0 && (swHigh-price)/price < s.cfg.SwingProximity && shortConf >= 0.60 && shortConf < s.cfg.ConfidenceThreshold && s.shortPos == nil && mtfScore <= 1 {
		s.log.Info("AI: boost short — price near swing high",
			zap.Float64("price", price), zap.Float64("swing_high", swHigh), zap.Int("mtf", mtfScore))
		shortConf = s.cfg.ConfidenceThreshold
		if shortEntry <= 0 { shortEntry = swHigh }
	}

	// ── MTF momentum boost: when MTF strongly agrees with GPT direction but conf just under threshold ──
	// This handles the "early reversal" case where EMA hasn't crossed yet but price action is shifting.
	if mtfScore >= 2 && longConf > 0 && longConf >= 0.60 && longConf < s.cfg.ConfidenceThreshold && s.longPos == nil {
		s.log.Info("AI: MTF momentum boost → LONG",
			zap.Float64("conf_before", longConf), zap.Int("mtf", mtfScore))
		longConf = s.cfg.ConfidenceThreshold
		if longEntry <= 0 { longEntry = price - price*s.cfg.EntryOffsetPct }
	}
	if mtfScore <= -2 && shortConf > 0 && shortConf >= 0.60 && shortConf < s.cfg.ConfidenceThreshold && s.shortPos == nil {
		s.log.Info("AI: MTF momentum boost → SHORT",
			zap.Float64("conf_before", shortConf), zap.Int("mtf", mtfScore))
		shortConf = s.cfg.ConfidenceThreshold
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
		if longConf >= s.cfg.ConfidenceThreshold && shortConf >= s.cfg.ConfidenceThreshold {
			if longConf >= shortConf {
				shortConf = 0
			} else {
				longConf = 0
			}
		}
		if s.longPos != nil && shortConf >= s.cfg.ConfidenceThreshold {
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
		if s.shortPos != nil && longConf >= s.cfg.ConfidenceThreshold {
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
	if s.longPos != nil && !s.longPos.filled && longConf >= s.cfg.ConfidenceThreshold {
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
	if s.shortPos != nil && !s.shortPos.filled && shortConf >= s.cfg.ConfidenceThreshold {
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
	if longConf >= s.cfg.ConfidenceThreshold && s.longPos == nil {
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
			// High confidence + GPT entry is ABOVE current price (missed the dip) → use market
			if longConf >= s.cfg.MarketEntryConf && entry >= price {
				entry = price
				s.log.Info("AI: high confidence + no better entry → market", zap.String("side", "LONG"), zap.Float64("conf", longConf))
			}
			// Strong MTF → cap entry distance to 0.2% from current price (don't wait for distant limit)
			if mtfScore >= 3 {
				maxEntry := price - price*0.002 // at most 0.2% below
				if entry < maxEntry {
					entry = maxEntry
					s.log.Info("AI: strong MTF → capped entry", zap.String("side", "LONG"), zap.Float64("entry", entry), zap.Int("mtf", mtfScore))
				}
			}
			entry = math.Round(entry*100) / 100
			if hedgeAllowed && s.shortPos != nil {
				// Hedge mode: force Range + reduced qty + dynamic TP
				s.openHedgeScalp(ctx, "LONG", price, entry, atr, s.shortPos)
			} else if isRange {
				// Range LONG requires MTF not bearish (≥0). Scalping against MTF is negative EV.
				if mtfScore < 0 {
					s.log.Info("AI: Range LONG skipped — MTF bearish", zap.Int("mtf", mtfScore))
				} else {
					s.openRange(ctx, "LONG", price, entry, atr)
				}
			} else {
				s.openTrend(ctx, "LONG", price, entry, atr)
			}
			// GPT entry as grid add-on if significantly better than actual entry
			if !hedgeAllowed && s.longPos != nil && s.longPos.filled && longEntry > 0 && longEntry < entry && (entry-longEntry)/entry > 0.002 {
				s.addGPTGrid(s.longPos, "LONG", longEntry)
			}
		}
	}

	// ── Open SHORT if confident ──
	if shortConf >= s.cfg.ConfidenceThreshold && s.cfg.EnableShort && s.shortPos == nil {
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
			// High confidence + GPT entry is BELOW current price (missed the rally) → use market
			if shortConf >= s.cfg.MarketEntryConf && entry <= price {
				entry = price
				s.log.Info("AI: high confidence + no better entry → market", zap.String("side", "SHORT"), zap.Float64("conf", shortConf))
			}
			// Strong MTF → cap entry distance to 0.2% from current price (don't wait for distant limit)
			if mtfScore <= -3 {
				minEntry := price + price*0.002 // at most 0.2% above
				if entry > minEntry {
					entry = minEntry
					s.log.Info("AI: strong MTF → capped entry", zap.String("side", "SHORT"), zap.Float64("entry", entry), zap.Int("mtf", mtfScore))
				}
			}
			entry = math.Round(entry*100) / 100
			if hedgeAllowed && s.longPos != nil {
				s.openHedgeScalp(ctx, "SHORT", price, entry, atr, s.longPos)
			} else if isRange {
				// Range SHORT requires MTF not bullish (≤0). Scalping against MTF is negative EV.
				if mtfScore > 0 {
					s.log.Info("AI: Range SHORT skipped — MTF bullish", zap.Int("mtf", mtfScore))
				} else {
					s.openRange(ctx, "SHORT", price, entry, atr)
				}
			} else {
				s.openTrend(ctx, "SHORT", price, entry, atr)
			}
			// GPT entry as grid add-on if significantly better than actual entry
			if !hedgeAllowed && s.shortPos != nil && s.shortPos.filled && shortEntry > 0 && shortEntry > entry && (shortEntry-entry)/entry > 0.002 {
				s.addGPTGrid(s.shortPos, "SHORT", shortEntry)
			}
		}
	}
}
