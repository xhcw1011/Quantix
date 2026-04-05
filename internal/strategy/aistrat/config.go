package aistrat

import (
	"fmt"
	"time"

	"go.uber.org/zap"

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
		// Timeout configs removed — SL/trailing handle exits, timeouts cause random-price closes.
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
		if v, ok := params["ConfQtyScale"].(bool); ok { cfg.ConfQtyScale = v }
		if v, ok := params["MaxRPercent"]; ok { cfg.MaxRPercent = toFloat(v) }
		if v, ok := params["FeeDragPct"]; ok { cfg.FeeDragPct = toFloat(v) }
		if v, ok := params["SignalDecay"]; ok { cfg.SignalDecay = toFloat(v) }
		if v, ok := params["SignalAccumMax"]; ok { cfg.SignalAccumMax = toFloat(v) }
		if v, ok := params["RegimeN"]; ok { cfg.RegimeN = toInt(v) }
		if v, ok := params["StrongTrendThreshold"]; ok { cfg.StrongTrendThreshold = toFloat(v) }
		if v, ok := params["StrongTrendMinVol"]; ok { cfg.StrongTrendMinVol = toFloat(v) }
		if v, ok := params["SlowTrendThreshold"]; ok { cfg.SlowTrendThreshold = toFloat(v) }
		if v, ok := params["SlowTrendDirScore"]; ok { cfg.SlowTrendDirScore = toFloat(v) }
		if v, ok := params["ExpansionATRK"]; ok { cfg.ExpansionATRK = toFloat(v) }
		if v, ok := params["ExpansionBodyK"]; ok { cfg.ExpansionBodyK = toFloat(v) }
		if v, ok := params["RegimeEntryConf"]; ok { cfg.RegimeEntryConf = toFloat(v) }
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
		if v, ok := params["ForceTrend"].(bool); ok { cfg.ForceTrend = v }
		if v, ok := params["HedgeOnDrawdown"].(bool); ok { cfg.HedgeOnDrawdown = v }
		if v, ok := params["HedgeDrawdownPct"]; ok { cfg.HedgeDrawdownPct = toFloat(v) }
		if v, ok := params["HedgeCooldown"]; ok { cfg.HedgeCooldown = time.Duration(toFloat(v)) * time.Minute }
		if v, ok := params["HedgeQtyRatio"]; ok { cfg.HedgeQtyRatio = toFloat(v) }
		if v, ok := params["HedgeTPRatio"]; ok { cfg.HedgeTPRatio = toFloat(v) }
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
	HedgeMode           bool          // true = long+short simultaneously; false = single strongest direction
	ForceTrend          bool          // true = disable Range mode, always use Trend mode
	HedgeOnDrawdown     bool          // true = allow counter-trend Range scalp when main position is losing
	HedgeDrawdownPct    float64       // min drawdown % to trigger hedge (default 0.005 = 0.5% of entry)
	HedgeCooldown       time.Duration // cooldown after hedge close before next hedge (default 15m)
	HedgeQtyRatio       float64       // hedge position size as ratio of main position (default 0.3)
	HedgeTPRatio        float64       // hedge TP = min(1U equivalent, mainSL_distance * this ratio) (default 0.5)

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
	ConfQtyScale    bool    // true = scale qty by confidence
	MaxRPercent     float64 // max R/price ratio (default 0.01 = 1%); skip trade if SL too wide
	FeeDragPct      float64 // round-trip fee as % of price, deducted from R for sizing (default 0.0014 = 0.14%)
	SignalDecay     float64 // per-bar decay factor for accumulated signal (default 0.7, range 0-1)
	SignalAccumMax  float64 // cap for accumulated signal score (default 1.5)

	// Regime detection
	RegimeN               int     // lookback bars for trend strength (default 20)
	StrongTrendThreshold  float64 // trendStrength > this = STRONG_TREND (default 2.5)
	StrongTrendMinVol     float64 // min ATR/price for STRONG_TREND (default 0.001)
	SlowTrendThreshold    float64 // trendStrength > this = SLOW_TREND (default 1.5)
	SlowTrendDirScore     float64 // min direction score for SLOW_TREND (default 0.60)
	ExpansionATRK         float64 // bar range > ATR * this = breakout candidate (default 2.0)
	ExpansionBodyK        float64 // bar body > ATR * this = confirmed breakout (default 1.0)
	RegimeEntryConf       float64 // GPT confidence threshold when STRONG_TREND/EXPANSION (default 0.60)

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
		CallIntervalBars: 2, EnableShort: true,
		RiskPerTrade: 0.02, ATRK: 2.0, TrailingATRK: 1.5,
		RangeTPPct: 0.012, RangeSLPct: 0.010,
		GridMaxLayers: 2, GridSpacingPct: 0.005, GridTPPct: 0.004, GridQtyRatio: 0.5,
		TPLevels: []float64{1.5, 2.0},
		TPQtySplits: []float64{0.30, 0.30},
		BreakevenR: 1.5, BreakevenBuf: 0.001,
		TrailBasePct: 0.012, TrailLowVolPct: 0.008, TrailHighVolPct: 0.015,
		TrailFloorPct: 0.005, MinSLDistPct: 0.008,
		ReversalConf: 0.72, MarketEntryConf: 0.90,
		RangeBEPct: 0.003, RangeLockPct: 0.006, RangeLockOffset: 0.003,
		RangeTrailPct: 0.004, RangeTrailDist: 0.003,
		BBWidthMin: 0.006, BBWidthMax: 0.015, RangeEMAConv: 0.003,
		MTFStrongTrend: 0.01, MTFWeakTrend: 0.002,
		MTFBullRSI: 60, MTFBearRSI: 40, MTF1mThreshold: 0.001,
		MTFQtyScaleHard: 0.70, MTFQtyScaleSoft: 0.85, SwingProximity: 0.0015,
		ConfQtyScale: true, ForceTrend: true, MaxRPercent: 0.01, FeeDragPct: 0.0014,
		SignalDecay: 0.85, SignalAccumMax: 1.5,
		RegimeN: 20, StrongTrendThreshold: 2.5, StrongTrendMinVol: 0.001,
		SlowTrendThreshold: 1.5, SlowTrendDirScore: 0.60,
		ExpansionATRK: 2.0, ExpansionBodyK: 1.0, RegimeEntryConf: 0.60,
		RSIPeriod: 14, MACDFast: 12, MACDSlow: 26, MACDSignal: 9,
		EMAFast: 20, EMASlow: 50, BBPeriod: 20, BBStdDev: 2.0,
		ATRPeriod: 60, VolMAPeriod: 20,
		EntryOffsetPct: 0.0005, MaxEntryDevPct: 0.005,
		LimitTimeoutBars: 2, MinHoldBars: 3, MinTrendBars: 5,
		GPTTemperature: 0.3, GPTMaxTokens: 600, GPTTimeout: 15 * time.Second,
		MaxDailyLossPct: 0.10, MaxConsecLoss: 5,
		HedgeOnDrawdown: false, HedgeDrawdownPct: 0.005,
		HedgeCooldown: 15 * time.Minute, HedgeQtyRatio: 0.3, HedgeTPRatio: 0.5,
	}
}

func toFloat(v any) float64 { switch n := v.(type) { case float64: return n; case int: return float64(n); case int64: return float64(n) }; return 0 }
func toInt(v any) int { switch n := v.(type) { case float64: return int(n); case int: return n; case int64: return int(n) }; return 0 }
