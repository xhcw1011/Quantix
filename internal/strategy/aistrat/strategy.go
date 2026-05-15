// Package aistrat implements an AI-driven trading strategy with regime detection.
//
// Regime detection (STRONG_TREND, SLOW_TREND, EXPANSION, RANGE) determines
// entry thresholds and mode. GPT provides directional signals with confidence.
// ATR-adaptive TP + R-based trailing stop manage exits.
package aistrat

import (
	"fmt"
	"math"
	"net/http"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/redis/go-redis/v9"
)

// emergencySignal carries the async result of an emergency GPT call.
type emergencySignal struct {
	side   string
	price  float64
	signal gptSignal
}

// ─── Strategy ────────────────────────────────────────────────────────────────

type AIStrategy struct {
	cfg    Config
	log    *zap.Logger
	client *http.Client

	barsByInterval map[string][]exchange.Kline // key = interval ("1m","5m","15m")
	warmedUp       bool
	liveReady      bool // true after first real-time primary bar (skip backfill GPT calls)
	barCount       int  // primary interval bar count
	lastCallBar    int
	totalCall      int

	longPos  *posState
	shortPos *posState
	syncer    *position.Syncer          // Redis-backed, set at warmup from ctx.Extra
	stagedEP  strategy.StagedExitPlacer // cached from ctx.Extra on first use
	rdb       *redis.Client             // for signal caching
	store     *data.Store               // for trade event logging
	userID   int
	engineID string

	dayStart       time.Time
	dayStartEquity float64
	consecLoss     int
	dayHalted      bool
	stopBar        int // bar index when last stop-loss fired — skip opening same bar
	expansionBar   int // bar index when last EXPANSION detected — cooldown before entry
	lastMTFScore    int     // multi-timeframe score from latest signal check
	mtfLongScale    float64 // position size multiplier for LONG (0.7-1.0)
	mtfShortScale   float64 // position size multiplier for SHORT (0.7-1.0)
	lastHedgeClose  time.Time // when the last hedge position was closed (for cooldown)
	lastConf        float64   // confidence of the signal that triggered current entry
	lastRegime      Regime    // current detected regime (updated every signal check)
	lastHourlyDir   int       // 1h EMA trend direction: +1 bullish, -1 bearish, 0 neutral
	lastBBMiddle    float64   // BB middle band from last reversion signal (for TP)
	lastBBLower     float64   // BB lower band (for grid SL)
	lastBBUpper     float64   // BB upper band (for grid SL)
	cachedHourlyLong  hourlyMode // cached 1h mode for LONG positions
	cachedHourlyShort hourlyMode // cached 1h mode for SHORT positions
	hourlyModeBars    int        // 15m bar count when cache was last updated
	lastTrendDir    int       // +1 = bullish, -1 = bearish, 0 = neutral (from detectRegime)
	lastSLReplace   time.Time // throttle ReplaceSLOrder calls (max 1 per 3s)
	// Signal accumulation: tracks rolling GPT confidence across bars
	accumLong       float64   // accumulated long signal strength (decays each bar)
	accumShort      float64   // accumulated short signal strength (decays each bar)
	replaySignals   []gptSignal // cached signals for backtest replay
	replayIdx       int         // current index into replaySignals

	lastCloseFailAt time.Time // throttle close retries after failure (prevent tick-level spam)

	// Post-SL immediate reversal: set by tickManage when SL fires on tick data.
	// Cleared on next OnBar when the urgent GPT check runs.
	postSLReeval    bool
	postSLSide      string    // which side was closed ("LONG"/"SHORT")
	postSLPrice     float64   // tick price at SL trigger
	lastEmergencyAt time.Time // throttle emergency GPT calls (max 1 per 30s)
	tpCooldownBar   int       // bar index when TP filled — block new entries for 3 bars

	// Async emergency GPT: the goroutine sends results here, OnTick consumes.
	emergencyCh     chan emergencySignal
	emergencyActive atomic.Bool
}

func New(cfg Config, log *zap.Logger) *AIStrategy {
	if cfg.PrimaryInterval == "" {
		cfg.PrimaryInterval = "5m"
	}
	return &AIStrategy{
		cfg:            cfg,
		log:            log,
		client:         &http.Client{Timeout: cfg.GPTTimeout},
		barsByInterval: make(map[string][]exchange.Kline),
		emergencyCh:    make(chan emergencySignal, 1),
	}
}

func (s *AIStrategy) Name() string {
	return fmt.Sprintf("AI(%s/every%dbars)", s.cfg.Model, s.cfg.CallIntervalBars)
}

func (s *AIStrategy) OnFill(ctx *strategy.Context, fill strategy.Fill) {
	// Detect staged TP closing fills: opposite side to the position.
	// LONG position closes via SELL; SHORT position closes via BUY.
	if s.handleStagedTPFill(fill) {
		return
	}

	// Match fill to the correct position (opening fill)
	pos := s.longPos
	if fill.Side == strategy.SideSell && fill.PositionSide == strategy.PositionSideShort {
		pos = s.shortPos // opening short
	}
	if fill.Side == strategy.SideBuy && fill.PositionSide == strategy.PositionSideLong {
		pos = s.longPos // opening long
	}
	if pos == nil || pos.filled { return }

	pos.filled = true
	pos.filledAt = time.Now()
	pos.lastPeakAt = time.Now()
	if fill.Price > 0 {
		diff := fill.Price - pos.entryPrice
		pos.entryPrice = fill.Price
		pos.peakPrice = fill.Price
		pos.stopLoss = math.Round((pos.stopLoss+diff)*100) / 100
		pos.trailing = pos.stopLoss
		pos.R = math.Abs(fill.Price - pos.stopLoss)
	}
	s.log.Info("AI: fill confirmed",
		zap.String("side", pos.side), zap.Float64("fill", fill.Price),
		zap.Float64("stop", pos.stopLoss), zap.Float64("tp", pos.takeProfit))

	s.syncToRedis(pos)

	// Staged TP only for trend positions. Grid/range uses local TP check.
	if pos.mode == modeTrend && !pos.stagedTPPlaced {
		s.placeStagedExitOrders(ctx, pos)
	}
}

// OnTick receives real-time price for precise TP/SL management.
// Implements strategy.TickReceiver.
func (s *AIStrategy) OnTick(ctx *strategy.Context, price float64) {
	if !s.warmedUp { return }
	if s.longPos != nil && s.longPos.filled {
		s.tickManage(ctx, price, s.longPos, &s.longPos)
	}
	if s.shortPos != nil && s.shortPos.filled {
		s.tickManage(ctx, price, s.shortPos, &s.shortPos)
	}
}

// throttledReplaceSL calls ReplaceSLOrder at most once per 3 seconds to avoid API rate limits.
func (s *AIStrategy) throttledReplaceSL(symbol, posSide, closeSide string, qty, stopPrice float64) {
	if s.stagedEP == nil { return }
	if time.Since(s.lastSLReplace) < 3*time.Second { return }
	s.stagedEP.ReplaceSLOrder(symbol, posSide, closeSide, qty, stopPrice)
	s.lastSLReplace = time.Now()
}

func (s *AIStrategy) tickManage(ctx *strategy.Context, price float64, p *posState, pptr **posState) {
	// Throttle after close failure: don't retry for 10s to prevent tick-level spam.
	if !s.lastCloseFailAt.IsZero() && time.Since(s.lastCloseFailAt) < 10*time.Second {
		return
	}

	// ── 0. Skip tick-level management during minimum hold period ──
	if p.barsHeld < s.cfg.MinHoldBars { return }

	// ── Range/grid mode: only check TP on tick (no SL, no trailing) ──
	if p.mode == modeRange {
		if p.takeProfit > 0 {
			if (p.side == "LONG" && price >= p.takeProfit) || (p.side == "SHORT" && price <= p.takeProfit) {
				s.log.Info("TICK GRID TP", zap.String("side", p.side),
					zap.Float64("price", price), zap.Float64("tp", p.takeProfit))
				s.closePos(ctx, p, pptr, "grid_tp")
				s.consecLoss = 0
			}
		}
		return
	}

	// Cancel safety-net exchange SL once local trailing is active.
	// Only after MinHoldBars so the position is always protected.
	if p.safetyNetSL && s.stagedEP != nil {
		posSide := "LONG"
		if p.side == "SHORT" { posSide = "SHORT" }
		s.stagedEP.CancelExchangeSL(s.cfg.Symbol, posSide)
		p.safetyNetSL = false
		s.log.Info("AI: safety-net SL cancelled — local trailing active",
			zap.String("side", p.side), zap.Float64("trailing", p.trailing))
	}

	// ── 1. Real-time SL check (must be instant, not wait for bar close) ──
	if (p.side == "LONG" && price <= p.stopLoss) || (p.side == "SHORT" && price >= p.stopLoss) {
		closedSide := p.side
		s.log.Warn("TICK STOP-LOSS", zap.String("side", closedSide),
			zap.Float64("price", price), zap.Float64("stop", p.stopLoss))
		s.closePos(ctx, p, pptr, "stop_loss")
		s.consecLoss++
		s.stopBar = s.barCount
		// Flag for immediate reversal evaluation on next OnBar.
		s.postSLReeval = true
		s.postSLSide = closedSide
		s.postSLPrice = price
		return
	}

	// ── 2. Real-time peak update ──
	if p.side == "LONG" && price > p.peakPrice { p.peakPrice = price; p.lastPeakAt = time.Now() }
	if p.side == "SHORT" && price < p.peakPrice { p.peakPrice = price; p.lastPeakAt = time.Now() }

	// ── 3. Real-time trailing calculation + check ──
	if p.R > 0 {
		pnlR := 0.0
		if p.side == "LONG" { pnlR = (price - p.entryPrice) / p.R }
		if p.side == "SHORT" { pnlR = (p.entryPrice - price) / p.R }

		// ── 1h mode-based trailing ──
		liveATR := s.calcATR()
		atr := math.Max(p.entryATR, liveATR)
		hMode := s.detectHourlyMode(p.side)

		switch hMode {
		case hourlyTrendStrong:
			// Two-tier trailing: tight when profit small, wide when profit large.
			// profit < 1ATR → no trailing (only hard SL).
			// profit 1ATR ~ 2R → tight trail (1ATR), protect profit.
			// profit ≥ 2R → wide trail (3ATR), ride the trend.
			// On tier upgrade (1→2): reset trailing to wide value so ratchet doesn't block it.
			if pnlR > 0 && math.Abs(price-p.entryPrice) < atr {
				// Let the trade breathe — skip trailing update
			} else {
				tier := 1
				trailDist := atr // tight: 1ATR
				if pnlR >= 2.0 {
					tier = 2
					trailDist = atr * s.cfg.TrailingATRK // wide: 3ATR
				}
				// Use SL as floor initially; only upgrade to breakeven after 0.3R profit.
				// Prevents immediate breakeven stop when position hasn't moved in favor yet.
				floor := p.stopLoss
				if pnlR >= 0.3 {
					floor = p.entryPrice // breakeven floor
				}
				var newTrail float64
				if p.side == "LONG" {
					newTrail = p.peakPrice - trailDist
					if newTrail < floor { newTrail = floor }
				} else {
					newTrail = p.peakPrice + trailDist
					if newTrail > floor { newTrail = floor }
				}
				newTrail = math.Round(newTrail*100) / 100
				// Tier upgrade: allow trailing to widen (reset ratchet for this transition).
				if tier > p.trailTier && p.trailTier > 0 {
					p.trailing = newTrail
					s.log.Info("AI: trail tier upgrade — widening trailing for trend ride",
						zap.String("side", p.side), zap.Float64("trailing", newTrail), zap.Float64("pnlR", pnlR))
				}
				p.trailTier = tier
				// Normal ratchet: only tighten within the same tier.
				if p.side == "LONG" && newTrail > p.trailing { p.trailing = newTrail }
				if p.side == "SHORT" && (p.trailing == 0 || newTrail < p.trailing) { p.trailing = newTrail }
			}

		case hourlyExitMode:
			// 1h opposes: tighter trail + aggressive profit locking.
			// ATR*1.0 (not 0.5) to avoid jump-trigger when switching from STRONG (trail=SL).
			// Stale peak exit: no new high/low for 45 min → close immediately.
			if !p.lastPeakAt.IsZero() && time.Since(p.lastPeakAt) > 45*time.Minute {
				s.log.Warn("TICK: stale peak exit — 45m no new high/low in EXIT_MODE",
					zap.String("side", p.side), zap.Float64("pnlR", pnlR))
				s.closePos(ctx, p, pptr, "stale_peak_exit")
				if pnlR > 0 { s.consecLoss = 0 } else { s.consecLoss++ }
				return
			}
			trailDist := atr * 1.0
			floor := p.stopLoss
			if pnlR >= 0.3 {
				lockR := math.Max(pnlR*0.5, 0.02)
				if p.side == "LONG" { floor = p.entryPrice + lockR*p.R }
				if p.side == "SHORT" { floor = p.entryPrice - lockR*p.R }
			}
			var newTrail float64
			if p.side == "LONG" {
				newTrail = p.peakPrice - trailDist
				if newTrail < floor { newTrail = floor }
			} else {
				newTrail = p.peakPrice + trailDist
				if newTrail > floor { newTrail = floor }
			}
			newTrail = math.Round(newTrail*100) / 100
			if p.side == "LONG" && newTrail > p.trailing { p.trailing = newTrail }
			if p.side == "SHORT" && (p.trailing == 0 || newTrail < p.trailing) { p.trailing = newTrail }

		default: // hourlyTrendWeak — medium trailing, profit < 1 ATR → skip trailing
			if pnlR > 0 && math.Abs(price-p.entryPrice) < atr {
				break // let the trade develop, only SL protects
			}
			trailDist := atr * s.cfg.TrailingATRK * 0.5
			floor := p.stopLoss
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
			var newTrail float64
			if p.side == "LONG" {
				newTrail = p.peakPrice - trailDist
				if newTrail < floor { newTrail = floor }
			} else {
				newTrail = p.peakPrice + trailDist
				if newTrail > floor { newTrail = floor }
			}
			newTrail = math.Round(newTrail*100) / 100
			if p.side == "LONG" && newTrail > p.trailing { p.trailing = newTrail }
			if p.side == "SHORT" && (p.trailing == 0 || newTrail < p.trailing) { p.trailing = newTrail }
		}

		// ── Time-based exit: close if held > 3h and NOT in strong trend ──
		if p.filled && hMode != hourlyTrendStrong && time.Since(p.filledAt) > 3*time.Hour {
			s.log.Warn("TICK: time exit — held >3h in weak/exit mode",
				zap.String("side", p.side), zap.Float64("pnlR", pnlR),
				zap.Duration("held", time.Since(p.filledAt)))
			s.closePos(ctx, p, pptr, "time_exit")
			if pnlR > 0 { s.consecLoss = 0 } else { s.consecLoss++ }
			return
		}

		// ── 4. Real-time bounce TP (remaining position) ──
		if p.remainQty < p.initQty && p.remainQty > 0 && pnlR > 0 {
			bounceThreshold := s.cfg.BounceTPR * p.R
			if p.side == "LONG" && p.peakPrice-price >= bounceThreshold {
				s.log.Info("TICK: bounce TP", zap.String("side", p.side),
					zap.Float64("peak", p.peakPrice), zap.Float64("price", price))
				s.closePos(ctx, p, pptr, "bounce_tp")
				s.consecLoss = 0
				return
			}
			if p.side == "SHORT" && price-p.peakPrice >= bounceThreshold {
				s.log.Info("TICK: bounce TP", zap.String("side", p.side),
					zap.Float64("peak", p.peakPrice), zap.Float64("price", price))
				s.closePos(ctx, p, pptr, "bounce_tp")
				s.consecLoss = 0
				return
			}
		}

		// ── 4b. Emergency reversal: if losing > 0.9R, trigger async GPT check ──
		// Raised from -0.8R to -0.9R, cooldown 30s→60s to avoid premature cuts.
		if pnlR < s.cfg.EmergencyPnlR && hMode == hourlyExitMode {
			s.log.Warn("TICK: emergency exit — losing >0.9R + 1h exit mode",
				zap.String("side", p.side), zap.Float64("pnlR", pnlR))
			s.closePos(ctx, p, pptr, "emergency_exit")
			s.consecLoss++
			return
		}
	}

	// ── 5. Real-time trailing trigger ──
	if p.side == "LONG" && p.trailing > p.stopLoss && price <= p.trailing {
		s.log.Warn("TICK TRAILING", zap.String("side", p.side),
			zap.Float64("price", price), zap.Float64("trail", p.trailing))
		s.closePos(ctx, p, pptr, "trailing")
		s.consecLoss = 0
		return
	}
	if p.side == "SHORT" && p.trailing > 0 && p.trailing < p.stopLoss && price >= p.trailing {
		s.log.Warn("TICK TRAILING", zap.String("side", p.side),
			zap.Float64("price", price), zap.Float64("trail", p.trailing))
		s.closePos(ctx, p, pptr, "trailing")
		s.consecLoss = 0
		return
	}
}

// emergencyReversalCheck launches an async GPT call when unrealized loss exceeds 0.9R.
// The GPT call runs in a goroutine to avoid blocking tick processing.
// Results are consumed by processEmergencyResult on the next tick.
func (s *AIStrategy) emergencyReversalCheck(ctx *strategy.Context, price float64, p *posState) {
	if s.emergencyActive.Load() { return }
	s.lastEmergencyAt = time.Now()
	s.emergencyActive.Store(true)

	side := p.side
	bars := s.primaryBars()
	if len(bars) == 0 { s.emergencyActive.Store(false); return }

	// Build context in the main goroutine (reads strategy state safely).
	lastBar := bars[len(bars)-1]
	syntheticBar := lastBar
	syntheticBar.Close = price
	mktCtx := s.buildContext(ctx, syntheticBar)

	s.log.Info("TICK: launching async emergency GPT check",
		zap.String("side", side), zap.Float64("price", price))

	go func() {
		defer s.emergencyActive.Store(false)
		signal, err := s.callGPT(mktCtx)
		if err != nil {
			s.log.Warn("emergency GPT call failed", zap.Error(err))
			return
		}
		select {
		case s.emergencyCh <- emergencySignal{side: side, price: price, signal: signal}:
		default:
		}
	}()
}

// processEmergencyResult consumes async emergency GPT results and acts on them.
// Called from OnTick in the main goroutine — safe to modify strategy state.
func (s *AIStrategy) processEmergencyResult(ctx *strategy.Context, currentPrice float64) {
	select {
	case result := <-s.emergencyCh:
		var p *posState
		var pptr **posState
		if result.side == "LONG" && s.longPos != nil && s.longPos.filled {
			p = s.longPos; pptr = &s.longPos
		} else if result.side == "SHORT" && s.shortPos != nil && s.shortPos.filled {
			p = s.shortPos; pptr = &s.shortPos
		}
		if p == nil {
			s.log.Info("TICK: emergency result arrived but position already closed")
			return
		}

		signal := result.signal
		reverseConf := 0.0
		if p.side == "LONG" && signal.Short != nil { reverseConf = signal.Short.Confidence }
		if p.side == "SHORT" && signal.Long != nil { reverseConf = signal.Long.Confidence }
		if signal.Action != "" {
			if p.side == "LONG" && signal.Action == "SELL" { reverseConf = signal.Confidence }
			if p.side == "SHORT" && signal.Action == "BUY" { reverseConf = signal.Confidence }
		}

		s.log.Info("TICK: emergency result received",
			zap.String("side", p.side), zap.Float64("price", currentPrice),
			zap.Float64("reverse_conf", reverseConf))

		// Emergency threshold = ReversalConf (same bar, not easier).
		// Previously was ReversalConf-0.07 which made it too easy to trigger (0.53).
		emergencyThreshold := s.cfg.ReversalConf
		if emergencyThreshold < 0.65 { emergencyThreshold = 0.65 }

		if reverseConf >= emergencyThreshold {
			closedSide := p.side
			s.log.Warn("TICK: emergency reversal → close "+closedSide,
				zap.Float64("conf", reverseConf), zap.Float64("price", currentPrice))
			s.closePos(ctx, p, pptr, "emergency_reversal")
			// No flip — let next bar's normal flow decide new direction.
		}
	default:
	}
}

// handleStagedTPFill detects closing fills from staged TP orders and updates remainQty.
// Returns true if the fill was consumed (closing fill for a staged position).
func (s *AIStrategy) handleStagedTPFill(fill strategy.Fill) bool {
	// LONG closes via SELL on LONG side; SHORT closes via BUY on SHORT side.
	var pos *posState
	var pptr **posState

	if fill.Side == strategy.SideSell && fill.PositionSide == strategy.PositionSideLong && s.longPos != nil && s.longPos.filled && s.longPos.stagedTPPlaced {
		pos = s.longPos
		pptr = &s.longPos
	} else if fill.Side == strategy.SideBuy && fill.PositionSide == strategy.PositionSideShort && s.shortPos != nil && s.shortPos.filled && s.shortPos.stagedTPPlaced {
		pos = s.shortPos
		pptr = &s.shortPos
	}

	if pos == nil {
		return false
	}

	pos.remainQty -= fill.Qty
	if pos.remainQty < 1e-10 { pos.remainQty = 0 }

	pnl := 0.0
	if pos.side == "LONG" { pnl = (fill.Price - pos.entryPrice) * fill.Qty }
	if pos.side == "SHORT" { pnl = (pos.entryPrice - fill.Price) * fill.Qty }

	s.log.Info("AI: staged TP fill",
		zap.String("side", pos.side),
		zap.Float64("fill_price", fill.Price),
		zap.Float64("fill_qty", fill.Qty),
		zap.Float64("remain_qty", pos.remainQty),
		zap.Float64("est_pnl", pnl),
	)

	// Mark the filled TP level in records
	s.markTPFilled(pos, fill.Price, fill.Qty)

	// Position fully closed (SL fired or all TPs filled) — cancel remaining orders on exchange.
	if pos.remainQty <= 0 {
		s.log.Info("AI: position fully closed by exchange order",
			zap.String("side", pos.side))
		if s.stagedEP != nil {
			posSide := "LONG"
			if pos.side == "SHORT" { posSide = "SHORT" }
			s.stagedEP.CancelAllProtective(s.cfg.Symbol, posSide)
		}
		s.consecLoss = 0
		s.accumLong = 0
		s.accumShort = 0
		// TP cooldown: block new entries for 3 bars (15 min) to avoid
		// immediate re-entry in the same trend at worse price + extra fees.
		s.tpCooldownBar = s.barCount
		s.syncRemove(pos.side)
		*pptr = nil
	} else {
		// TP filled → move SL to breakeven immediately.
		// Remaining 50% position can't turn a profitable trade into a loss.
		if !pos.tp1RHit {
			pos.tp1RHit = true
			pos.trailing = pos.entryPrice
			s.log.Info("AI: TP fill → trailing to breakeven",
				zap.String("side", pos.side), zap.Float64("entry", pos.entryPrice))
		}
		// No exchange SL — local trailing handles the breakeven exit.
		s.syncToRedis(pos)
	}
	return true
}

// markTPFilled marks the closest matching TP level as filled (match by price proximity).
func (s *AIStrategy) markTPFilled(pos *posState, fillPrice, fillQty float64) {
	bestIdx := -1
	bestDist := math.MaxFloat64
	for i := range pos.stagedTPs {
		if pos.stagedTPs[i].Status != "pending" { continue }
		dist := math.Abs(pos.stagedTPs[i].Price - fillPrice)
		if dist < bestDist {
			bestDist = dist
			bestIdx = i
		}
	}
	if bestIdx >= 0 && bestDist < pos.entryPrice*0.005 { // within 0.5% of expected price
		pos.stagedTPs[bestIdx].Status = "filled"
		s.log.Info("AI: staged TP level filled",
			zap.Int("level", pos.stagedTPs[bestIdx].Level),
			zap.Float64("expected_price", pos.stagedTPs[bestIdx].Price),
			zap.Float64("fill_price", fillPrice))
	}
	s.saveStagedTPsToRedis(pos)
}

