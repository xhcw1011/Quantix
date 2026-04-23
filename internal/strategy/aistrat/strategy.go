// Package aistrat implements a rule-based trading strategy with regime detection.
//
// Regime detection (STRONG_TREND, SLOW_TREND, EXPANSION, RANGE) determines
// entry thresholds and mode. Pure technical signals (breakout + mean reversion)
// drive entries. ATR-adaptive TP + R-based trailing stop manage exits.
package aistrat

import (
	"fmt"
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/redis/go-redis/v9"
)

// ─── Strategy ────────────────────────────────────────────────────────────────

type AIStrategy struct {
	cfg Config
	log *zap.Logger

	barsByInterval map[string][]exchange.Kline // key = interval ("1m","5m","15m")
	warmedUp       bool
	liveReady      bool // true after first real-time primary bar (skip backfill processing)
	barCount       int  // primary interval bar count
	lastCallBar    int
	totalCall      int

	longPos  *posState
	shortPos *posState
	syncer    *position.Syncer          // Redis-backed, set at warmup from ctx.Extra
	stagedEP  strategy.StagedExitPlacer // cached from ctx.Extra on first use
	rdb       *redis.Client             // for position state persistence
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
	// Regime hysteresis: transitions need 2 consecutive bars of the same new
	// regime before committing. Prevents single-bar flicker from thrashing
	// downstream (GRID regime-flip exit, breakout shortcut).
	confirmedRegime Regime // hystereized regime reported to downstream
	pendingRegime   Regime // regime being evaluated for transition
	pendingCount    int    // consecutive bars of pending regime
	lastSLReplace   time.Time // throttle ReplaceSLOrder calls (max 1 per 3s)
	// Signal accumulation: tracks rolling confidence across bars
	accumLong  float64 // accumulated long signal strength (decays each bar)
	accumShort float64 // accumulated short signal strength (decays each bar)

	lastCloseFailAt time.Time // throttle close retries after failure (prevent tick-level spam)

	// Post-SL immediate reversal: set by tickManage when SL fires on tick data.
	postSLReeval  bool
	postSLSide    string  // which side was closed ("LONG"/"SHORT")
	postSLPrice   float64 // tick price at SL trigger
	tpCooldownBar int     // bar index when TP filled — block new entries for 3 bars
}

func New(cfg Config, log *zap.Logger) *AIStrategy {
	if cfg.PrimaryInterval == "" {
		cfg.PrimaryInterval = "5m"
	}
	return &AIStrategy{
		cfg:            cfg,
		log:            log,
		barsByInterval: make(map[string][]exchange.Kline),
	}
}

func (s *AIStrategy) Name() string {
	return fmt.Sprintf("AI(every%dbars)", s.cfg.CallIntervalBars)
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
	s.log.Info("SIG: fill confirmed",
		zap.String("side", pos.side), zap.Float64("fill", fill.Price),
		zap.Float64("stop", pos.stopLoss), zap.Float64("tp", pos.takeProfit))

	s.syncToRedis(pos)

	if pos.mode == modeTrend && !pos.stagedTPPlaced {
		s.placeStagedExitOrders(ctx, pos)
	}
	// Grid/range: place a single reduce-only limit order at TP price (maker fee).
	// Sits on the order book — fills as maker (0.02%) instead of reactive market (0.05%).
	if pos.mode == modeRange && pos.takeProfit > 0 && !pos.stagedTPPlaced {
		s.placeGridTP(ctx, pos)
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

// checkGridProtections evaluates fixed-distance SL and TP for a grid position
// at the given price. Returns true if the position was closed so the caller
// skips further management.
//
// Shared by OnTick (tickManage) and OnBar (manageRange): live broker delivers
// ticks so OnTick hits first, but backtests only see bar closes — without the
// OnBar call these guards are skipped entirely in backtest. src is just a log
// tag ("TICK" or "BAR").
//
// consecLoss is intentionally NOT incremented on fixed_sl: the grid risk cap
// is position-scoped, not a strategy-failure signal. Letting it count toward
// consecLoss would halt trend entries after 3 grid SLs, conflating grid risk
// management with trend-signal disruption (separate evaluation paths).
func (s *AIStrategy) checkGridProtections(ctx *strategy.Context, price float64, p *posState, pptr **posState, src string) bool {
	if s.cfg.GridFixedSLDist > 0 {
		var advDist float64
		if p.side == "LONG" { advDist = p.entryPrice - price }
		if p.side == "SHORT" { advDist = price - p.entryPrice }
		if advDist > s.cfg.GridFixedSLDist {
			s.log.Warn(src+" GRID fixed-distance SL hit",
				zap.String("side", p.side),
				zap.Float64("entry", p.entryPrice),
				zap.Float64("price", price),
				zap.Float64("dist", advDist),
				zap.Float64("threshold", s.cfg.GridFixedSLDist),
				zap.Int("layers", len(p.gridOrders)))
			s.closePos(ctx, p, pptr, "fixed_sl", price)
			return true
		}
	}
	if p.takeProfit > 0 {
		if (p.side == "LONG" && price >= p.takeProfit) || (p.side == "SHORT" && price <= p.takeProfit) {
			s.log.Info(src+" GRID TP", zap.String("side", p.side),
				zap.Float64("price", price), zap.Float64("tp", p.takeProfit))
			s.closePos(ctx, p, pptr, "grid_tp", price)
			s.consecLoss = 0
			return true
		}
	}
	return false
}

func (s *AIStrategy) tickManage(ctx *strategy.Context, price float64, p *posState, pptr **posState) {
	// Throttle after close failure: don't retry for 10s to prevent tick-level spam.
	if !s.lastCloseFailAt.IsZero() && time.Since(s.lastCloseFailAt) < 10*time.Second {
		return
	}

	// ── 0. Skip tick-level management during minimum hold period ──
	if p.barsHeld < s.cfg.MinHoldBars { return }

	// ── Range/grid mode: fixed-distance SL + TP check on tick ──
	if p.mode == modeRange {
		if s.checkGridProtections(ctx, price, p, pptr, "TICK") { return }
		return
	}

	// Cancel safety-net exchange SL once local trailing is active.
	// Only after MinHoldBars so the position is always protected.
	if p.safetyNetSL && s.stagedEP != nil {
		posSide := "LONG"
		if p.side == "SHORT" { posSide = "SHORT" }
		s.stagedEP.CancelExchangeSL(s.cfg.Symbol, posSide)
		p.safetyNetSL = false
		s.log.Info("SIG: safety-net SL cancelled — local trailing active",
			zap.String("side", p.side), zap.Float64("trailing", p.trailing))
	}

	// ── 1. Real-time SL check (must be instant, not wait for bar close) ──
	if (p.side == "LONG" && price <= p.stopLoss) || (p.side == "SHORT" && price >= p.stopLoss) {
		closedSide := p.side
		s.log.Warn("TICK STOP-LOSS", zap.String("side", closedSide),
			zap.Float64("price", price), zap.Float64("stop", p.stopLoss))
		s.closePos(ctx, p, pptr, "stop_loss", price)
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
					s.log.Info("SIG: trail tier upgrade — widening trailing for trend ride",
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
				s.closePos(ctx, p, pptr, "stale_peak_exit", price)
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
			s.closePos(ctx, p, pptr, "time_exit", price)
			if pnlR > 0 { s.consecLoss = 0 } else { s.consecLoss++ }
			return
		}

		// ── 4. Real-time bounce TP (remaining position) ──
		if p.remainQty < p.initQty && p.remainQty > 0 && pnlR > 0 {
			bounceThreshold := s.cfg.BounceTPR * p.R
			if p.side == "LONG" && p.peakPrice-price >= bounceThreshold {
				s.log.Info("TICK: bounce TP", zap.String("side", p.side),
					zap.Float64("peak", p.peakPrice), zap.Float64("price", price))
				s.closePos(ctx, p, pptr, "bounce_tp", price)
				s.consecLoss = 0
				return
			}
			if p.side == "SHORT" && price-p.peakPrice >= bounceThreshold {
				s.log.Info("TICK: bounce TP", zap.String("side", p.side),
					zap.Float64("peak", p.peakPrice), zap.Float64("price", price))
				s.closePos(ctx, p, pptr, "bounce_tp", price)
				s.consecLoss = 0
				return
			}
		}

		// ── 4b. Emergency reversal: if losing > 0.9R, trigger async GPT check ──
		// Raised from -0.8R to -0.9R, cooldown 30s→60s to avoid premature cuts.
		if pnlR < s.cfg.EmergencyPnlR && hMode == hourlyExitMode {
			s.log.Warn("TICK: emergency exit — losing >0.9R + 1h exit mode",
				zap.String("side", p.side), zap.Float64("pnlR", pnlR))
			s.closePos(ctx, p, pptr, "emergency_exit", price)
			s.consecLoss++
			return
		}
	}

	// ── 5. Real-time trailing trigger ──
	if p.side == "LONG" && p.trailing > p.stopLoss && price <= p.trailing {
		s.log.Warn("TICK TRAILING", zap.String("side", p.side),
			zap.Float64("price", price), zap.Float64("trail", p.trailing))
		s.closePos(ctx, p, pptr, "trailing", price)
		s.consecLoss = 0
		return
	}
	if p.side == "SHORT" && p.trailing > 0 && p.trailing < p.stopLoss && price >= p.trailing {
		s.log.Warn("TICK TRAILING", zap.String("side", p.side),
			zap.Float64("price", price), zap.Float64("trail", p.trailing))
		s.closePos(ctx, p, pptr, "trailing", price)
		s.consecLoss = 0
		return
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

	s.log.Info("SIG: staged TP fill",
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
		s.log.Info("SIG: position fully closed by exchange order",
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
			s.log.Info("SIG: TP fill → trailing to breakeven",
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
		s.log.Info("SIG: staged TP level filled",
			zap.Int("level", pos.stagedTPs[bestIdx].Level),
			zap.Float64("expected_price", pos.stagedTPs[bestIdx].Price),
			zap.Float64("fill_price", fillPrice))
	}
	s.saveStagedTPsToRedis(pos)
}

