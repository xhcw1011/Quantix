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
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
	"github.com/redis/go-redis/v9"
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
		if v, ok := params["GridMaxLayers"]; ok { cfg.GridMaxLayers = toInt(v) }
		if v, ok := params["GridSpacingPct"]; ok { cfg.GridSpacingPct = toFloat(v) }
		if v, ok := params["GridTPPct"]; ok { cfg.GridTPPct = toFloat(v) }
		if v, ok := params["GridQtyRatio"]; ok { cfg.GridQtyRatio = toFloat(v) }
		if v, ok := params["TPLevels"]; ok {
			switch vv := v.(type) {
			case []float64: cfg.TPLevels = vv
			case []any:
				var sl []float64
				for _, item := range vv { if f, ok := item.(float64); ok { sl = append(sl, f) } }
				if len(sl) > 0 { cfg.TPLevels = sl }
			}
		}
		if v, ok := params["TPQtySplits"]; ok {
			switch vv := v.(type) {
			case []float64: cfg.TPQtySplits = vv
			case []any:
				var sl []float64
				for _, item := range vv { if f, ok := item.(float64); ok { sl = append(sl, f) } }
				if len(sl) > 0 { cfg.TPQtySplits = sl }
			}
		}
		if v, ok := params["BreakevenR"]; ok { cfg.BreakevenR = toFloat(v) }
		if v, ok := params["BreakevenBuf"]; ok { cfg.BreakevenBuf = toFloat(v) }
		if v, ok := params["TrailBasePct"]; ok { cfg.TrailBasePct = toFloat(v) }
		if v, ok := params["TrailLowVolPct"]; ok { cfg.TrailLowVolPct = toFloat(v) }
		if v, ok := params["TrailHighVolPct"]; ok { cfg.TrailHighVolPct = toFloat(v) }
		if v, ok := params["TrailFloorPct"]; ok { cfg.TrailFloorPct = toFloat(v) }
		if v, ok := params["MinSLDistPct"]; ok { cfg.MinSLDistPct = toFloat(v) }
		if v, ok := params["ReversalConf"]; ok { cfg.ReversalConf = toFloat(v) }
		if v, ok := params["MarketEntryConf"]; ok { cfg.MarketEntryConf = toFloat(v) }
		if v, ok := params["RangeBEPct"]; ok { cfg.RangeBEPct = toFloat(v) }
		if v, ok := params["RangeLockPct"]; ok { cfg.RangeLockPct = toFloat(v) }
		if v, ok := params["RangeLockOffset"]; ok { cfg.RangeLockOffset = toFloat(v) }
		if v, ok := params["RangeTrailPct"]; ok { cfg.RangeTrailPct = toFloat(v) }
		if v, ok := params["RangeTrailDist"]; ok { cfg.RangeTrailDist = toFloat(v) }
		if v, ok := params["RangeProfitTimeout"]; ok { cfg.RangeProfitTimeout = time.Duration(toFloat(v)) * time.Minute }
		if v, ok := params["RangeLossTimeout"]; ok { cfg.RangeLossTimeout = time.Duration(toFloat(v)) * time.Minute }
		if v, ok := params["RangeFlatTimeout"]; ok { cfg.RangeFlatTimeout = time.Duration(toFloat(v)) * time.Minute }
		if v, ok := params["BBWidthMin"]; ok { cfg.BBWidthMin = toFloat(v) }
		if v, ok := params["BBWidthMax"]; ok { cfg.BBWidthMax = toFloat(v) }
		if v, ok := params["RangeEMAConv"]; ok { cfg.RangeEMAConv = toFloat(v) }
		if v, ok := params["MTFStrongTrend"]; ok { cfg.MTFStrongTrend = toFloat(v) }
		if v, ok := params["MTFWeakTrend"]; ok { cfg.MTFWeakTrend = toFloat(v) }
		if v, ok := params["MTFBullRSI"]; ok { cfg.MTFBullRSI = toFloat(v) }
		if v, ok := params["MTFBearRSI"]; ok { cfg.MTFBearRSI = toFloat(v) }
		if v, ok := params["MTF1mThreshold"]; ok { cfg.MTF1mThreshold = toFloat(v) }
		if v, ok := params["MTFQtyScaleHard"]; ok { cfg.MTFQtyScaleHard = toFloat(v) }
		if v, ok := params["MTFQtyScaleSoft"]; ok { cfg.MTFQtyScaleSoft = toFloat(v) }
		if v, ok := params["SwingProximity"]; ok { cfg.SwingProximity = toFloat(v) }
		if v, ok := params["RSIPeriod"]; ok { cfg.RSIPeriod = toInt(v) }
		if v, ok := params["MACDFast"]; ok { cfg.MACDFast = toInt(v) }
		if v, ok := params["MACDSlow"]; ok { cfg.MACDSlow = toInt(v) }
		if v, ok := params["MACDSignal"]; ok { cfg.MACDSignal = toInt(v) }
		if v, ok := params["EMAFast"]; ok { cfg.EMAFast = toInt(v) }
		if v, ok := params["EMASlow"]; ok { cfg.EMASlow = toInt(v) }
		if v, ok := params["BBPeriod"]; ok { cfg.BBPeriod = toInt(v) }
		if v, ok := params["BBStdDev"]; ok { cfg.BBStdDev = toFloat(v) }
		if v, ok := params["ATRPeriod"]; ok { cfg.ATRPeriod = toInt(v) }
		if v, ok := params["VolMAPeriod"]; ok { cfg.VolMAPeriod = toInt(v) }
		if v, ok := params["EntryOffsetPct"]; ok { cfg.EntryOffsetPct = toFloat(v) }
		if v, ok := params["MaxEntryDevPct"]; ok { cfg.MaxEntryDevPct = toFloat(v) }
		if v, ok := params["LimitTimeoutBars"]; ok { cfg.LimitTimeoutBars = toInt(v) }
		if v, ok := params["MinHoldBars"]; ok { cfg.MinHoldBars = toInt(v) }
		if v, ok := params["MinTrendBars"]; ok { cfg.MinTrendBars = toInt(v) }
		if v, ok := params["GPTTemperature"]; ok { cfg.GPTTemperature = toFloat(v) }
		if v, ok := params["GPTMaxTokens"]; ok { cfg.GPTMaxTokens = toInt(v) }
		if v, ok := params["GPTTimeout"]; ok { cfg.GPTTimeout = time.Duration(toFloat(v)) * time.Second }
		if v, ok := params["Interval"].(string); ok && cfg.PrimaryInterval == "" { cfg.PrimaryInterval = v }
		if v, ok := params["Intervals"]; ok {
			switch vv := v.(type) {
			case []string:
				cfg.Intervals = vv
			case []any:
				for _, item := range vv { if s, ok := item.(string); ok { cfg.Intervals = append(cfg.Intervals, s) } }
			}
		}
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

	// Multi-timeframe
	PrimaryInterval string   // "5m" — drives GPT signals + entries
	Intervals       []string // all subscribed intervals, e.g. ["1m","5m","15m"]

	// Trend mode
	RiskPerTrade float64 // 1% of equity per trade
	ATRK         float64 // stop-loss ATR multiplier
	TrailingATRK float64 // trailing ATR multiplier

	// Range/scalp mode (percentage of entry price)
	RangeTPPct float64 // take-profit % (default 0.004 = 0.4%)
	RangeSLPct float64 // stop-loss % (default 0.0025 = 0.25%)

	// Grid mode (range only)
	GridMaxLayers  int     // max grid orders per position (default 2)
	GridSpacingPct float64 // spacing between grid levels (default 0.005 = 0.5%)
	GridTPPct      float64 // grid order take-profit (default 0.004 = 0.4%)
	GridQtyRatio   float64 // grid qty as ratio of base qty (default 0.5)

	// Staged TP (trend mode) — exchange-native limit orders
	TPLevels     []float64 // R-multiples for each TP level (default [1.0, 1.5, 2.5, 4.0])
	TPQtySplits  []float64 // fraction of qty for each level (default [0.40, 0.30, 0.20, 0.10])
	BreakevenR   float64   // R threshold to move SL to breakeven (default 0.5)
	BreakevenBuf float64   // buffer above/below entry for breakeven SL (default 0.001 = 0.1%)

	// Trailing stop (trend fallback when staged orders unavailable)
	TrailBasePct    float64 // base trailing % (default 0.012 = 1.2%)
	TrailLowVolPct  float64 // trailing % for low volatility (default 0.008)
	TrailHighVolPct float64 // trailing % for high volatility (default 0.015)
	TrailFloorPct   float64 // absolute minimum trailing distance % (default 0.005)
	MinSLDistPct    float64 // minimum SL distance from entry (default 0.008 = 0.8%)
	ReversalConf    float64 // confidence threshold for GPT reversal exit (default 0.72)
	MarketEntryConf float64 // confidence threshold for immediate market entry (default 0.90)

	// Range position management
	RangeBEPct         float64       // PnL % to move SL to breakeven (default 0.003)
	RangeLockPct       float64       // PnL % to lock in partial profit (default 0.006)
	RangeLockOffset    float64       // profit lock offset % (default 0.003)
	RangeTrailPct      float64       // PnL % to start trailing (default 0.008)
	RangeTrailDist     float64       // trailing distance % (default 0.003)
	RangeProfitTimeout time.Duration // timeout for profitable range pos (default 60m)
	RangeLossTimeout   time.Duration // timeout for losing range pos (default 20m)
	RangeFlatTimeout   time.Duration // timeout for flat range pos (default 30m)
	BBWidthMin         float64       // min BB width for range TP (default 0.006)
	BBWidthMax         float64       // max BB width for range TP (default 0.015)
	RangeEMAConv       float64       // EMA convergence threshold for regime detection (default 0.003)

	// MTF scoring
	MTFStrongTrend  float64 // 15m return threshold for strong trend (default 0.01)
	MTFWeakTrend    float64 // 15m return threshold for weak trend (default 0.002)
	MTFBullRSI      float64 // RSI threshold for bullish signal (default 60)
	MTFBearRSI      float64 // RSI threshold for bearish signal (default 40)
	MTF1mThreshold  float64 // 1m return threshold (default 0.001)
	MTFQtyScaleHard float64 // qty scale for strong headwind (default 0.70)
	MTFQtyScaleSoft float64 // qty scale for mild headwind (default 0.85)
	SwingProximity  float64 // swing high/low proximity % (default 0.0015)

	// Technical indicator periods
	RSIPeriod   int     // RSI lookback (default 14)
	MACDFast    int     // MACD fast EMA (default 12)
	MACDSlow    int     // MACD slow EMA (default 26)
	MACDSignal  int     // MACD signal line (default 9)
	EMAFast     int     // fast EMA period (default 20)
	EMASlow     int     // slow EMA period (default 50)
	BBPeriod    int     // Bollinger Bands period (default 20)
	BBStdDev    float64 // BB standard deviation multiplier (default 2.0)
	ATRPeriod   int     // ATR lookback for position sizing (default 60)
	VolMAPeriod int     // Volume MA period (default 20)

	// Entry/exit tuning
	EntryOffsetPct   float64       // limit entry offset from current price (default 0.0013)
	MaxEntryDevPct   float64       // max GPT entry deviation from spot (default 0.005)
	LimitTimeoutBars int           // bars to wait for limit fill (default 2)
	MinHoldBars      int           // minimum bars before TP/SL checks (default 3)
	MinTrendBars     int           // minimum bars before trend management (default 5)

	// GPT tuning
	GPTTemperature float64       // GPT temperature (default 0.3)
	GPTMaxTokens   int           // GPT max completion tokens (default 400)
	GPTTimeout     time.Duration // GPT API call timeout (default 15s)

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
		GridMaxLayers: 2, GridSpacingPct: 0.005, GridTPPct: 0.004, GridQtyRatio: 0.5,
		TPLevels: []float64{1.0, 1.5, 2.5, 4.0},
		TPQtySplits: []float64{0.40, 0.30, 0.20, 0.10},
		BreakevenR: 0.5, BreakevenBuf: 0.001,
		TrailBasePct: 0.012, TrailLowVolPct: 0.008, TrailHighVolPct: 0.015,
		TrailFloorPct: 0.005, MinSLDistPct: 0.008,
		ReversalConf: 0.72, MarketEntryConf: 0.90,
		RangeBEPct: 0.003, RangeLockPct: 0.006, RangeLockOffset: 0.003,
		RangeTrailPct: 0.008, RangeTrailDist: 0.003,
		RangeProfitTimeout: 60 * time.Minute, RangeLossTimeout: 20 * time.Minute, RangeFlatTimeout: 30 * time.Minute,
		BBWidthMin: 0.006, BBWidthMax: 0.015, RangeEMAConv: 0.003,
		MTFStrongTrend: 0.01, MTFWeakTrend: 0.002,
		MTFBullRSI: 60, MTFBearRSI: 40, MTF1mThreshold: 0.001,
		MTFQtyScaleHard: 0.70, MTFQtyScaleSoft: 0.85, SwingProximity: 0.0015,
		RSIPeriod: 14, MACDFast: 12, MACDSlow: 26, MACDSignal: 9,
		EMAFast: 20, EMASlow: 50, BBPeriod: 20, BBStdDev: 2.0,
		ATRPeriod: 60, VolMAPeriod: 20,
		EntryOffsetPct: 0.0013, MaxEntryDevPct: 0.005,
		LimitTimeoutBars: 2, MinHoldBars: 3, MinTrendBars: 5,
		GPTTemperature: 0.3, GPTMaxTokens: 400, GPTTimeout: 15 * time.Second,
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
	filledAt   time.Time
	orderID    string
	limitBar   int

	// Staged TP (trend mode): exchange-native limit orders
	stagedTPPlaced bool // true once SL + TP orders are on the exchange
	breakevenMoved bool // true once SL has been moved to breakeven at +0.5R

	// Grid orders (range mode only)
	gridOrders []*gridOrder
}

type gridOrder struct {
	entryPrice float64
	qty        float64
	tp         float64 // take-profit price
	filled     bool
	filledAt   time.Time
	orderID    string
	limitBar   int
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
	cooldownUntil  int // bar index — no new entries until barCount >= this
	stopBar        int // bar index when last stop-loss fired — skip opening same bar
	lastMTFScore    int     // multi-timeframe score from latest signal check
	mtfLongScale    float64 // position size multiplier for LONG (0.7-1.0)
	mtfShortScale   float64 // position size multiplier for SHORT (0.7-1.0)
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

	// Manage positions on primary bar too
	if s.longPos != nil { s.managePos(ctx, bar, s.longPos, &s.longPos) }
	if s.shortPos != nil { s.managePos(ctx, bar, s.shortPos, &s.shortPos) }

	// Track if we have pending orders (for post-GPT cancel logic)
	hasPendingLong := s.longPos != nil && !s.longPos.filled
	hasPendingShort := s.shortPos != nil && !s.shortPos.filled

	// Don't open new positions on the same bar as a stop-loss
	if s.stopBar == s.barCount { return }

	// GPT signal check (every N primary bars)
	interval := s.cfg.CallIntervalBars
	if interval < 1 { interval = 1 }
	if s.barCount-s.lastCallBar < interval { return }

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
	var ema20_15, ema50_15 float64 // used below for structure confirmation
	if len(bars15) >= 8 {
		c15 := make([]float64, len(bars15))
		for i, b := range bars15 { c15[i] = b.Close }
		ret15 := (c15[len(c15)-1] - c15[len(c15)-8]) / c15[len(c15)-8]
		if ret15 > s.cfg.MTFStrongTrend { mtfScore += 2 } else if ret15 > s.cfg.MTFWeakTrend { mtfScore += 1 }
		if ret15 < -s.cfg.MTFStrongTrend { mtfScore -= 2 } else if ret15 < -s.cfg.MTFWeakTrend { mtfScore -= 1 }

		// 15m EMA structure confirmation (±1): prevents bounces from flipping the score.
		// If EMA20 < EMA50, the macro trend is bearish regardless of short-term return.
		if len(c15) >= s.cfg.EMASlow {
			ema20_15 = indicator.Last(indicator.EMA(c15, s.cfg.EMAFast))
			ema50_15 = indicator.Last(indicator.EMA(c15, s.cfg.EMASlow))
			if ema20_15 > ema50_15 { mtfScore++ }  // bullish structure
			if ema20_15 < ema50_15 { mtfScore-- }  // bearish structure
		}
	}

	// 5m momentum score (±1): MACD OR RSI, either is enough
	closes5m := s.getCloses()
	if len(closes5m) >= 14 {
		rsi5m := indicator.Last(indicator.RSI(closes5m, s.cfg.RSIPeriod))
		macd5m := indicator.MACD(closes5m, s.cfg.MACDFast, s.cfg.MACDSlow, s.cfg.MACDSignal)
		macdHist5m := indicator.Last(macd5m.Histogram)
		if macdHist5m > 0 || rsi5m > s.cfg.MTFBullRSI { mtfScore++ }
		if macdHist5m < 0 || rsi5m < s.cfg.MTFBearRSI { mtfScore-- }
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
	if price > 0 && swLow > 0 && (price-swLow)/price < s.cfg.SwingProximity && longConf < 0.82 && s.longPos == nil && mtfScore >= -1 {
		s.log.Info("AI: boost long — price near swing low",
			zap.Float64("price", price), zap.Float64("swing_low", swLow), zap.Int("mtf", mtfScore))
		longConf = 0.82
		if longEntry <= 0 { longEntry = swLow }
	}
	if price > 0 && swHigh > 0 && (swHigh-price)/price < s.cfg.SwingProximity && shortConf < 0.82 && s.shortPos == nil && mtfScore <= 1 {
		s.log.Info("AI: boost short — price near swing high",
			zap.Float64("price", price), zap.Float64("swing_high", swHigh), zap.Int("mtf", mtfScore))
		shortConf = 0.82
		if shortEntry <= 0 { shortEntry = swHigh }
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
	if !s.cfg.HedgeMode {
		if longConf >= s.cfg.ConfidenceThreshold && shortConf >= s.cfg.ConfidenceThreshold {
			if longConf >= shortConf {
				shortConf = 0
			} else {
				longConf = 0
			}
		}
		if s.longPos != nil && shortConf >= s.cfg.ConfidenceThreshold {
			shortConf = 0
		}
		if s.shortPos != nil && longConf >= s.cfg.ConfidenceThreshold {
			longConf = 0
		}
	}

	// Entry: pick the better of GPT price vs 0.10% offset
	// LONG: lower is better; SHORT: higher is better
	entryOffset := price * s.cfg.EntryOffsetPct
	maxDev := price * s.cfg.MaxEntryDevPct // cap GPT entry within configured % of current price

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
			entry = math.Round(entry*100) / 100
			if isRange {
				s.openRange(ctx, "LONG", price, entry, atr)
			} else {
				s.openTrend(ctx, "LONG", price, entry, atr)
			}
			// GPT entry as grid add-on if significantly better than actual entry
			if s.longPos != nil && s.longPos.filled && longEntry > 0 && longEntry < entry && (entry-longEntry)/entry > 0.002 {
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
			entry = math.Round(entry*100) / 100
			if isRange {
				s.openRange(ctx, "SHORT", price, entry, atr)
			} else {
				s.openTrend(ctx, "SHORT", price, entry, atr)
			}
			// GPT entry as grid add-on if significantly better than actual entry
			if s.shortPos != nil && s.shortPos.filled && shortEntry > 0 && shortEntry > entry && (shortEntry-entry)/entry > 0.002 {
				s.addGPTGrid(s.shortPos, "SHORT", shortEntry)
			}
		}
	}
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

// placeStagedExitOrders places exchange-native SL + 4 staged TP limit orders for trend mode.
//
// TP plan (based on R = |entry - stopLoss|):
//   +1.0R → close 40%  (recover ~2x risk → "free position")
//   +1.5R → close 30%  (lock profit, 70% total closed)
//   +2.5R → close 20%  (trend confirmed)
//   +4.0R → close 10%  ("lottery ticket" — surprise if it runs)
//
// +0.5R breakeven SL move is handled separately in OnTick.
func (s *AIStrategy) placeStagedExitOrders(ctx *strategy.Context, pos *posState) {
	ep, ok := ctx.Extra["staged_exit"].(strategy.StagedExitPlacer)
	if !ok {
		s.log.Warn("staged exit placer not available (paper/backtest mode), using local management")
		return
	}
	s.stagedEP = ep // cache for handleStagedTPFill (no ctx available there)

	R := pos.R
	if R <= 0 { return }
	entry := pos.entryPrice
	qty := pos.initQty

	// Determine close side
	closeSide := "SELL"
	posSide := "LONG"
	if pos.side == "SHORT" {
		closeSide = "BUY"
		posSide = "SHORT"
	}

	levels := s.cfg.TPLevels
	splits := s.cfg.TPQtySplits
	if len(levels) == 0 || len(splits) == 0 || len(levels) != len(splits) {
		s.log.Error("staged TP: invalid TPLevels/TPQtySplits config")
		return
	}

	tps := make([]strategy.StagedTP, 0, len(levels))
	usedQty := 0.0
	for i, lvl := range levels {
		var tpPrice float64
		if pos.side == "LONG" {
			tpPrice = math.Round((entry+lvl*R)*100) / 100
		} else {
			tpPrice = math.Round((entry-lvl*R)*100) / 100
		}
		var q float64
		if i == len(levels)-1 {
			q = qty - usedQty
			q = math.Floor(q*1000) / 1000
			if q <= 0 { q = 0.001 }
		} else {
			q = math.Floor(qty*splits[i]*1000) / 1000
		}
		usedQty += q
		tps = append(tps, strategy.StagedTP{Price: tpPrice, Qty: q})
	}

	ok = ep.PlaceStagedTPOrders(s.cfg.Symbol, posSide, closeSide, pos.stopLoss, qty, tps)
	if ok {
		pos.stagedTPPlaced = true
		s.log.Info("AI: staged TP orders placed on exchange",
			zap.String("side", pos.side),
			zap.Float64("entry", entry), zap.Float64("R", R),
			zap.Float64("sl", pos.stopLoss),
			zap.Any("levels", levels), zap.Any("splits", splits),
		)
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

// checkBreakevenMove moves the SL to breakeven when price reaches +0.5R.
// This is the only tick-level action for trend mode; everything else is on the exchange.
func (s *AIStrategy) checkBreakevenMove(ctx *strategy.Context, price float64, p *posState) {
	if p.breakevenMoved || p.R <= 0 { return }

	pnlR := 0.0
	if p.side == "LONG" { pnlR = (price - p.entryPrice) / p.R }
	if p.side == "SHORT" { pnlR = (p.entryPrice - price) / p.R }

	if pnlR < s.cfg.BreakevenR { return }

	// Price has reached +0.5R — move SL to breakeven (+0.1% buffer above/below entry)
	ep, ok := ctx.Extra["staged_exit"].(strategy.StagedExitPlacer)
	if !ok { return }

	closeSide := "SELL"
	posSide := "LONG"
	if p.side == "SHORT" {
		closeSide = "BUY"
		posSide = "SHORT"
	}

	var newStop float64
	if p.side == "LONG" {
		newStop = math.Round((p.entryPrice + p.entryPrice*s.cfg.BreakevenBuf)*100) / 100
	} else {
		newStop = math.Round((p.entryPrice - p.entryPrice*s.cfg.BreakevenBuf)*100) / 100
	}

	if ep.ReplaceSLOrder(s.cfg.Symbol, posSide, closeSide, p.remainQty, newStop) {
		p.breakevenMoved = true
		p.stopLoss = newStop
		s.log.Info("AI: SL moved to breakeven at +0.5R",
			zap.String("side", p.side),
			zap.Float64("price", price),
			zap.Float64("new_stop", newStop),
			zap.Float64("pnl_r", pnlR),
		)
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

// ─── Regime Detection ────────────────────────────────────────────────────────

func (s *AIStrategy) isRangeRegime(price float64) bool {
	closes := s.getCloses()
	if len(closes) < 50 { return true }
	ema20 := indicator.Last(indicator.EMA(closes, s.cfg.EMAFast))
	ema50 := indicator.Last(indicator.EMA(closes, s.cfg.EMASlow))
	if price > 0 && math.Abs(ema20-ema50)/price < s.cfg.RangeEMAConv {
		return true // EMAs converged = range
	}
	return false
}

// effectiveRisk returns risk-per-trade based on current exposure.
// Single direction: 2x configured risk (e.g. 4%); dual hedge: 1x (e.g. 2%).
func (s *AIStrategy) effectiveRisk(side string) float64 {
	hasOpposite := false
	if side == "LONG" && s.shortPos != nil && s.shortPos.filled { hasOpposite = true }
	if side == "SHORT" && s.longPos != nil && s.longPos.filled { hasOpposite = true }
	if hasOpposite {
		return s.cfg.RiskPerTrade // hedge mode: use base risk (2%)
	}
	return s.cfg.RiskPerTrade * 2 // single direction: double risk (4%)
}

// addGPTGrid adds the GPT-suggested support/resistance price as a grid order for future fill.
func (s *AIStrategy) addGPTGrid(pos *posState, side string, gptEntry float64) {
	gridQty := math.Floor(pos.initQty*s.cfg.GridQtyRatio*1000) / 1000
	if gridQty <= 0 { return }
	// Cap: total qty must not exceed 2x initial
	if pos.remainQty+gridQty > pos.initQty*2 { return }

	var gridTP float64
	if side == "LONG" {
		gridTP = math.Round((gptEntry+gptEntry*s.cfg.GridTPPct)*100) / 100
	} else {
		gridTP = math.Round((gptEntry-gptEntry*s.cfg.GridTPPct)*100) / 100
	}

	g := &gridOrder{
		entryPrice: gptEntry, qty: gridQty, tp: gridTP,
		filled: false, limitBar: s.barCount,
	}
	pos.gridOrders = append(pos.gridOrders, g)
	s.log.Info("AI: GPT entry as grid add-on",
		zap.String("side", side), zap.Float64("gpt_entry", gptEntry),
		zap.Float64("grid_qty", gridQty), zap.Float64("grid_tp", gridTP))
}

// ─── Open Position ───────────────────────────────────────────────────────────

func (s *AIStrategy) openRange(ctx *strategy.Context, side string, currentPrice, entryPrice, atr float64) {
	entryPrice = math.Round(entryPrice*100) / 100

	// Dynamic TP based on Bollinger Band width (recent volatility)
	// Use 60% of BB width as TP target, clamped between 0.6% and 1.5%
	tpPct := s.cfg.RangeTPPct
	closes := s.getCloses()
	if len(closes) >= 20 {
		bb := indicator.BollingerBands(closes, s.cfg.BBPeriod, s.cfg.BBStdDev)
		bbU, bbL := indicator.Last(bb.Upper), indicator.Last(bb.Lower)
		if bbU > bbL && currentPrice > 0 {
			bbWidthPct := (bbU - bbL) / currentPrice * 0.6 // 60% of BB width
			if bbWidthPct < s.cfg.BBWidthMin { bbWidthPct = s.cfg.BBWidthMin }
			if bbWidthPct > s.cfg.BBWidthMax { bbWidthPct = s.cfg.BBWidthMax }
			tpPct = bbWidthPct
		}
	}
	tpDist := entryPrice * tpPct
	slDist := entryPrice * s.cfg.RangeSLPct

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
	risk := s.effectiveRisk(side)
	qty := math.Floor(equity*risk/slDist*1000) / 1000
	mtfScale := s.mtfLongScale; if side == "SHORT" { mtfScale = s.mtfShortScale }
	if mtfScale > 0 && mtfScale < 1.0 { qty = math.Floor(qty*mtfScale*1000) / 1000 }
	if qty <= 0 { return }

	useLimit := math.Abs(entryPrice-currentPrice) > 0.01
	omsID := s.placeOrder(ctx, side, entryPrice, qty, useLimit)
	if omsID == "" { return }

	filledAt := time.Time{}
	if !useLimit { filledAt = time.Now() }
	pos := &posState{
		side: side, mode: modeRange, entryPrice: entryPrice,
		initQty: qty, remainQty: qty,
		R: slDist, stopLoss: stopLoss, takeProfit: takeProfit,
		trailing: stopLoss, peakPrice: entryPrice,
		filled: !useLimit, filledAt: filledAt, orderID: omsID, limitBar: s.barCount,
	}
	if side == "LONG" { s.longPos = pos } else { s.shortPos = pos }

	s.log.Info("AI: OPEN RANGE",
		zap.String("side", side), zap.Float64("entry", entryPrice),
		zap.Float64("tp", takeProfit), zap.Float64("sl", stopLoss),
		zap.Float64("qty", qty))
	s.logEvent("open", side, "range", currentPrice, entryPrice, qty, 0, 0,
		fmt.Sprintf(`{"tp":%.2f,"sl":%.2f}`, takeProfit, stopLoss))
	s.syncToRedis(pos)
}

func (s *AIStrategy) openTrend(ctx *strategy.Context, side string, currentPrice, entryPrice, atr float64) {
	entryPrice = math.Round(entryPrice*100) / 100
	minDist := entryPrice * s.cfg.MinSLDistPct
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
	risk := s.effectiveRisk(side)
	qty := math.Floor(equity*risk/R*1000) / 1000
	mtfScale := s.mtfLongScale; if side == "SHORT" { mtfScale = s.mtfShortScale }
	if mtfScale > 0 && mtfScale < 1.0 { qty = math.Floor(qty*mtfScale*1000) / 1000 }
	if qty <= 0 { return }

	useLimit := math.Abs(entryPrice-currentPrice) > 0.01
	omsID := s.placeOrder(ctx, side, entryPrice, qty, useLimit)
	if omsID == "" { return }

	filledAt := time.Time{}
	if !useLimit { filledAt = time.Now() }
	pos := &posState{
		side: side, mode: modeTrend, entryPrice: entryPrice,
		initQty: qty, remainQty: qty,
		R: R, stopLoss: stopLoss, trailing: stopLoss, peakPrice: entryPrice,
		filled: !useLimit, filledAt: filledAt, orderID: omsID, limitBar: s.barCount,
	}
	if side == "LONG" { s.longPos = pos } else { s.shortPos = pos }

	s.log.Info("AI: OPEN TREND",
		zap.String("side", side), zap.Float64("entry", entryPrice),
		zap.Float64("sl", stopLoss), zap.Float64("R", R), zap.Float64("qty", qty))
	s.logEvent("open", side, "trend", currentPrice, entryPrice, qty, 0, 0,
		fmt.Sprintf(`{"sl":%.2f,"R":%.2f}`, stopLoss, R))
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

// placeCloseOrder places a close order. Uses limit order (maker fee) unless useMarket is true.
// Limit close price: sell at current+$0.01 (LONG), buy at current-$0.01 (SHORT) to get maker fee.
func (s *AIStrategy) placeCloseOrder(ctx *strategy.Context, side string, qty float64, useMarket bool) {
	closeSide := strategy.SideSell
	psSide := strategy.PositionSideLong
	if side == "SHORT" {
		closeSide = strategy.SideBuy
		psSide = strategy.PositionSideShort
	}
	req := strategy.OrderRequest{
		Symbol: s.cfg.Symbol, Side: closeSide, PositionSide: psSide, Qty: qty,
	}
	if !useMarket {
		// Get current price from latest primary bar
		bars := s.primaryBars()
		if len(bars) > 0 {
			lastPrice := bars[len(bars)-1].Close
			if side == "LONG" {
				req.Price = math.Round((lastPrice+0.01)*100) / 100 // sell slightly above
			} else {
				req.Price = math.Round((lastPrice-0.01)*100) / 100 // buy slightly below
			}
			req.Type = strategy.OrderLimit
		}
	}
	ctx.PlaceOrder(req)
}

// ─── Position Management (every bar) ─────────────────────────────────────────

func (s *AIStrategy) managePos(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	price := bar.Close
	iv := bar.Interval; if iv == "" { iv = s.cfg.PrimaryInterval }
	isPrimary := iv == s.cfg.PrimaryInterval
	if isPrimary { p.barsHeld++ }

	// Limit order pending (check on primary bars only)
	if !p.filled {
		if isPrimary && s.barCount-p.limitBar > s.cfg.LimitTimeoutBars {
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

	// ── Stop-loss (skip for trend with staged orders — exchange handles it) ──
	if !(p.mode == modeTrend && p.stagedTPPlaced) {
		if (p.side == "LONG" && price <= p.stopLoss) || (p.side == "SHORT" && price >= p.stopLoss) {
			s.log.Warn("STOP-LOSS", zap.String("side", p.side), zap.Float64("price", price), zap.Float64("stop", p.stopLoss))
			s.closePos(ctx, p, pptr, "stop_loss")
			s.consecLoss++
			s.stopBar = s.barCount
			s.log.Info("AI: stop-loss hit")
			return
		}
	}

	if p.barsHeld < s.cfg.MinHoldBars { return } // minimum hold

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
		rsi := indicator.Last(indicator.RSI(closes, s.cfg.RSIPeriod))
		macd := indicator.MACD(closes, s.cfg.MACDFast, s.cfg.MACDSlow, s.cfg.MACDSignal)
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
			buf := p.entryPrice * s.cfg.BreakevenBuf
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

	// ── Range trailing: protect profits ──
	rangePnlPct := 0.0
	if p.side == "LONG" { rangePnlPct = (price - p.entryPrice) / p.entryPrice }
	if p.side == "SHORT" { rangePnlPct = (p.entryPrice - price) / p.entryPrice }

	// +0.3%: move SL to breakeven
	if rangePnlPct >= s.cfg.RangeBEPct && p.side == "LONG" && p.stopLoss < p.entryPrice {
		p.stopLoss = p.entryPrice + p.entryPrice*s.cfg.BreakevenBuf
		s.log.Info("AI: Range +0.3% → SL to breakeven", zap.Float64("sl", p.stopLoss))
	}
	if rangePnlPct >= s.cfg.RangeBEPct && p.side == "SHORT" && p.stopLoss > p.entryPrice {
		p.stopLoss = p.entryPrice - p.entryPrice*s.cfg.BreakevenBuf
		s.log.Info("AI: Range +0.3% → SL to breakeven", zap.Float64("sl", p.stopLoss))
	}
	// +0.6%: lock in +0.3% profit
	if rangePnlPct >= s.cfg.RangeLockPct {
		lockPrice := 0.0
		if p.side == "LONG" {
			lockPrice = p.entryPrice + p.entryPrice*s.cfg.RangeLockOffset
			if p.stopLoss < lockPrice { p.stopLoss = lockPrice }
		} else {
			lockPrice = p.entryPrice - p.entryPrice*s.cfg.RangeLockOffset
			if p.stopLoss > lockPrice { p.stopLoss = lockPrice }
		}
	}
	// +0.8%: trailing 0.3% from peak
	if rangePnlPct >= s.cfg.RangeTrailPct {
		trailDist := p.peakPrice * s.cfg.RangeTrailDist
		if p.side == "LONG" {
			nt := p.peakPrice - trailDist
			if nt > p.stopLoss { p.stopLoss = nt }
			if price <= p.stopLoss {
				s.log.Info("RANGE TRAILING", zap.Float64("price", price), zap.Float64("sl", p.stopLoss))
				s.closePos(ctx, p, pptr, "range_trailing")
				s.consecLoss = 0
				return
			}
		} else {
			nt := p.peakPrice + trailDist
			if nt < p.stopLoss { p.stopLoss = nt }
			if price >= p.stopLoss {
				s.log.Info("RANGE TRAILING", zap.Float64("price", price), zap.Float64("sl", p.stopLoss))
				s.closePos(ctx, p, pptr, "range_trailing")
				s.consecLoss = 0
				return
			}
		}
	}

	// ── Smart timeout (time-based, independent of bar interval) ──
	pnlPct := 0.0
	if p.side == "LONG" { pnlPct = (price - p.entryPrice) / p.entryPrice }
	if p.side == "SHORT" { pnlPct = (p.entryPrice - price) / p.entryPrice }

	held := time.Since(p.filledAt)
	if p.filledAt.IsZero() { held = 0 }

	// Floating profit > 0.5% → skip normal timeout but add protection
	if pnlPct > 0.005 {
		// Move SL to breakeven if not already
		if p.side == "LONG" && p.stopLoss < p.entryPrice {
			p.stopLoss = p.entryPrice + p.entryPrice*s.cfg.BreakevenBuf
		}
		if p.side == "SHORT" && p.stopLoss > p.entryPrice {
			p.stopLoss = p.entryPrice - p.entryPrice*s.cfg.BreakevenBuf
		}
		// Extended timeout: 60min even with profit (prevent stale positions)
		if held >= s.cfg.RangeProfitTimeout {
			s.log.Info("RANGE TIMEOUT (profitable but stale)", zap.String("side", p.side),
				zap.Float64("pnl_pct", pnlPct*100), zap.Duration("held", held))
			s.closePos(ctx, p, pptr, "timeout_profit")
			s.consecLoss = 0
			return
		}
		return
	}
	// Floating loss → early timeout at 20min
	if pnlPct < 0 && held >= s.cfg.RangeLossTimeout {
		s.log.Info("RANGE TIMEOUT (floating loss)", zap.String("side", p.side),
			zap.Float64("pnl_pct", pnlPct*100), zap.Duration("held", held))
		s.closePos(ctx, p, pptr, "timeout_loss")
		return
	}
	// Sideways → timeout at 30min
	if held >= s.cfg.RangeFlatTimeout {
		s.log.Info("RANGE TIMEOUT (sideways)", zap.String("side", p.side),
			zap.Float64("pnl_pct", pnlPct*100), zap.Duration("held", held))
		s.closePos(ctx, p, pptr, "timeout_flat")
		return
	}

	// ── Grid orders: add on dip, take profit on bounce ──
	s.manageGrid(ctx, bar, p, pptr)
}

func (s *AIStrategy) manageGrid(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	if s.cfg.GridMaxLayers <= 0 { return }
	price := bar.Close

	// 1. Check existing grid orders for TP or fill
	for i := len(p.gridOrders) - 1; i >= 0; i-- {
		g := p.gridOrders[i]

		// Pending grid order — check if filled (limit order)
		if !g.filled {
			// Check if price reached the grid entry
			if (p.side == "LONG" && price <= g.entryPrice) || (p.side == "SHORT" && price >= g.entryPrice) {
				g.filled = true
				g.filledAt = time.Now()
				s.log.Info("AI: grid order filled",
					zap.String("side", p.side), zap.Float64("entry", g.entryPrice),
					zap.Float64("qty", g.qty), zap.Int("layer", i+1))
			}
			continue
		}

		// Filled grid order — check TP
		gridProfit := false
		if p.side == "LONG" && price >= g.tp { gridProfit = true }
		if p.side == "SHORT" && price <= g.tp { gridProfit = true }

		if gridProfit {
			s.log.Info("AI: grid TP hit",
				zap.String("side", p.side), zap.Float64("entry", g.entryPrice),
				zap.Float64("tp", g.tp), zap.Float64("price", price),
				zap.Float64("qty", g.qty), zap.Int("layer", i+1))
			s.placeCloseOrder(ctx, p.side, g.qty, false)
			p.remainQty -= g.qty
			// Remove this grid order
			p.gridOrders = append(p.gridOrders[:i], p.gridOrders[i+1:]...)
		}
	}

	// 2. Open new grid order if price moved far enough from last level
	if len(p.gridOrders) >= s.cfg.GridMaxLayers { return }
	if !p.filled { return } // base must be filled first

	// Only add grids in Range regime
	if !s.isRangeRegime(price) { return }

	// Determine the reference price (last grid entry or base entry)
	refPrice := p.entryPrice
	if len(p.gridOrders) > 0 {
		last := p.gridOrders[len(p.gridOrders)-1]
		refPrice = last.entryPrice
	}

	spacing := p.entryPrice * s.cfg.GridSpacingPct
	shouldAdd := false
	var gridEntry, gridTP float64

	if p.side == "LONG" && price <= refPrice-spacing {
		gridEntry = math.Round(price*100) / 100
		gridTP = math.Round((gridEntry+gridEntry*s.cfg.GridTPPct)*100) / 100
		shouldAdd = true
	}
	if p.side == "SHORT" && price >= refPrice+spacing {
		gridEntry = math.Round(price*100) / 100
		gridTP = math.Round((gridEntry-gridEntry*s.cfg.GridTPPct)*100) / 100
		shouldAdd = true
	}

	if !shouldAdd { return }

	// Check total position won't exceed safe limits
	gridQty := math.Floor(p.initQty*s.cfg.GridQtyRatio*1000) / 1000
	if gridQty <= 0 { return }
	// Cap: total position (base + grids) must not exceed 2x initial qty
	totalQty := p.remainQty + gridQty
	if totalQty > p.initQty*2 { return }

	// Place grid order as market (price already at the level)
	omsID := s.placeOrder(ctx, p.side, gridEntry, gridQty, false)
	if omsID == "" { return }

	g := &gridOrder{
		entryPrice: gridEntry, qty: gridQty, tp: gridTP,
		filled: true, filledAt: time.Now(), orderID: omsID,
	}
	p.gridOrders = append(p.gridOrders, g)
	p.remainQty += gridQty

	s.log.Info("AI: grid order opened",
		zap.String("side", p.side), zap.Float64("entry", gridEntry),
		zap.Float64("tp", gridTP), zap.Float64("qty", gridQty),
		zap.Int("layer", len(p.gridOrders)))
}

func (s *AIStrategy) manageTrend(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	if p.barsHeld < s.cfg.MinTrendBars { return }

	// When staged TP orders are on the exchange, the exchange handles all exits.
	// Only run GPT reversal check (to cancel all orders and reverse if market flips).
	if p.stagedTPPlaced {
		if s.barCount-s.lastCallBar >= s.cfg.CallIntervalBars {
			s.checkReversal(ctx, bar, p, pptr)
		}
		return
	}

	// Fallback: local trailing for paper/backtest mode (no staged orders).
	price := bar.Close
	atr := s.calcATR()

	pnlR := 0.0
	if p.R > 0 {
		if p.side == "LONG" { pnlR = (price - p.entryPrice) / p.R }
		if p.side == "SHORT" { pnlR = (p.entryPrice - price) / p.R }
	}

	// Adaptive trailing based on 15m ATR + profit level
	atr15 := 0.0
	bars15 := s.barsForInterval("15m")
	if len(bars15) >= 15 {
		recent15 := bars15[len(bars15)-15:]
		var sum15 float64
		for i := 1; i < len(recent15); i++ {
			sum15 += math.Max(recent15[i].High-recent15[i].Low,
				math.Max(math.Abs(recent15[i].High-recent15[i-1].Close), math.Abs(recent15[i].Low-recent15[i-1].Close)))
		}
		atr15 = sum15 / float64(len(recent15)-1)
	}
	baseTrailPct := s.cfg.TrailBasePct
	if atr15 > 0 && p.peakPrice > 0 {
		atr15Pct := atr15 / p.peakPrice
		if atr15Pct < 0.005 { baseTrailPct = s.cfg.TrailLowVolPct }
		if atr15Pct >= 0.005 && atr15Pct < 0.01 { baseTrailPct = s.cfg.TrailBasePct }
		if atr15Pct >= 0.01 { baseTrailPct = s.cfg.TrailHighVolPct }
	}

	var trailDist float64
	trailFloor := p.peakPrice * s.cfg.TrailFloorPct
	if pnlR >= 2.0 {
		trailDist = p.peakPrice * baseTrailPct * 0.40
		if trailDist < trailFloor { trailDist = trailFloor }
	} else if pnlR >= 1.5 {
		d := p.peakPrice * baseTrailPct * 0.65
		if d < p.peakPrice*0.006 { d = p.peakPrice * 0.006 }
		trailDist = d
	} else if pnlR >= 1.0 {
		trailDist = p.peakPrice * baseTrailPct
	} else {
		trailDist = atr * s.cfg.TrailingATRK
		minTrailDist := p.peakPrice * s.cfg.TrailBasePct
		if trailDist < minTrailDist { trailDist = minTrailDist }
	}

	if p.side == "LONG" {
		nt := p.peakPrice - trailDist
		if pnlR >= 0.5 && nt < p.entryPrice { nt = p.entryPrice }
		if nt > p.trailing { p.trailing = nt }
		if price <= p.trailing && p.trailing > p.stopLoss {
			s.closePos(ctx, p, pptr, "trailing")
			if pnlR > 0 { s.consecLoss = 0 }
			return
		}
	} else {
		nt := p.peakPrice + trailDist
		if pnlR >= 0.5 && nt > p.entryPrice { nt = p.entryPrice }
		if nt < p.trailing { p.trailing = nt }
		if price >= p.trailing && p.trailing > 0 && p.trailing < p.stopLoss {
			s.closePos(ctx, p, pptr, "trailing")
			if pnlR > 0 { s.consecLoss = 0 }
			return
		}
	}

	if s.barCount-s.lastCallBar >= s.cfg.CallIntervalBars && p.barsHeld >= s.cfg.MinTrendBars {
		s.checkReversal(ctx, bar, p, pptr)
	}
}

func (s *AIStrategy) checkReversal(ctx *strategy.Context, bar exchange.Kline, p *posState, pptr **posState) {
	mktCtx := s.buildContext(ctx, bar)
	signal, err := s.callGPT(mktCtx)
	if err != nil { s.lastCallBar = s.barCount; return }
	s.lastCallBar = s.barCount
	s.totalCall++
	s.cacheSignal(bar, signal)

	// Extract reversal signal from new dual format
	reverseConf := 0.0
	reverseReason := ""
	if p.side == "LONG" && signal.Short != nil {
		reverseConf = signal.Short.Confidence
		reverseReason = signal.Short.Reasoning
	}
	if p.side == "SHORT" && signal.Long != nil {
		reverseConf = signal.Long.Confidence
		reverseReason = signal.Long.Reasoning
	}
	// Backward compat: old single-signal format
	if signal.Action != "" {
		if p.side == "LONG" && signal.Action == "SELL" { reverseConf = signal.Confidence; reverseReason = signal.Reasoning }
		if p.side == "SHORT" && signal.Action == "BUY" { reverseConf = signal.Confidence; reverseReason = signal.Reasoning }
	}

	s.log.Info("AI reversal check",
		zap.String("holding", p.side),
		zap.Float64("reverse_conf", reverseConf),
		zap.String("reasoning", reverseReason))

	// Reversal threshold lower than entry — exit faster when direction changes
	if reverseConf >= s.cfg.ReversalConf {
		s.log.Info("AI: reversal → close "+p.side, zap.Float64("conf", reverseConf))
		s.closePos(ctx, p, pptr, "gpt_reversal")
		s.stopBar = s.barCount // prevent immediate re-entry on same bar
	}
}

// ─── Close Helpers ───────────────────────────────────────────────────────────

func (s *AIStrategy) closePos(ctx *strategy.Context, p *posState, pptr **posState, reason string) {
	qty := math.Floor(p.remainQty*1000) / 1000
	if qty <= 0 { *pptr = nil; return }

	// If staged TP orders are on exchange, cancel them first (GPT reversal / manual close).
	if p.stagedTPPlaced {
		if ep, ok := ctx.Extra["staged_exit"].(strategy.StagedExitPlacer); ok {
			posSide := "LONG"
			if p.side == "SHORT" { posSide = "SHORT" }
			ep.CancelAllProtective(s.cfg.Symbol, posSide)
		}
		p.stagedTPPlaced = false
	}

	// Log grid orders being closed with base
	if len(p.gridOrders) > 0 {
		gridQty := 0.0
		for _, g := range p.gridOrders { if g.filled { gridQty += g.qty } }
		if gridQty > 0 {
			s.log.Info("AI: closing grid orders with base",
				zap.Int("layers", len(p.gridOrders)), zap.Float64("grid_qty", gridQty))
		}
	}

	// Stop-loss, trailing, and reversal use market order (must fill immediately)
	// Only TP and timeout use limit for lower fees
	useMarket := reason == "stop_loss" || reason == "gpt_reversal" || reason == "trailing" || reason == "timeout_loss"
	s.placeCloseOrder(ctx, p.side, qty, useMarket)
	bars := s.primaryBars()
	closePrice := 0.0
	if len(bars) > 0 { closePrice = bars[len(bars)-1].Close }
	pnl := 0.0
	if p.side == "LONG" { pnl = (closePrice - p.entryPrice) * qty }
	if p.side == "SHORT" { pnl = (p.entryPrice - closePrice) * qty }
	s.log.Info("AI: CLOSE", zap.String("side", p.side), zap.String("reason", reason),
		zap.Float64("entry", p.entryPrice), zap.Float64("qty", qty), zap.Bool("market", useMarket),
		zap.Float64("est_pnl", pnl))
	s.logEvent("close", p.side, reason, closePrice, p.entryPrice, qty, 0, pnl, "")
	s.syncRemove(p.side)
	*pptr = nil
}

func (s *AIStrategy) closePartial(ctx *strategy.Context, p *posState, pptr **posState, qty float64, reason string) {
	qty = math.Floor(qty*1000) / 1000
	if qty <= 0 { return }
	if qty > p.remainQty { qty = p.remainQty }

	s.placeCloseOrder(ctx, p.side, qty, false) // partial close always uses limit
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

const systemPrompt = `You are a crypto futures trader. Multi-timeframe analysis: 5m (entry) + 15m (trend).

RESPONSE (strict JSON):
{"long":{"confidence":0.0-1.0,"entry_price":0.00,"reasoning":"..."},"short":{"confidence":0.0-1.0,"entry_price":0.00,"reasoning":"..."}}

MULTI-TIMEFRAME RULES:
1. CHECK indicators_15m FIRST for STRUCTURE (EMA20 vs EMA50) — this is the dominant trend:
   - 15m EMA20 > EMA50: BULLISH STRUCTURE → favor long, short needs strong evidence
   - 15m EMA20 < EMA50: BEARISH STRUCTURE → favor short, long needs strong evidence
2. THEN check return_8bar for MOMENTUM:
   - 15m return_8bar > +1%: strong upward momentum
   - 15m return_8bar < -1%: strong downward momentum
   - Between ±1%: weak/mixed momentum
3. CRITICAL — distinguish BOUNCE from REVERSAL:
   - If 15m EMA structure is BEARISH but return_8bar is temporarily positive:
     this is an OVERSOLD BOUNCE, NOT a trend reversal. Keep long confidence < 0.50.
   - If 15m EMA structure is BULLISH but return_8bar is temporarily negative:
     this is an OVERBOUGHT PULLBACK, NOT a trend reversal. Keep short confidence < 0.50.
   - True reversal requires BOTH structure change (EMA crossover) AND momentum alignment.
4. USE 5m indicators for precise timing:
   - long entry_price: nearest SUPPORT (swing_low_10, bb_lower, ema20), below current price
   - short entry_price: nearest RESISTANCE (swing_high_10, bb_upper), above current price
   - entry_price within 0.5% of current price

CONFIDENCE GUIDE:
- Strong trend (structure + momentum aligned): 0.85-0.95
- Range (EMA20 ≈ EMA50): 0.65-0.85 for both sides
- Bounce against structure (counter-trend): < 0.50
- Weak/conflicting signals: 0.30-0.60

Be decisive. When 15m STRUCTURE and MOMENTUM both align, give HIGH confidence (0.85+).
Never chase a bounce as if it were a reversal.`

type mktCtx struct {
	Symbol       string             `json:"symbol"`
	Price        float64            `json:"price"`
	Indicators   map[string]float64 `json:"indicators"`
	Indicators15 map[string]float64 `json:"indicators_15m,omitempty"`
	RecentBars   []barData          `json:"recent_bars"`
	Position     string             `json:"position"`
}
type barData struct {
	T string `json:"t"`; O, H, L, C, V float64
}

func (s *AIStrategy) buildContext(ctx *strategy.Context, bar exchange.Kline) mktCtx {
	closes := s.getCloses()
	rsi := indicator.Last(indicator.RSI(closes, s.cfg.RSIPeriod))
	macd := indicator.MACD(closes, s.cfg.MACDFast, s.cfg.MACDSlow, s.cfg.MACDSignal)
	bb := indicator.BollingerBands(closes, s.cfg.BBPeriod, s.cfg.BBStdDev)
	ema20 := indicator.Last(indicator.EMA(closes, s.cfg.EMAFast))
	ema50 := indicator.Last(indicator.EMA(closes, s.cfg.EMASlow))
	atr := s.calcATR()
	bbU, bbL := indicator.Last(bb.Upper), indicator.Last(bb.Lower)
	bbPos := 0.5; if bbU-bbL > 0 { bbPos = (bar.Close - bbL) / (bbU - bbL) }
	vols := make([]float64, len(s.primaryBars())); for i, b := range s.primaryBars() { vols[i] = b.Volume }
	volMA := indicator.Last(indicator.SMA(vols, s.cfg.VolMAPeriod)); vr := 1.0; if volMA > 0 { vr = bar.Volume / volMA }

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

	n := 10; if len(s.primaryBars()) < n { n = len(s.primaryBars()) }
	bars := make([]barData, n); st := len(s.primaryBars()) - n
	for i := 0; i < n; i++ {
		b := s.primaryBars()[st+i]
		bars[i] = barData{T: b.OpenTime.Format("15:04"), O: r2(b.Open), H: r2(b.High), L: r2(b.Low), C: r2(b.Close), V: r2(b.Volume)}
	}

	// ── 15m trend indicators ──
	var ind15 map[string]float64
	bars15 := s.barsForInterval("15m")
	if len(bars15) >= 20 {
		closes15 := make([]float64, len(bars15))
		for i, b := range bars15 { closes15[i] = b.Close }
		rsi15 := indicator.Last(indicator.RSI(closes15, s.cfg.RSIPeriod))
		ema20_15 := indicator.Last(indicator.EMA(closes15, s.cfg.EMAFast))
		ema50_15 := 0.0
		if len(closes15) >= 50 { ema50_15 = indicator.Last(indicator.EMA(closes15, s.cfg.EMASlow)) }
		macd15 := indicator.MACD(closes15, s.cfg.MACDFast, s.cfg.MACDSlow, s.cfg.MACDSignal)
		ret8 := 0.0
		if len(closes15) >= 8 { ret8 = (closes15[len(closes15)-1] - closes15[len(closes15)-8]) / closes15[len(closes15)-8] * 100 }
		trend := "range"
		if ret8 > 1.0 { trend = "uptrend" } else if ret8 < -1.0 { trend = "downtrend" }
		_ = trend
		ind15 = map[string]float64{
			"rsi":       r2(rsi15),
			"ema20":     r2(ema20_15),
			"ema50":     r2(ema50_15),
			"macd_hist": r2(indicator.Last(macd15.Histogram)),
			"return_8bar": r3(ret8),
		}
	}

	posStr := "FLAT"
	parts := []string{}
	if s.longPos != nil && s.longPos.filled { parts = append(parts, fmt.Sprintf("LONG@%.2f", s.longPos.entryPrice)) }
	if s.shortPos != nil && s.shortPos.filled { parts = append(parts, fmt.Sprintf("SHORT@%.2f", s.shortPos.entryPrice)) }
	if len(parts) > 0 { posStr = fmt.Sprintf("%v", parts) }

	return mktCtx{Symbol: s.cfg.Symbol, Price: r2(bar.Close), Indicators: ind, Indicators15: ind15, RecentBars: bars, Position: posStr}
}

func (s *AIStrategy) callGPT(mc mktCtx) (gptSignal, error) {
	ctxJSON, _ := json.Marshal(mc)
	body, _ := json.Marshal(map[string]any{
		"model": s.cfg.Model, "temperature": s.cfg.GPTTemperature, "max_completion_tokens": s.cfg.GPTMaxTokens,
		"messages": []map[string]string{{"role": "system", "content": systemPrompt}, {"role": "user", "content": string(ctxJSON)}},
	})
	callCtx, cancel := context.WithTimeout(context.Background(), s.cfg.GPTTimeout)
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

	content := strings.TrimSpace(gr.Choices[0].Message.Content)
	if content == "" { return gptSignal{}, fmt.Errorf("empty GPT response") }
	// Strip markdown code fence if present
	if strings.HasPrefix(content, "```") {
		lines := strings.Split(content, "\n")
		filtered := []string{}
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "```") { continue }
			filtered = append(filtered, l)
		}
		content = strings.Join(filtered, "\n")
	}

	var sig gptSignal
	if err := json.Unmarshal([]byte(content), &sig); err != nil {
		return gptSignal{}, fmt.Errorf("parse %q: %w", content, err)
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
			minDist := entry * s.cfg.MinSLDistPct
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
			tp1RHit: lp.TP1Hit, barsHeld: max(lp.BarsHeld, 10), filled: true, filledAt: time.Now(),
		}
		if s.longPos.initQty == 0 { s.longPos.initQty = lp.Qty }
		if s.longPos.R == 0 { s.longPos.R = math.Abs(entry - sl) }
		if s.longPos.peakPrice == 0 { s.longPos.peakPrice = currentPrice }
		if s.longPos.trailing == 0 { s.longPos.trailing = sl }
		if lp.Mode == "trend" { s.longPos.mode = modeTrend }
		if lp.Mode == "range" { s.longPos.mode = modeRange }

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
			minDist := entry * s.cfg.MinSLDistPct
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
			tp1RHit: sp.TP1Hit, barsHeld: max(sp.BarsHeld, 10), filled: true, filledAt: time.Now(),
		}
		if s.shortPos.initQty == 0 { s.shortPos.initQty = sp.Qty }
		if s.shortPos.R == 0 { s.shortPos.R = math.Abs(entry - sl) }
		if s.shortPos.peakPrice == 0 { s.shortPos.peakPrice = currentPrice }
		if s.shortPos.trailing == 0 { s.shortPos.trailing = sl }
		if sp.Mode == "trend" { s.shortPos.mode = modeTrend }
		if sp.Mode == "range" { s.shortPos.mode = modeRange }

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

// cacheSignal stores GPT signal in Redis for backtesting replay.
// Key: quantix:signals:{symbol}:{interval} → JSON list
func (s *AIStrategy) cacheSignal(bar exchange.Kline, sig gptSignal) {
	if s.rdb == nil { return }
	entry := map[string]any{
		"time":      bar.CloseTime.Unix(),
		"bar":       s.barCount,
		"price":     r2(bar.Close),
		"atr":       r2(s.calcATR()),
		"interval":  s.cfg.PrimaryInterval,
		"mtf_score": s.lastMTFScore,
	}
	if sig.Long != nil {
		entry["long_conf"] = sig.Long.Confidence
		entry["long_entry"] = sig.Long.EntryPrice
		entry["long_reason"] = sig.Long.Reasoning
	}
	if sig.Short != nil {
		entry["short_conf"] = sig.Short.Confidence
		entry["short_entry"] = sig.Short.EntryPrice
		entry["short_reason"] = sig.Short.Reasoning
	}
	// Backward compat
	if sig.Action != "" {
		entry["action"] = sig.Action
		entry["confidence"] = sig.Confidence
		entry["entry_price"] = sig.EntryPrice
	}
	data, err := json.Marshal(entry)
	if err != nil { return }
	key := fmt.Sprintf("quantix:signals:%s:%s", s.cfg.Symbol, s.cfg.PrimaryInterval)
	if err := s.rdb.RPush(context.Background(), key, string(data)).Err(); err != nil {
		s.log.Warn("AI: signal cache failed", zap.Error(err))
	}
}

func r2(v float64) float64 { return math.Round(v*100) / 100 }
func r3(v float64) float64 { return math.Round(v*1000) / 1000 }
func toFloat(v any) float64 { switch n := v.(type) { case float64: return n; case int: return float64(n); case int64: return float64(n) }; return 0 }
func toInt(v any) int { switch n := v.(type) { case float64: return int(n); case int: return n; case int64: return int(n) }; return 0 }

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
