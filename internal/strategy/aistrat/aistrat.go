// Package aistrat implements an AI-driven dual-mode trading strategy.
//
// Trend Mode: R-based sizing, trailing stop, let profits run.
// Range Mode: fixed TP/SL scalping, quick in/out, supports simultaneous long+short.
//
// GPT decides direction (BUY/SELL/HOLD). Regime detection picks the mode.
// Hedge Mode: LONG and SHORT positions managed independently.
package aistrat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

func init() {
	registry.Register("ai", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := DefaultConfig()
		if v, ok := params["Symbol"].(string); ok { cfg.Symbol = v }
		if v, ok := params["APIKey"].(string); ok { cfg.APIKey = v }
		if v, ok := params["Model"].(string); ok { cfg.Model = v }
		if v, ok := params["ConfidenceThreshold"]; ok { cfg.ConfidenceThreshold = toFloat(v) }
		if v, ok := params["LookbackBars"]; ok { cfg.LookbackBars = toInt(v) }
		if v, ok := params["CallIntervalBars"]; ok { cfg.CallIntervalBars = toInt(v) }
		if v, ok := params["RiskPerTrade"]; ok { cfg.RiskPerTrade = toFloat(v) }
		if v, ok := params["ATRK"]; ok { cfg.ATRK = toFloat(v) }
		if v, ok := params["TrailingATRK"]; ok { cfg.TrailingATRK = toFloat(v) }
		if v, ok := params["MaxDailyLossPct"]; ok { cfg.MaxDailyLossPct = toFloat(v) }
		if v, ok := params["MaxConsecLoss"]; ok { cfg.MaxConsecLoss = toInt(v) }
		if v, ok := params["EnableShort"].(bool); ok { cfg.EnableShort = v }
		if v, ok := params["HedgeMode"].(bool); ok { cfg.HedgeMode = v }
		if v, ok := params["RangeTPPct"]; ok { cfg.RangeTPPct = toFloat(v) }
		if v, ok := params["RangeSLPct"]; ok { cfg.RangeSLPct = toFloat(v) }
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("ai strategy requires APIKey parameter")
		}
		return New(cfg, log), nil
	})
}

// ─── Config ──────────────────────────────────────────────────────────────────

type Config struct {
	Symbol              string
	APIKey              string
	Model               string
	ConfidenceThreshold float64
	LookbackBars        int
	CallIntervalBars    int
	EnableShort         bool
	HedgeMode           bool // true = long+short simultaneously; false = single strongest direction

	// Trend mode
	RiskPerTrade float64 // 1% of equity per trade
	ATRK         float64 // stop-loss ATR multiplier
	TrailingATRK float64 // trailing ATR multiplier

	// Range/scalp mode (percentage of entry price)
	RangeTPPct float64 // take-profit % (default 0.004 = 0.4%)
	RangeSLPct float64 // stop-loss % (default 0.0025 = 0.25%)

	// Risk limits
	MaxDailyLossPct float64
	MaxConsecLoss   int
}

func DefaultConfig() Config {
	return Config{
		Symbol: "ETHUSDT", Model: "gpt-5.4-mini",
		ConfidenceThreshold: 0.82, LookbackBars: 60,
		CallIntervalBars: 10, EnableShort: true,
		RiskPerTrade: 0.02, ATRK: 4.0, TrailingATRK: 10.0,
		RangeTPPct: 0.012, RangeSLPct: 0.010,
		MaxDailyLossPct: 0.10, MaxConsecLoss: 5,
	}
}

// ─── Position ────────────────────────────────────────────────────────────────

type posMode int
const (
	modeTrend posMode = iota
	modeRange
)

type posState struct {
	side       string  // "LONG" or "SHORT"
	mode       posMode
	entryPrice float64
	initQty    float64
	remainQty  float64
	R          float64
	stopLoss   float64
	takeProfit float64 // range mode: fixed TP price
	trailing   float64
	peakPrice  float64
	tp1RHit    bool
	barsHeld   int
	filled     bool
	orderID    string
	limitBar   int
}

// ─── Strategy ────────────────────────────────────────────────────────────────

type AIStrategy struct {
	cfg    Config
	log    *zap.Logger
	client *http.Client

	bars        []exchange.Kline
	warmedUp    bool
	barCount    int
	lastCallBar int
	totalCall   int

	longPos  *posState
	shortPos *posState
	syncer   *position.Syncer // Redis-backed, set at warmup from ctx.Extra

	dayStart       time.Time
	dayStartEquity float64
	consecLoss     int
	dayHalted      bool
	cooldownUntil  int // bar index — no new entries until barCount >= this
}

func New(cfg Config, log *zap.Logger) *AIStrategy {
	return &AIStrategy{
		cfg: cfg, log: log,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *AIStrategy) Name() string {
	return fmt.Sprintf("AI(%s/every%dbars)", s.cfg.Model, s.cfg.CallIntervalBars)
}

// ─── OnBar ───────────────────────────────────────────────────────────────────

func (s *AIStrategy) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if bar.Symbol != s.cfg.Symbol { return }

	s.bars = append(s.bars, bar)
	if len(s.bars) > s.cfg.LookbackBars*2 {
		s.bars = s.bars[len(s.bars)-s.cfg.LookbackBars*2:]
	}
	s.barCount++

	if !s.warmedUp {
		if len(s.bars) >= s.cfg.LookbackBars && time.Since(bar.CloseTime) < 10*time.Minute {
			s.warmedUp = true
			s.dayStart = time.Now()
			if pf := ctx.Portfolio; pf != nil {
				s.dayStartEquity = pf.Equity(map[string]float64{s.cfg.Symbol: bar.Close})
			}
			// Get syncer from context (injected by live engine)
			if v, ok := ctx.Extra["position_syncer"]; ok {
				if ps, ok := v.(*position.Syncer); ok {
					s.syncer = ps
					// Set onChange callback to detect external closes
					// (already wired in engine, but we can also react here)
				}
			}
			// Recover positions from syncer (Redis → exchange verified)
			s.recoverFromSyncer(bar.Close)
			s.log.Info("AI warmed up", zap.Int("bars", len(s.bars)),
				zap.Bool("syncer", s.syncer != nil),
				zap.Bool("long", s.longPos != nil),
				zap.Bool("short", s.shortPos != nil))
		}
		return
	}

	price := bar.Close
	s.checkDayReset(ctx, price)
	if s.dayHalted { return }

	// Check syncer for externally closed positions (manual close on exchange)
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

	// Manage both positions independently (every bar)
	if s.longPos != nil { s.managePos(ctx, bar, s.longPos, &s.longPos) }
	if s.shortPos != nil { s.managePos(ctx, bar, s.shortPos, &s.shortPos) }

	// GPT signal check (every N bars)
	interval := s.cfg.CallIntervalBars
	if interval < 1 { interval = 1 }
	if s.barCount-s.lastCallBar < interval { return }

	// Cooldown after stop-loss — wait 10 bars before new entries
	if s.barCount < s.cooldownUntil { return }

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

	isRange := s.isRangeRegime(price)
	maxDev := price * 0.005

	// Rule-based boost: if price is near swing low/high, override GPT confidence
	swLow := s.findSwingLow(10)
	swHigh := s.findSwingHigh(10)
	if price > 0 && swLow > 0 && (price-swLow)/price < 0.0015 && longConf < 0.70 {
		s.log.Info("AI: boost long — price near swing low",
			zap.Float64("price", price), zap.Float64("swing_low", swLow))
		longConf = 0.70
		if longEntry <= 0 { longEntry = swLow }
	}
	if price > 0 && swHigh > 0 && (swHigh-price)/price < 0.0015 && shortConf < 0.70 {
		s.log.Info("AI: boost short — price near swing high",
			zap.Float64("price", price), zap.Float64("swing_high", swHigh))
		shortConf = 0.70
		if shortEntry <= 0 { shortEntry = swHigh }
	}

	// Minimum entry offset: ensure limit orders get a better fill than market
	minOffset := price * 0.0015 // 0.15% minimum from current price

	// Minimum spread between long and short to avoid self-hedging
	// Only open opposite direction if entries are at least 0.5% apart
	minSpread := price * 0.0035 // ~$7 at ETH $2000

	// Single-direction mode: only open the strongest signal, not both
	if !s.cfg.HedgeMode {
		if longConf >= s.cfg.ConfidenceThreshold && shortConf >= s.cfg.ConfidenceThreshold {
			// Both qualify — pick the stronger one
			if longConf >= shortConf {
				shortConf = 0 // suppress short
			} else {
				longConf = 0 // suppress long
			}
		}
		// Also don't open opposite direction if already have a position
		if s.longPos != nil && shortConf >= s.cfg.ConfidenceThreshold {
			shortConf = 0 // already long, don't open short
		}
		if s.shortPos != nil && longConf >= s.cfg.ConfidenceThreshold {
			longConf = 0 // already short, don't open long
		}
	}

	// ── Trend protection: don't trade against momentum ──
	// Uses 60-bar (1h) trend + 10-bar (10min) momentum to allow rebounds
	closes := s.getCloses()
	if len(closes) >= 60 {
		ret60 := (closes[len(closes)-1] - closes[len(closes)-60]) / closes[len(closes)-60]
		ret10 := 0.0
		if len(closes) >= 10 {
			ret10 = (closes[len(closes)-1] - closes[len(closes)-10]) / closes[len(closes)-10]
		}

		// Downtrend: block long UNLESS short-term is rebounding
		if ret60 < -0.005 && ret10 < 0.003 && longConf > 0 {
			s.log.Info("AI: BUY blocked — downtrend, no rebound",
				zap.Float64("1h", ret60*100), zap.Float64("10m", ret10*100))
			longConf = 0
		}
		// Uptrend: block short UNLESS short-term is pulling back
		if ret60 > 0.005 && ret10 > -0.003 && shortConf > 0 {
			s.log.Info("AI: SELL blocked — uptrend, no pullback",
				zap.Float64("1h", ret60*100), zap.Float64("10m", ret10*100))
			shortConf = 0
		}
	}

	// ── Open LONG if confident ──
	if longConf >= s.cfg.ConfidenceThreshold && s.longPos == nil {
		// Don't open long if short exists (filled or pending) and too close
		if s.shortPos != nil {
			if math.Abs(s.shortPos.entryPrice-price) < minSpread {
				longConf = 0
			}
		}
		entry := longEntry
		// Long entry must be below current price by at least 0.15%
		if entry <= 0 || entry > price-minOffset { entry = price - minOffset }
		if (price - entry) > maxDev { entry = price - minOffset } // cap at max deviation
		entry = math.Round(entry*100) / 100
		if isRange {
			s.openRange(ctx, "LONG", price, entry, atr)
		} else {
			s.openTrend(ctx, "LONG", price, entry, atr)
		}
	}

	// ── Open SHORT if confident ──
	if shortConf >= s.cfg.ConfidenceThreshold && s.cfg.EnableShort && s.shortPos == nil {
		// Don't open short if long exists (filled or pending) and too close
		if s.longPos != nil {
			if math.Abs(s.longPos.entryPrice-price) < minSpread {
				shortConf = 0
			}
		}
		entry := shortEntry
		// Short entry must be above current price by at least 0.15%
		if entry <= 0 || entry < price+minOffset { entry = price + minOffset }
		if (entry - price) > maxDev { entry = price + minOffset }
		entry = math.Round(entry*100) / 100
		if isRange {
			s.openRange(ctx, "SHORT", price, entry, atr)
		} else {
			s.openTrend(ctx, "SHORT", price, entry, atr)
		}
	}
}

func (s *AIStrategy) OnFill(ctx *strategy.Context, fill strategy.Fill) {
	// Match fill to the correct position
	pos := s.longPos
	if fill.Side == strategy.SideSell && fill.PositionSide == strategy.PositionSideShort {
		pos = s.shortPos // opening short
	}
	if fill.Side == strategy.SideBuy && fill.PositionSide == strategy.PositionSideLong {
		pos = s.longPos // opening long
	}
	if pos == nil || pos.filled { return }

	pos.filled = true
	if fill.Price > 0 {
		diff := fill.Price - pos.entryPrice
		pos.entryPrice = fill.Price
		pos.peakPrice = fill.Price
		pos.stopLoss += diff
		pos.trailing = pos.stopLoss
		pos.R = math.Abs(fill.Price - pos.stopLoss)
		if pos.mode == modeRange {
			tpDist := fill.Price * s.cfg.RangeTPPct
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
}

// ─── Regime Detection ────────────────────────────────────────────────────────

func (s *AIStrategy) isRangeRegime(price float64) bool {
	closes := s.getCloses()
	if len(closes) < 50 { return true }
	ema20 := indicator.Last(indicator.EMA(closes, 20))
	ema50 := indicator.Last(indicator.EMA(closes, 50))
	if price > 0 && math.Abs(ema20-ema50)/price < 0.003 {
		return true // EMAs converged = range
	}
	return false
}

// ─── Open Position ───────────────────────────────────────────────────────────

func (s *AIStrategy) openRange(ctx *strategy.Context, side string, currentPrice, entryPrice, atr float64) {
	entryPrice = math.Round(entryPrice*100) / 100

	// TP/SL as percentage of entry price — scales with price level
	tpDist := entryPrice * s.cfg.RangeTPPct // e.g. $2060 × 0.4% = $8.24
	slDist := entryPrice * s.cfg.RangeSLPct // e.g. $2060 × 0.25% = $5.15

	var stopLoss, takeProfit float64
	if side == "LONG" {
		takeProfit = entryPrice + tpDist
		stopLoss = entryPrice - slDist
	} else {
		takeProfit = entryPrice - tpDist
		stopLoss = entryPrice + slDist
	}
	if slDist <= 0 { return }

	equity := 100.0
	if pf := ctx.Portfolio; pf != nil {
		equity = pf.Equity(map[string]float64{s.cfg.Symbol: currentPrice})
	}
	qty := math.Floor(equity*s.cfg.RiskPerTrade/slDist*1000) / 1000
	if qty <= 0 { return }

	useLimit := math.Abs(entryPrice-currentPrice) > 0.01
	omsID := s.placeOrder(ctx, side, entryPrice, qty, useLimit)
	if omsID == "" { return }

	pos := &posState{
		side: side, mode: modeRange, entryPrice: entryPrice,
		initQty: qty, remainQty: qty,
		R: slDist, stopLoss: stopLoss, takeProfit: takeProfit,
		trailing: stopLoss, peakPrice: entryPrice,
		filled: !useLimit, orderID: omsID, limitBar: s.barCount,
	}
	if side == "LONG" { s.longPos = pos } else { s.shortPos = pos }

	s.log.Info("AI: OPEN RANGE",
		zap.String("side", side), zap.Float64("entry", entryPrice),
		zap.Float64("tp", takeProfit), zap.Float64("sl", stopLoss),
		zap.Float64("qty", qty))
	s.syncToRedis(pos)
}

func (s *AIStrategy) openTrend(ctx *strategy.Context, side string, currentPrice, entryPrice, atr float64) {
	entryPrice = math.Round(entryPrice*100) / 100
	minDist := entryPrice * 0.008
	atrDist := atr * s.cfg.ATRK
	if atrDist < minDist { atrDist = minDist }

	var stopLoss float64
	if side == "LONG" {
		stopLoss = entryPrice - atrDist
		if stopLoss >= entryPrice { return }
	} else {
		stopLoss = entryPrice + atrDist
		if stopLoss <= entryPrice { return }
	}

	R := math.Abs(entryPrice - stopLoss)
	if R <= 0 { return }
	equity := 100.0
	if pf := ctx.Portfolio; pf != nil {
		equity = pf.Equity(map[string]float64{s.cfg.Symbol: currentPrice})
	}
	qty := math.Floor(equity*s.cfg.RiskPerTrade/R*1000) / 1000
	if qty <= 0 { return }

	useLimit := math.Abs(entryPrice-currentPrice) > 0.01
	omsID := s.placeOrder(ctx, side, entryPrice, qty, useLimit)
	if omsID == "" { return }

	pos := &posState{
		side: side, mode: modeTrend, entryPrice: entryPrice,
		initQty: qty, remainQty: qty,
		R: R, stopLoss: stopLoss, trailing: stopLoss, peakPrice: entryPrice,
		filled: !useLimit, orderID: omsID, limitBar: s.barCount,
	}
	if side == "LONG" { s.longPos = pos } else { s.shortPos = pos }

	s.log.Info("AI: OPEN TREND",
		zap.String("side", side), zap.Float64("entry", entryPrice),
		zap.Float64("sl", stopLoss), zap.Float64("R", R), zap.Float64("qty", qty))
	s.syncToRedis(pos)
}

func (s *AIStrategy) placeOrder(ctx *strategy.Context, side string, price, qty float64, useLimit bool) string {
	psSide := strategy.PositionSideLong
	orderSide := strategy.SideBuy
	if side == "SHORT" {
		psSide = strategy.PositionSideShort
		orderSide = strategy.SideSell
	}
	req := strategy.OrderRequest{
		Symbol: s.cfg.Symbol, Side: orderSide, PositionSide: psSide, Qty: qty,
	}
	if useLimit {
		req.Type = strategy.OrderLimit
		req.Price = price
	}
	return ctx.PlaceOrder(req)
}

// ─── Position Management (every bar) ─────────────────────────────────────────

func (s *AIStrategy) managePos(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	price := bar.Close
	p.barsHeld++

	// Limit order pending
	if !p.filled {
		if s.barCount-p.limitBar > 15 {
			s.log.Warn("AI: limit timeout", zap.String("side", p.side), zap.String("id", p.orderID))
			if p.orderID != "" { ctx.CancelOrder(p.orderID) }
			*pptr = nil
			return
		}
		return
	}

	// Update peak
	if p.side == "LONG" && price > p.peakPrice { p.peakPrice = price }
	if p.side == "SHORT" && price < p.peakPrice { p.peakPrice = price }

	// ── Stop-loss (both modes) ──
	if (p.side == "LONG" && price <= p.stopLoss) || (p.side == "SHORT" && price >= p.stopLoss) {
		s.log.Warn("STOP-LOSS", zap.String("side", p.side), zap.Float64("price", price), zap.Float64("stop", p.stopLoss))
		s.closePos(ctx, p, pptr, "stop_loss")
		s.consecLoss++
		s.cooldownUntil = s.barCount + 10 // 10 bar cooldown after stop-loss
		s.log.Info("AI: cooldown 10 bars after stop-loss")
		return
	}

	if p.barsHeld < 3 { return } // minimum hold

	if p.mode == modeRange {
		s.manageRange(ctx, bar, p, pptr)
	} else {
		s.manageTrend(ctx, bar, p, pptr)
	}
}

func (s *AIStrategy) manageRange(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	price := bar.Close

	// ── Take-profit hit: decide whether to close or upgrade to trend ──
	tpHit := (p.side == "LONG" && price >= p.takeProfit) ||
		(p.side == "SHORT" && price <= p.takeProfit)

	if tpHit && !p.tp1RHit {
		// Check momentum: is the move continuing?
		closes := s.getCloses()
		rsi := indicator.Last(indicator.RSI(closes, 14))
		macd := indicator.MACD(closes, 12, 26, 9)
		macdHist := indicator.Last(macd.Histogram)

		strongMomentum := false
		if p.side == "LONG" && rsi > 55 && macdHist > 0 {
			strongMomentum = true
		}
		if p.side == "SHORT" && rsi < 45 && macdHist < 0 {
			strongMomentum = true
		}

		if strongMomentum {
			// Momentum continuing → close 50%, upgrade rest to trend mode
			halfQty := math.Floor(p.initQty*0.50*1000) / 1000
			if halfQty > 0 {
				s.log.Info("RANGE TP → UPGRADE to trend (momentum strong)",
					zap.String("side", p.side), zap.Float64("price", price),
					zap.Float64("rsi", rsi), zap.Float64("macd_hist", macdHist))
				s.closePartial(ctx, p, pptr, halfQty, "range_tp_partial")
			}
			if *pptr == nil { return } // fully closed

			// Convert to trend mode
			p.mode = modeTrend
			p.tp1RHit = true // treat the range TP as the 1R event
			atr := s.calcATR()
			// Move stop to breakeven
			buf := p.entryPrice * 0.001
			if p.side == "LONG" {
				p.stopLoss = p.entryPrice - buf
				p.trailing = p.peakPrice - atr*s.cfg.TrailingATRK
			} else {
				p.stopLoss = p.entryPrice + buf
				p.trailing = p.peakPrice + atr*s.cfg.TrailingATRK
			}
			p.R = math.Abs(price - p.stopLoss)
			s.consecLoss = 0
			return
		}

		// Momentum fading → close all, take the scalp profit
		s.log.Info("RANGE TP → close all (momentum fading)",
			zap.String("side", p.side), zap.Float64("price", price))
		s.closePos(ctx, p, pptr, "range_tp")
		s.consecLoss = 0
		return
	}

	// ── Timeout: 45 bars ──
	if p.barsHeld >= 45 {
		s.log.Info("RANGE TIMEOUT", zap.String("side", p.side), zap.Int("bars", p.barsHeld))
		s.closePos(ctx, p, pptr, "timeout")
		return
	}
}

func (s *AIStrategy) manageTrend(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	price := bar.Close
	atr := s.calcATR()

	if p.barsHeld < 5 { return }

	pnlR := 0.0
	if p.R > 0 {
		if p.side == "LONG" { pnlR = (price - p.entryPrice) / p.R }
		if p.side == "SHORT" { pnlR = (p.entryPrice - price) / p.R }
	}

	// TP +4R: close 25%, move stop to +2R (let profits run longer)
	if !p.tp1RHit && pnlR >= 4.0 {
		qty := math.Floor(p.initQty*0.25*1000) / 1000
		if qty > 0 {
			s.log.Info("TP +4R → close 25%", zap.Float64("pnl_R", pnlR))
			s.closePartial(ctx, p, pptr, qty, "tp_4R")
			p.tp1RHit = true
			if p.side == "LONG" { p.stopLoss = p.entryPrice + p.R*2 }
			if p.side == "SHORT" { p.stopLoss = p.entryPrice - p.R*2 }
			s.consecLoss = 0
		}
	}
	// Remaining 75% rides trailing stop

	// Trailing (ATR with minimum distance floor of 1.2%)
	trailDist := atr * s.cfg.TrailingATRK
	minTrailDist := p.peakPrice * 0.012
	if trailDist < minTrailDist { trailDist = minTrailDist }

	if p.side == "LONG" {
		nt := p.peakPrice - trailDist
		if nt > p.trailing { p.trailing = nt }
		if price <= p.trailing && p.trailing > p.stopLoss {
			s.log.Info("TRAILING STOP", zap.Float64("price", price), zap.Float64("trail", p.trailing))
			s.closePos(ctx, p, pptr, "trailing")
			if pnlR > 0 { s.consecLoss = 0 }
			return
		}
	} else {
		nt := p.peakPrice + trailDist
		if nt < p.trailing { p.trailing = nt }
		if price >= p.trailing && p.trailing > 0 && p.trailing < p.stopLoss {
			s.log.Info("TRAILING STOP", zap.Float64("price", price), zap.Float64("trail", p.trailing))
			s.closePos(ctx, p, pptr, "trailing")
			if pnlR > 0 { s.consecLoss = 0 }
			return
		}
	}

	// GPT reversal check
	if s.barCount-s.lastCallBar >= s.cfg.CallIntervalBars && p.barsHeld >= 5 {
		s.checkReversal(ctx, bar, p, pptr)
	}
}

func (s *AIStrategy) checkReversal(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	mktCtx := s.buildContext(ctx, bar)
	signal, err := s.callGPT(mktCtx)
	if err != nil { s.lastCallBar = s.barCount; return }
	s.lastCallBar = s.barCount
	s.totalCall++

	s.log.Info("AI reversal check",
		zap.String("holding", p.side), zap.String("signal", signal.Action),
		zap.Float64("confidence", signal.Confidence),
		zap.String("reasoning", signal.Reasoning))

	if signal.Confidence < 0.75 { return }
	if p.side == "LONG" && signal.Action == "SELL" {
		s.log.Info("AI: reversal → close LONG")
		s.closePos(ctx, p, pptr, "gpt_reversal")
	}
	if p.side == "SHORT" && signal.Action == "BUY" {
		s.log.Info("AI: reversal → close SHORT")
		s.closePos(ctx, p, pptr, "gpt_reversal")
	}
}

// ─── Close Helpers ───────────────────────────────────────────────────────────

func (s *AIStrategy) closePos(ctx *strategy.Context, p *posState, pptr **posState, reason string) {
	qty := math.Floor(p.remainQty*1000) / 1000
	if qty <= 0 { *pptr = nil; return }

	closeSide := strategy.SideSell
	psSide := strategy.PositionSideLong
	if p.side == "SHORT" {
		closeSide = strategy.SideBuy
		psSide = strategy.PositionSideShort
	}
	ctx.PlaceOrder(strategy.OrderRequest{
		Symbol: s.cfg.Symbol, Side: closeSide, PositionSide: psSide, Qty: qty,
	})
	s.log.Info("AI: CLOSE", zap.String("side", p.side), zap.String("reason", reason),
		zap.Float64("entry", p.entryPrice), zap.Float64("qty", qty))
	s.syncRemove(p.side)
	*pptr = nil
}

func (s *AIStrategy) closePartial(ctx *strategy.Context, p *posState, pptr **posState, qty float64, reason string) {
	qty = math.Floor(qty*1000) / 1000
	if qty <= 0 { return }
	if qty > p.remainQty { qty = p.remainQty }

	closeSide := strategy.SideSell
	psSide := strategy.PositionSideLong
	if p.side == "SHORT" {
		closeSide = strategy.SideBuy
		psSide = strategy.PositionSideShort
	}
	ctx.PlaceOrder(strategy.OrderRequest{
		Symbol: s.cfg.Symbol, Side: closeSide, PositionSide: psSide, Qty: qty,
	})
	p.remainQty -= qty
	if p.remainQty <= 1e-10 {
		s.syncRemove(p.side)
		*pptr = nil
	} else {
		s.syncToRedis(p) // update qty in Redis
	}
}

// ─── Daily Risk ──────────────────────────────────────────────────────────────

func (s *AIStrategy) checkDayReset(ctx *strategy.Context, price float64) {
	now := time.Now()
	if now.YearDay() != s.dayStart.YearDay() || now.Year() != s.dayStart.Year() {
		s.dayStart = now
		if pf := ctx.Portfolio; pf != nil {
			s.dayStartEquity = pf.Equity(map[string]float64{s.cfg.Symbol: price})
		}
		s.dayHalted = false
		s.consecLoss = 0
		s.log.Info("AI: new day", zap.Float64("equity", s.dayStartEquity))
	}
}

// ─── Technical Helpers ───────────────────────────────────────────────────────

func (s *AIStrategy) getCloses() []float64 {
	c := make([]float64, len(s.bars))
	for i, b := range s.bars { c[i] = b.Close }
	return c
}

func (s *AIStrategy) calcATR() float64 {
	n := 60
	if len(s.bars) < n+1 { n = len(s.bars) - 1; if n < 5 { return 0 } }
	recent := s.bars[len(s.bars)-n-1:]
	var sum float64
	for i := 1; i < len(recent); i++ {
		sum += math.Max(recent[i].High-recent[i].Low,
			math.Max(math.Abs(recent[i].High-recent[i-1].Close), math.Abs(recent[i].Low-recent[i-1].Close)))
	}
	return sum / float64(n)
}

// ─── GPT ─────────────────────────────────────────────────────────────────────

type gptSignal struct {
	Action     string  `json:"action"`
	Confidence float64 `json:"confidence"`
	EntryPrice float64 `json:"entry_price"`
	Reasoning  string  `json:"reasoning"`
	// Dual signals for hedge mode
	Long  *subSignal `json:"long,omitempty"`
	Short *subSignal `json:"short,omitempty"`
}

type subSignal struct {
	Confidence float64 `json:"confidence"`
	EntryPrice float64 `json:"entry_price"`
	Reasoning  string  `json:"reasoning"`
}

const systemPrompt = `You are a crypto futures trader using HEDGE MODE (hold long AND short simultaneously).

Evaluate BOTH directions independently every time. In range markets, BOTH sides should often be >= 0.65.

RESPONSE (strict JSON):
{"long":{"confidence":0.0-1.0,"entry_price":0.00,"reasoning":"..."},"short":{"confidence":0.0-1.0,"entry_price":0.00,"reasoning":"..."}}

RULES:
- long entry_price: at nearest SUPPORT (swing_low_10, bb_lower, EMA20), below current price
- short entry_price: at nearest RESISTANCE (swing_high_10, bb_upper), above current price
- entry_price within 0.5% of current price
- Both can be >= 0.65 simultaneously — this is EXPECTED in range markets

CRITICAL — CHECK return_60bar FIRST:
- return_60bar < -0.5%: DOWNTREND. Do NOT buy. Only short or hold.
- return_60bar > +0.5%: UPTREND. Do NOT short. Only buy or hold.
- return_60bar between -0.5% and +0.5%: RANGE. Both sides OK.

RANGE MARKET:
- LONG at swing_low / bb_lower — confidence 0.65-0.80
- SHORT at swing_high / bb_upper — confidence 0.65-0.80

TREND MARKET:
- Trade WITH the trend only: higher confidence for trend direction
- Counter-trend: confidence < 0.30 (strongly discouraged)

KEY: In ranging markets you MUST give both sides a fair evaluation. Do not bias toward one direction.`

type mktCtx struct {
	Symbol     string             `json:"symbol"`
	Price      float64            `json:"price"`
	Indicators map[string]float64 `json:"indicators"`
	RecentBars []barData          `json:"recent_bars"`
	Position   string             `json:"position"`
}
type barData struct {
	T string `json:"t"`; O, H, L, C, V float64
}

func (s *AIStrategy) buildContext(ctx *strategy.Context, bar exchange.Kline) mktCtx {
	closes := s.getCloses()
	rsi := indicator.Last(indicator.RSI(closes, 14))
	macd := indicator.MACD(closes, 12, 26, 9)
	bb := indicator.BollingerBands(closes, 20, 2.0)
	ema20 := indicator.Last(indicator.EMA(closes, 20))
	ema50 := indicator.Last(indicator.EMA(closes, 50))
	atr := s.calcATR()
	bbU, bbL := indicator.Last(bb.Upper), indicator.Last(bb.Lower)
	bbPos := 0.5; if bbU-bbL > 0 { bbPos = (bar.Close - bbL) / (bbU - bbL) }
	vols := make([]float64, len(s.bars)); for i, b := range s.bars { vols[i] = b.Volume }
	volMA := indicator.Last(indicator.SMA(vols, 20)); vr := 1.0; if volMA > 0 { vr = bar.Volume / volMA }

	ind := map[string]float64{
		"rsi": r2(rsi), "macd_hist": r2(indicator.Last(macd.Histogram)),
		"ema20": r2(ema20), "ema50": r2(ema50),
		"bb_upper": r2(bbU), "bb_lower": r2(bbL), "bb_pos": r3(bbPos),
		"atr": r2(atr), "vol_ratio": r3(vr),
		"swing_high_10": r2(s.findSwingHigh(10)), "swing_low_10": r2(s.findSwingLow(10)),
		"return_60bar": func() float64 {
			c := s.getCloses()
			if len(c) < 60 { return 0 }
			return r3((c[len(c)-1] - c[len(c)-60]) / c[len(c)-60] * 100)
		}(),
		"return_10bar": func() float64 {
			c := s.getCloses()
			if len(c) < 10 { return 0 }
			return r3((c[len(c)-1] - c[len(c)-10]) / c[len(c)-10] * 100)
		}(),
	}

	n := 10; if len(s.bars) < n { n = len(s.bars) }
	bars := make([]barData, n); st := len(s.bars) - n
	for i := 0; i < n; i++ {
		b := s.bars[st+i]
		bars[i] = barData{T: b.OpenTime.Format("15:04"), O: r2(b.Open), H: r2(b.High), L: r2(b.Low), C: r2(b.Close), V: r2(b.Volume)}
	}

	posStr := "FLAT"
	parts := []string{}
	if s.longPos != nil && s.longPos.filled { parts = append(parts, fmt.Sprintf("LONG@%.2f", s.longPos.entryPrice)) }
	if s.shortPos != nil && s.shortPos.filled { parts = append(parts, fmt.Sprintf("SHORT@%.2f", s.shortPos.entryPrice)) }
	if len(parts) > 0 { posStr = fmt.Sprintf("%v", parts) }

	return mktCtx{Symbol: s.cfg.Symbol, Price: r2(bar.Close), Indicators: ind, RecentBars: bars, Position: posStr}
}

func (s *AIStrategy) callGPT(mc mktCtx) (gptSignal, error) {
	ctxJSON, _ := json.Marshal(mc)
	body, _ := json.Marshal(map[string]any{
		"model": s.cfg.Model, "temperature": 0.3, "max_completion_tokens": 250,
		"messages": []map[string]string{{"role": "system", "content": systemPrompt}, {"role": "user", "content": string(ctxJSON)}},
	})
	callCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil { return gptSignal{}, err }
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	resp, err := s.client.Do(req); if err != nil { return gptSignal{}, err }
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 { return gptSignal{}, fmt.Errorf("GPT %d: %s", resp.StatusCode, string(rb)) }
	var gr struct{ Choices []struct{ Message struct{ Content string `json:"content"` } `json:"message"` } `json:"choices"` }
	json.Unmarshal(rb, &gr)
	if len(gr.Choices) == 0 { return gptSignal{}, fmt.Errorf("no choices") }
	var sig gptSignal
	if err := json.Unmarshal([]byte(gr.Choices[0].Message.Content), &sig); err != nil {
		return gptSignal{}, fmt.Errorf("parse %q: %w", gr.Choices[0].Message.Content, err)
	}
	return sig, nil
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
			minDist := entry * 0.003
			if slDist < minDist { slDist = minDist }
			sl = entry - slDist
		}
		tp := lp.TakeProfit
		if tp == 0 { tp = entry + entry*s.cfg.RangeTPPct }

		s.longPos = &posState{
			side: "LONG", mode: posMode(0), entryPrice: entry,
			initQty: lp.InitQty, remainQty: lp.Qty,
			R: lp.R, stopLoss: sl, takeProfit: tp,
			trailing: lp.Trailing, peakPrice: lp.PeakPrice,
			tp1RHit: lp.TP1Hit, barsHeld: max(lp.BarsHeld, 10), filled: true,
		}
		if s.longPos.initQty == 0 { s.longPos.initQty = lp.Qty }
		if s.longPos.R == 0 { s.longPos.R = math.Abs(entry - sl) }
		if s.longPos.peakPrice == 0 { s.longPos.peakPrice = currentPrice }
		if s.longPos.trailing == 0 { s.longPos.trailing = sl }
		if lp.Mode == "trend" { s.longPos.mode = modeTrend }

		s.log.Info("AI: recovered LONG from syncer",
			zap.Float64("entry", entry), zap.Float64("qty", lp.Qty),
			zap.Float64("stop", sl))
	}

	// Recover SHORT
	if sp := s.syncer.GetShort(); sp != nil && sp.Qty > 0 {
		entry := sp.EntryPrice
		if entry == 0 { entry = currentPrice }

		sl := sp.StopLoss
		if sl == 0 {
			slDist := atr * s.cfg.ATRK
			minDist := entry * 0.003
			if slDist < minDist { slDist = minDist }
			sl = entry + slDist
		}
		tp := sp.TakeProfit
		if tp == 0 { tp = entry - entry*s.cfg.RangeTPPct }

		s.shortPos = &posState{
			side: "SHORT", mode: posMode(0), entryPrice: entry,
			initQty: sp.InitQty, remainQty: sp.Qty,
			R: sp.R, stopLoss: sl, takeProfit: tp,
			trailing: sp.Trailing, peakPrice: sp.PeakPrice,
			tp1RHit: sp.TP1Hit, barsHeld: max(sp.BarsHeld, 10), filled: true,
		}
		if s.shortPos.initQty == 0 { s.shortPos.initQty = sp.Qty }
		if s.shortPos.R == 0 { s.shortPos.R = math.Abs(entry - sl) }
		if s.shortPos.peakPrice == 0 { s.shortPos.peakPrice = currentPrice }
		if s.shortPos.trailing == 0 { s.shortPos.trailing = sl }
		if sp.Mode == "trend" { s.shortPos.mode = modeTrend }

		s.log.Info("AI: recovered SHORT from syncer",
			zap.Float64("entry", entry), zap.Float64("qty", sp.Qty),
			zap.Float64("stop", sl))
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
		R: pos.R, InitQty: pos.initQty,
		TP1Hit: pos.tp1RHit, BarsHeld: pos.barsHeld,
		OrderID: pos.orderID, Filled: pos.filled,
	}
	s.syncer.UpdatePosition(context.Background(), sp)
}

// syncRemove clears a position from Syncer.
func (s *AIStrategy) syncRemove(side string) {
	if s.syncer == nil { return }
	s.syncer.RemovePosition(context.Background(), side)
}

func (s *AIStrategy) findSwingLow(n int) float64 {
	if len(s.bars) < n { n = len(s.bars) }
	low := math.MaxFloat64
	for i := len(s.bars) - n; i < len(s.bars); i++ { if s.bars[i].Low < low { low = s.bars[i].Low } }
	return low
}
func (s *AIStrategy) findSwingHigh(n int) float64 {
	high := 0.0; start := len(s.bars) - n; if start < 0 { start = 0 }
	for i := start; i < len(s.bars); i++ { if s.bars[i].High > high { high = s.bars[i].High } }
	return high
}

func r2(v float64) float64 { return math.Round(v*100) / 100 }
func r3(v float64) float64 { return math.Round(v*1000) / 1000 }
func toFloat(v any) float64 { switch n := v.(type) { case float64: return n; case int: return float64(n); case int64: return float64(n) }; return 0 }
func toInt(v any) int { switch n := v.(type) { case float64: return int(n); case int: return n; case int64: return int(n) }; return 0 }
