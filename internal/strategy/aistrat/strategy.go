// Package aistrat implements an AI-driven dual-mode trading strategy.
//
// Trend Mode: R-based sizing, trailing stop, let profits run.
// Range Mode: fixed TP/SL scalping, quick in/out, supports simultaneous long+short.
//
// GPT decides direction (BUY/SELL/HOLD). Regime detection picks the mode.
// Hedge Mode: LONG and SHORT positions managed independently.
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
	lastMTFScore    int     // multi-timeframe score from latest signal check
	mtfLongScale    float64 // position size multiplier for LONG (0.7-1.0)
	mtfShortScale   float64 // position size multiplier for SHORT (0.7-1.0)
	lastHedgeClose  time.Time // when the last hedge position was closed (for cooldown)
	lastConf        float64   // confidence of the signal that triggered current entry
	lastRegime      Regime    // current detected regime (updated every signal check)
	lastTrendDir    int       // +1 = bullish, -1 = bearish, 0 = neutral (from detectRegime)
	lastSLReplace   time.Time // throttle ReplaceSLOrder calls (max 1 per 3s)
	// Signal accumulation: tracks rolling GPT confidence across bars
	accumLong       float64   // accumulated long signal strength (decays each bar)
	accumShort      float64   // accumulated short signal strength (decays each bar)
	replaySignals   []gptSignal // cached signals for backtest replay
	replayIdx       int         // current index into replaySignals

	// Post-SL immediate reversal: set by tickManage when SL fires on tick data.
	// Cleared on next OnBar when the urgent GPT check runs.
	postSLReeval    bool
	postSLSide      string    // which side was closed ("LONG"/"SHORT")
	postSLPrice     float64   // tick price at SL trigger
	lastEmergencyAt time.Time // throttle emergency GPT calls (max 1 per 30s)

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
	if fill.Price > 0 {
		// Adjust SL by the fill-vs-order price difference, then compute unified R.
		diff := fill.Price - pos.entryPrice
		pos.entryPrice = fill.Price
		pos.peakPrice = fill.Price
		pos.stopLoss = math.Round((pos.stopLoss+diff)*100) / 100
		pos.trailing = pos.stopLoss
		pos.R = math.Abs(fill.Price - pos.stopLoss) // unified R = |entry - SL|
	}
	s.log.Info("AI: fill confirmed",
		zap.String("side", pos.side), zap.Float64("fill", fill.Price),
		zap.Float64("stop", pos.stopLoss), zap.Float64("tp", pos.takeProfit))

	// Persist updated TP/SL to Redis so recovery uses correct values.
	s.syncToRedis(pos)

	// Place staged TP orders on exchange. Local trailing handles SL exit.
	if !pos.stagedTPPlaced {
		s.placeStagedExitOrders(ctx, pos)
	}
}

// OnTick receives real-time price for precise TP/SL management.
// Implements strategy.TickReceiver.
func (s *AIStrategy) OnTick(ctx *strategy.Context, price float64) {
	if !s.warmedUp { return }
	s.processEmergencyResult(ctx, price)
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
	if p.side == "LONG" && price > p.peakPrice { p.peakPrice = price }
	if p.side == "SHORT" && price < p.peakPrice { p.peakPrice = price }

	// ── 3. Real-time trailing calculation + check ──
	if p.R > 0 {
		pnlR := 0.0
		if p.side == "LONG" { pnlR = (price - p.entryPrice) / p.R }
		if p.side == "SHORT" { pnlR = (p.entryPrice - price) / p.R }

		// Phase 2: pnlR >= 2.0 → ATR trailing
		if pnlR >= 2.0 {
			atr := s.calcATR()
			trailDist := atr * s.cfg.TrailingATRK
			var newTrail float64
			if p.side == "LONG" {
				newTrail = p.peakPrice - trailDist
				if newTrail < p.entryPrice { newTrail = p.entryPrice } // floor at BE
			} else {
				newTrail = p.peakPrice + trailDist
				if newTrail > p.entryPrice { newTrail = p.entryPrice }
			}
			newTrail = math.Round(newTrail*100) / 100
			// Only tighten
			if p.side == "LONG" && newTrail > p.trailing {
				p.trailing = newTrail
				s.throttledReplaceSL(s.cfg.Symbol, "LONG", "SELL", p.remainQty, p.trailing)
			}
			if p.side == "SHORT" && (p.trailing == 0 || newTrail < p.trailing) {
				p.trailing = newTrail
				s.throttledReplaceSL(s.cfg.Symbol, "SHORT", "BUY", p.remainQty, p.trailing)
			}
		}
		// Phase 1: pnlR >= 1.5 → breakeven
		if pnlR >= 1.5 && !p.tp1RHit {
			p.trailing = p.entryPrice
			p.tp1RHit = true
			closeSide := "SELL"
			if p.side == "SHORT" { closeSide = "BUY" }
			s.throttledReplaceSL(s.cfg.Symbol, p.side, closeSide, p.remainQty, p.trailing)
			s.log.Info("TICK: trailing → breakeven", zap.String("side", p.side), zap.Float64("pnlR", pnlR))
		}

		// ── 4. Real-time bounce TP (remaining position) ──
		if p.remainQty < p.initQty && p.remainQty > 0 && pnlR > 0 {
			bounceThreshold := 0.5 * p.R
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

		// ── 4b. Emergency reversal: if losing > 0.8R, trigger async GPT check ──
		if pnlR < -0.8 && time.Since(s.lastEmergencyAt) > 30*time.Second {
			s.emergencyReversalCheck(ctx, price, p)
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

// emergencyReversalCheck launches an async GPT call when unrealized loss exceeds 0.8R.
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

		emergencyThreshold := s.cfg.ReversalConf - 0.07
		if emergencyThreshold < 0.55 { emergencyThreshold = 0.55 }

		if reverseConf >= emergencyThreshold {
			closedSide := p.side
			s.log.Warn("TICK: emergency reversal → close "+closedSide,
				zap.Float64("conf", reverseConf), zap.Float64("price", currentPrice))
			s.closePos(ctx, p, pptr, "emergency_reversal")

			if *pptr != nil { return }

			atr := s.calcATR()
			entry := math.Round(currentPrice*100) / 100
			flipThreshold := (emergencyThreshold + s.cfg.ConfidenceThreshold) / 2
			if closedSide == "LONG" && signal.Short != nil && signal.Short.Confidence >= flipThreshold && s.shortPos == nil {
				s.lastConf = signal.Short.Confidence
				s.log.Info("TICK: emergency flip → SHORT", zap.Float64("conf", signal.Short.Confidence))
				s.openTrend(ctx, "SHORT", currentPrice, entry, atr)
			}
			if closedSide == "SHORT" && signal.Long != nil && signal.Long.Confidence >= flipThreshold && s.longPos == nil {
				s.lastConf = signal.Long.Confidence
				s.log.Info("TICK: emergency flip → LONG", zap.Float64("conf", signal.Long.Confidence))
				s.openTrend(ctx, "LONG", currentPrice, entry, atr)
			}
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
		s.syncRemove(pos.side)
		*pptr = nil
	} else {
		// Update exchange SL qty to match remaining position after TP fill.
		if s.stagedEP != nil && pos.trailing > 0 {
			closeSide := "SELL"
			if pos.side == "SHORT" { closeSide = "BUY" }
			s.stagedEP.ReplaceSLOrder(s.cfg.Symbol, pos.side, closeSide, pos.remainQty, pos.trailing)
		}
		// Dynamic TP tightening: if oscillating, move far TPs closer to the fill price.
		s.maybeTightenTPs(pos, fill.Price)
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

// maybeTightenTPs moves unfilled far TPs closer to the last fill price in oscillation.
// In trending markets (|MTF| >= 2), TPs are kept to let profits run.
func (s *AIStrategy) maybeTightenTPs(pos *posState, lastFillPrice float64) {
	if s.stagedEP == nil { return }
	if math.Abs(float64(s.lastMTFScore)) >= 2 {
		s.log.Info("AI: keeping far TPs — trending market", zap.Int("mtf", s.lastMTFScore))
		return
	}

	// Count unfilled TPs
	var unfilled []int
	for i, tp := range pos.stagedTPs {
		if tp.Status == "pending" { unfilled = append(unfilled, i) }
	}
	if len(unfilled) == 0 { return }

	// Calculate new tighter prices: space them ATR×0.3 apart from the fill price
	atr := s.calcATR()
	spacing := atr * 0.3
	if spacing < 1.0 { spacing = 1.0 } // minimum $1 spacing

	needsReplace := false
	for j, idx := range unfilled {
		offset := spacing * float64(j+1)
		var newPrice float64
		if pos.side == "LONG" {
			newPrice = math.Round((lastFillPrice+offset)*100) / 100
		} else {
			newPrice = math.Round((lastFillPrice-offset)*100) / 100
		}
		old := pos.stagedTPs[idx].Price
		// Only tighten (move closer to entry), never push further out.
		// LONG TPs are ABOVE entry — tightening = lowering the price (closer to entry)
		// SHORT TPs are BELOW entry — tightening = raising the price (closer to entry)
		if pos.side == "LONG" && newPrice > old { continue }  // new is further out, skip
		if pos.side == "SHORT" && newPrice < old { continue } // new is further out, skip
		if math.Abs(newPrice-old) < 0.5 { continue } // negligible change
		pos.stagedTPs[idx].Price = newPrice
		needsReplace = true
	}

	if !needsReplace { return }

	// Cancel all existing TPs and re-place with new prices
	posSide := "LONG"
	closeSide := "SELL"
	if pos.side == "SHORT" {
		posSide = "SHORT"
		closeSide = "BUY"
	}
	s.stagedEP.CancelAllProtective(s.cfg.Symbol, posSide)

	// Re-place SL + new TPs
	var tps []strategy.StagedTP
	for _, tp := range pos.stagedTPs {
		if tp.Status == "pending" {
			tps = append(tps, strategy.StagedTP{Price: tp.Price, Qty: tp.Qty})
		}
	}
	if len(tps) > 0 {
		s.stagedEP.PlaceStagedTPOrders(s.cfg.Symbol, posSide, closeSide, pos.stopLoss, pos.remainQty, tps)
		s.log.Info("AI: tightened staged TPs (oscillation)",
			zap.String("side", pos.side),
			zap.Float64("fill_price", lastFillPrice),
			zap.Any("new_tps", tps))
	}
	s.saveStagedTPsToRedis(pos)
}
