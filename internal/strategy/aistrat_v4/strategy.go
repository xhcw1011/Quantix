package aistrat_v4

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

const (
	maxBarBuffer = 256 // keep last 256 bars; lookback + atr + headroom
	maxPosPct    = 0.20
)

// Strategy implements strategy.Strategy with single-shot z-score fade.
type Strategy struct {
	cfg Config
	log *zap.Logger

	// Bar buffer (chronological). Truncated to maxBarBuffer entries.
	closes []float64
	highs  []float64
	lows   []float64

	// Bar counter increments on every closed bar.
	barCount int

	// Open position state. nil when flat.
	pos *positionState

	// Per-side cooldown tracking.
	cd cooldown
}

// New creates a Strategy with the given config.
func New(cfg Config, log *zap.Logger) *Strategy {
	return &Strategy{cfg: cfg, log: log}
}

// Name returns a human-readable identifier.
func (s *Strategy) Name() string {
	return fmt.Sprintf("AI_v4(z>=%.1f,lb=%d)", s.cfg.EntryZScore, s.cfg.Lookback)
}

// OnBar processes a closed bar.
func (s *Strategy) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if bar.Symbol != s.cfg.Symbol {
		return
	}
	s.barCount++
	s.closes = append(s.closes, bar.Close)
	s.highs = append(s.highs, bar.High)
	s.lows = append(s.lows, bar.Low)
	if len(s.closes) > maxBarBuffer {
		s.closes = s.closes[len(s.closes)-maxBarBuffer:]
		s.highs = s.highs[len(s.highs)-maxBarBuffer:]
		s.lows = s.lows[len(s.lows)-maxBarBuffer:]
	}

	// 1. Check exit on existing position
	if s.pos != nil {
		if doClose, reason := shouldExit(s.closes, s.highs, s.lows, s.cfg, s.pos, s.barCount); doClose {
			s.log.Info("SIG_v4: CLOSE",
				zap.String("side", s.pos.Side),
				zap.String("reason", reason),
				zap.Float64("entry", s.pos.EntryPrice),
				zap.Float64("close", bar.Close),
				zap.Int("bars_held", s.barCount-s.pos.EntryBar),
			)
			s.placeCloseOrder(ctx, s.pos, bar.Close, reason)
			return
		}
	}

	// 2. Check entry when flat
	if s.pos == nil {
		side, _ := shouldEnter(
			s.closes, s.highs, s.lows, s.cfg, s.pos, s.barCount,
			withCooldown(s.cd),
		)
		if side != "" {
			z := zScore(s.closes, s.cfg.Lookback)
			s.placeOpenOrder(ctx, side, bar.Close, z)
		}
	}
}

// placeOpenOrder places a market open + records pending position state.
func (s *Strategy) placeOpenOrder(ctx *strategy.Context, side string, price, z float64) {
	a := atr(s.highs, s.lows, s.closes, 14)
	if a == 0 {
		s.log.Warn("SIG_v4: skip entry — ATR is zero")
		return
	}
	// SL distance covers (StopZScore - EntryZScore) std worth of price.
	// Approximation: ATR is similar magnitude to the std used by zScore.
	slDist := (s.cfg.StopZScore - s.cfg.EntryZScore) * a
	if slDist <= 0 {
		s.log.Warn("SIG_v4: skip entry — invalid sl distance")
		return
	}

	equity := ctx.Portfolio.Equity(map[string]float64{s.cfg.Symbol: price})
	qty := calcQty(equity, s.cfg.RiskPerTrade, slDist, price, maxPosPct, s.cfg.Leverage)
	if qty <= 0 {
		s.log.Warn("SIG_v4: skip entry — qty is zero")
		return
	}

	var orderSide strategy.Side
	var posSide strategy.PositionSide
	var slPrice, tpPrice float64
	if side == "LONG" {
		orderSide = strategy.SideBuy
		posSide = strategy.PositionSideLong
		slPrice = price - slDist
		tpPrice = price + s.cfg.EntryZScore*a
	} else {
		orderSide = strategy.SideSell
		posSide = strategy.PositionSideShort
		slPrice = price + slDist
		tpPrice = price - s.cfg.EntryZScore*a
	}

	req := strategy.OrderRequest{
		Symbol:       s.cfg.Symbol,
		Side:         orderSide,
		PositionSide: posSide,
		Type:         strategy.OrderMarket,
		Qty:          qty,
		// Entry-context snapshot for post-trade attribution. entry_stop lets the
		// backtest engine express MFE/MAE in R without registering a broker stop
		// (this strategy manages its own exits). regime is a look-ahead-safe
		// volatility bucket: 0=low_vol, 1=mid_vol, 2=high_vol.
		Meta: map[string]float64{
			"regime":     volRegime(s.highs, s.lows, s.closes),
			"zscore":     z,
			"atr":        a,
			"atr_pct":    a / price * 100,
			"entry_stop": slPrice,
		},
	}
	id := ctx.PlaceOrder(req)
	if id == "" {
		s.log.Error("SIG_v4: PlaceOrder returned empty id, entry aborted")
		return
	}

	s.pos = &positionState{
		Side:         side,
		EntryPrice:   price,
		EntryBar:     s.barCount,
		Qty:          qty,
		EntryZScore:  z,
		StopLossPx:   slPrice,
		TakeProfitPx: tpPrice,
		OrderID:      id,
	}

	s.log.Info("SIG_v4: OPEN",
		zap.String("side", side),
		zap.Float64("entry", price),
		zap.Float64("z", z),
		zap.Float64("qty", qty),
		zap.Float64("sl", slPrice),
		zap.Float64("tp", tpPrice),
		zap.Float64("atr", a),
	)
}

// placeCloseOrder places a market close + records cooldown.
func (s *Strategy) placeCloseOrder(ctx *strategy.Context, pos *positionState, price float64, reason string) {
	var orderSide strategy.Side
	var posSide strategy.PositionSide
	if pos.Side == "LONG" {
		orderSide = strategy.SideSell
		posSide = strategy.PositionSideLong
	} else {
		orderSide = strategy.SideBuy
		posSide = strategy.PositionSideShort
	}
	req := strategy.OrderRequest{
		Symbol:       s.cfg.Symbol,
		Side:         orderSide,
		PositionSide: posSide,
		Type:         strategy.OrderMarket,
		Qty:          pos.Qty,
		Reason:       normalizeExitReason(reason), // sl/tp/time → canonical attribution tags
	}
	id := ctx.PlaceOrder(req)
	if id == "" {
		s.log.Error("SIG_v4: close PlaceOrder returned empty id")
		return
	}

	if pos.Side == "LONG" {
		s.cd.LastLongCloseBar = s.barCount
	} else {
		s.cd.LastShortCloseBar = s.barCount
	}
	s.pos = nil

	s.log.Info("SIG_v4: CLOSE_PLACED",
		zap.String("side", pos.Side),
		zap.String("reason", reason),
		zap.Float64("close", price),
		zap.Float64("qty", pos.Qty),
		zap.String("close_oms_id", id),
	)
}

// OnFill is called when our orders fill on the exchange. v4 keeps state purely
// from bar events for MVP.
func (s *Strategy) OnFill(_ *strategy.Context, _ strategy.Fill) {}

// volRegime classifies the current volatility regime from the ratio of a short
// (14-bar) ATR to a longer (50-bar) baseline, using only closed bars — no
// look-ahead. Returns a code for OrderRequest.Meta["regime"]:
//
//	0 = low_vol  (contracting, ratio ≤ 0.8)
//	1 = mid_vol  (normal)
//	2 = high_vol (expanding, ratio ≥ 1.3)
//
// A missing/zero baseline falls back to mid_vol.
func volRegime(highs, lows, closes []float64) float64 {
	short := atr(highs, lows, closes, 14)
	long := atr(highs, lows, closes, 50)
	if short == 0 || long == 0 {
		return 1
	}
	switch r := short / long; {
	case r >= 1.3:
		return 2
	case r <= 0.8:
		return 0
	default:
		return 1
	}
}

// normalizeExitReason maps this strategy's terse exit codes to the canonical
// attribution vocabulary shared with the backtest broker and analyzer.
func normalizeExitReason(reason string) string {
	switch reason {
	case "sl":
		return "stop_loss"
	case "tp":
		return "take_profit"
	case "time":
		return "time_exit"
	default:
		return reason
	}
}
