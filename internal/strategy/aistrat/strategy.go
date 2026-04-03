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
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/redis/go-redis/v9"
)

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
		diff := fill.Price - pos.entryPrice
		pos.entryPrice = fill.Price
		pos.peakPrice = fill.Price
		pos.stopLoss += diff
		pos.trailing = pos.stopLoss
		pos.R = math.Abs(fill.Price - pos.stopLoss)
		if pos.mode == modeRange {
			// Dynamic TP based on BB width at fill time
			tpPct := s.cfg.RangeTPPct
			closes := s.getCloses()
			if len(closes) >= 20 {
				bb := indicator.BollingerBands(closes, s.cfg.BBPeriod, s.cfg.BBStdDev)
				bbU, bbL := indicator.Last(bb.Upper), indicator.Last(bb.Lower)
				if bbU > bbL && fill.Price > 0 {
					w := (bbU - bbL) / fill.Price * 0.6
					if w < s.cfg.BBWidthMin { w = s.cfg.BBWidthMin }
					if w > s.cfg.BBWidthMax { w = s.cfg.BBWidthMax }
					tpPct = w
				}
			}
			tpDist := fill.Price * tpPct
			slDist := fill.Price * s.cfg.RangeSLPct
			if pos.side == "LONG" {
				pos.takeProfit = fill.Price + tpDist
				pos.stopLoss = fill.Price - slDist
			} else {
				pos.takeProfit = fill.Price - tpDist
				pos.stopLoss = fill.Price + slDist
			}
		}
	}
	s.log.Info("AI: fill confirmed",
		zap.String("side", pos.side), zap.Float64("fill", fill.Price),
		zap.Float64("stop", pos.stopLoss), zap.Float64("tp", pos.takeProfit))

	// Trend mode: place staged TP orders on exchange immediately after fill.
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

func (s *AIStrategy) tickManage(ctx *strategy.Context, price float64, p *posState, pptr **posState) {
	// Trend mode with staged exchange orders: only do +0.5R breakeven SL move.
	// All TP/SL execution is handled by exchange-native limit/stop orders.
	if p.mode == modeTrend && p.stagedTPPlaced {
		s.checkBreakevenMove(ctx, price, p)
		return
	}

	// Range mode (or trend without staged orders): keep old tick-level SL check.
	if p.mode == modeRange {
		if (p.side == "LONG" && price <= p.stopLoss) || (p.side == "SHORT" && price >= p.stopLoss) {
			s.log.Warn("TICK STOP-LOSS", zap.String("side", p.side),
				zap.Float64("price", price), zap.Float64("stop", p.stopLoss))
			s.closePos(ctx, p, pptr, "stop_loss")
			s.consecLoss++
			s.stopBar = s.barCount
		}
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

	// Position fully closed (SL fired or all TPs filled) — cancel remaining orders on exchange.
	if pos.remainQty <= 0 {
		s.log.Info("AI: position fully closed by exchange order",
			zap.String("side", pos.side))
		// Cancel any remaining protective orders (e.g., SL still active after all TPs filled,
		// or TP orders still active after SL fired).
		if s.stagedEP != nil {
			posSide := "LONG"
			if pos.side == "SHORT" { posSide = "SHORT" }
			s.stagedEP.CancelAllProtective(s.cfg.Symbol, posSide)
		}
		s.consecLoss = 0
		s.syncRemove(pos.side)
		*pptr = nil
	} else {
		s.syncToRedis(pos)
	}
	return true
}
