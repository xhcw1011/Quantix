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
		if v, ok := params["Symbol"].(string); ok {
			cfg.Symbol = v
		}
		if v, ok := params["APIKey"].(string); ok {
			cfg.APIKey = v
		}
		if v, ok := params["Model"].(string); ok {
			cfg.Model = v
		}
		if v, ok := params["ConfidenceThreshold"]; ok {
			cfg.ConfidenceThreshold = toFloat(v)
		}
		if v, ok := params["LookbackBars"]; ok {
			cfg.LookbackBars = toInt(v)
		}
		if v, ok := params["CallIntervalBars"]; ok {
			cfg.CallIntervalBars = toInt(v)
		}
		if v, ok := params["RangeCallIntervalBars"]; ok {
			cfg.RangeCallIntervalBars = toInt(v)
		}
		if v, ok := params["Leverage"]; ok {
			cfg.Leverage = toFloat(v)
		}
		if v, ok := params["PosSizePct"]; ok {
			cfg.PosSizePct = toFloat(v)
		}
		if v, ok := params["RiskPerTrade"]; ok {
			cfg.RiskPerTrade = toFloat(v)
		}
		if v, ok := params["GridEquityPct"]; ok {
			cfg.GridEquityPct = toFloat(v)
		}
		if v, ok := params["GridRiskPerLayer"]; ok {
			cfg.GridRiskPerLayer = toFloat(v)
		}
		if v, ok := params["GridAgeSizeDecay"]; ok {
			cfg.GridAgeSizeDecay = toFloat(v)
		}
		if v, ok := params["GridAgeSizeFloor"]; ok {
			cfg.GridAgeSizeFloor = toFloat(v)
		}
		if v, ok := params["TrendEquityPct"]; ok {
			cfg.TrendEquityPct = toFloat(v)
		}
		if v, ok := params["TrendRiskPerTrade"]; ok {
			cfg.TrendRiskPerTrade = toFloat(v)
		}
		if v, ok := params["ATRK"]; ok {
			cfg.ATRK = toFloat(v)
		}
		if v, ok := params["TrailingATRK"]; ok {
			cfg.TrailingATRK = toFloat(v)
		}
		if v, ok := params["TrailingTightATRK"]; ok {
			cfg.TrailingTightATRK = toFloat(v)
		}
		if v, ok := params["MaxDailyLossPct"]; ok {
			cfg.MaxDailyLossPct = toFloat(v)
		}
		if v, ok := params["MaxConsecLoss"]; ok {
			cfg.MaxConsecLoss = toInt(v)
		}
		if v, ok := params["EnableShort"].(bool); ok {
			cfg.EnableShort = v
		}
		if v, ok := params["HedgeMode"].(bool); ok {
			cfg.HedgeMode = v
		}
		if v, ok := params["RangeTPPct"]; ok {
			cfg.RangeTPPct = toFloat(v)
		}
		if v, ok := params["RangeSLPct"]; ok {
			cfg.RangeSLPct = toFloat(v)
		}
		if v, ok := params["GridMaxLayers"]; ok {
			cfg.GridMaxLayers = toInt(v)
		}
		if v, ok := params["GridStaleBars"]; ok {
			cfg.GridStaleBars = toInt(v)
		}
		if v, ok := params["GridStalePnlR"]; ok {
			cfg.GridStalePnlR = toFloat(v)
		}
		if v, ok := params["CatastrophicStopR"]; ok {
			cfg.CatastrophicStopR = toFloat(v)
		}
		if v, ok := params["TrendCutR"]; ok {
			cfg.TrendCutR = toFloat(v)
		}
		if v, ok := params["TrendCutAgeDecay"]; ok {
			cfg.TrendCutAgeDecay = toFloat(v)
		}
		if v, ok := params["TrendCutAgeFloor"]; ok {
			cfg.TrendCutAgeFloor = toFloat(v)
		}
		if v, ok := params["TrendMaxRegimeAge"]; ok {
			cfg.TrendMaxRegimeAge = toInt(v)
		}
		if v, ok := params["TrendFixedTPR"]; ok {
			cfg.TrendFixedTPR = toFloat(v)
		}
		if v, ok := params["TrendMomentumLookback"]; ok {
			cfg.TrendMomentumLookback = toInt(v)
		}
		if v, ok := params["TrendMomentumDecayRatio"]; ok {
			cfg.TrendMomentumDecayRatio = toFloat(v)
		}
		if v, ok := params["TrendVolATRMultiple"]; ok {
			cfg.TrendVolATRMultiple = toFloat(v)
		}
		if v, ok := params["TrendVolShortATRPeriod"]; ok {
			cfg.TrendVolShortATRPeriod = toInt(v)
		}
		if v, ok := params["TrendMaxExtensionATR"]; ok {
			cfg.TrendMaxExtensionATR = toFloat(v)
		}
		if v, ok := params["TrendExtensionSwingBars"]; ok {
			cfg.TrendExtensionSwingBars = toInt(v)
		}
		if v, ok := params["TrendRequireHTFAlign"].(bool); ok {
			cfg.TrendRequireHTFAlign = v
		}
		if v, ok := params["TrendMinVolumeMultiple"]; ok {
			cfg.TrendMinVolumeMultiple = toFloat(v)
		}
		if v, ok := params["TrendVolumeLookback"]; ok {
			cfg.TrendVolumeLookback = toInt(v)
		}
		if v, ok := params["RegimeInterval"].(string); ok {
			cfg.RegimeInterval = v
		}
		if v, ok := params["HTFInterval"].(string); ok {
			cfg.HTFInterval = v
		}
		if v, ok := params["EntryFilterInterval"].(string); ok {
			cfg.EntryFilterInterval = v
		}
		if v, ok := params["RangeTrendFilter"].(bool); ok {
			cfg.RangeTrendFilter = v
		}
		if v, ok := params["HourlyTrendMinSlope"]; ok {
			cfg.HourlyTrendMinSlope = toFloat(v)
		}
		if v, ok := params["HourlyTrendEMA"]; ok {
			cfg.HourlyTrendEMA = toInt(v)
		}
		if v, ok := params["HourlyTrendStickyBars"]; ok {
			cfg.HourlyTrendStickyBars = toInt(v)
		}
		if v, ok := params["TrendScoreThreshold"]; ok {
			cfg.TrendScoreThreshold = toFloat(v)
		}
		if v, ok := params["TrendAlignFullPenaltyScore"]; ok {
			cfg.TrendAlignFullPenaltyScore = toFloat(v)
		}
		if v, ok := params["TrendScoreDecay"]; ok {
			cfg.TrendScoreDecay = toFloat(v)
		}
		if v, ok := params["TrendScorePerBarCap"]; ok {
			cfg.TrendScorePerBarCap = toFloat(v)
		}
		if v, ok := params["TrendScoreMax"]; ok {
			cfg.TrendScoreMax = toFloat(v)
		}
		if v, ok := params["TrendScoreConfirmTF"].(string); ok {
			cfg.TrendScoreConfirmTF = v
		}
		if v, ok := params["TrendEntryCooldownBars"]; ok {
			cfg.TrendEntryCooldownBars = toInt(v)
		}
		if v, ok := params["GridMaxTPDist"]; ok {
			cfg.GridMaxTPDist = toFloat(v)
		}
		if v, ok := params["GridSpacingPct"]; ok {
			cfg.GridSpacingPct = toFloat(v)
		}
		if v, ok := params["GridTPPct"]; ok {
			cfg.GridTPPct = toFloat(v)
		}
		if v, ok := params["GridQtyRatio"]; ok {
			cfg.GridQtyRatio = toFloat(v)
		}
		if v, ok := params["TPLevels"]; ok {
			switch vv := v.(type) {
			case []float64:
				cfg.TPLevels = vv
			case []any:
				var sl []float64
				for _, item := range vv {
					if f, ok := item.(float64); ok {
						sl = append(sl, f)
					}
				}
				if len(sl) > 0 {
					cfg.TPLevels = sl
				}
			}
		}
		if v, ok := params["TPQtySplits"]; ok {
			switch vv := v.(type) {
			case []float64:
				cfg.TPQtySplits = vv
			case []any:
				var sl []float64
				for _, item := range vv {
					if f, ok := item.(float64); ok {
						sl = append(sl, f)
					}
				}
				if len(sl) > 0 {
					cfg.TPQtySplits = sl
				}
			}
		}
		if v, ok := params["BreakevenR"]; ok {
			cfg.BreakevenR = toFloat(v)
		}
		if v, ok := params["BreakevenBuf"]; ok {
			cfg.BreakevenBuf = toFloat(v)
		}
		if v, ok := params["TrailBasePct"]; ok {
			cfg.TrailBasePct = toFloat(v)
		}
		if v, ok := params["TrailLowVolPct"]; ok {
			cfg.TrailLowVolPct = toFloat(v)
		}
		if v, ok := params["TrailHighVolPct"]; ok {
			cfg.TrailHighVolPct = toFloat(v)
		}
		if v, ok := params["TrailFloorPct"]; ok {
			cfg.TrailFloorPct = toFloat(v)
		}
		if v, ok := params["MinSLDistPct"]; ok {
			cfg.MinSLDistPct = toFloat(v)
		}
		if v, ok := params["ReversalConf"]; ok {
			cfg.ReversalConf = toFloat(v)
		}
		if v, ok := params["MarketEntryConf"]; ok {
			cfg.MarketEntryConf = toFloat(v)
		}
		if v, ok := params["RangeBEPct"]; ok {
			cfg.RangeBEPct = toFloat(v)
		}
		if v, ok := params["RangeLockPct"]; ok {
			cfg.RangeLockPct = toFloat(v)
		}
		if v, ok := params["RangeLockOffset"]; ok {
			cfg.RangeLockOffset = toFloat(v)
		}
		if v, ok := params["RangeTrailPct"]; ok {
			cfg.RangeTrailPct = toFloat(v)
		}
		if v, ok := params["RangeTrailDist"]; ok {
			cfg.RangeTrailDist = toFloat(v)
		}
		// Timeout configs removed — SL/trailing handle exits, timeouts cause random-price closes.
		if v, ok := params["BBWidthMin"]; ok {
			cfg.BBWidthMin = toFloat(v)
		}
		if v, ok := params["BBWidthMax"]; ok {
			cfg.BBWidthMax = toFloat(v)
		}
		if v, ok := params["RangeEMAConv"]; ok {
			cfg.RangeEMAConv = toFloat(v)
		}
		if v, ok := params["MTFStrongTrend"]; ok {
			cfg.MTFStrongTrend = toFloat(v)
		}
		if v, ok := params["MTFWeakTrend"]; ok {
			cfg.MTFWeakTrend = toFloat(v)
		}
		if v, ok := params["MTFBullRSI"]; ok {
			cfg.MTFBullRSI = toFloat(v)
		}
		if v, ok := params["MTFBearRSI"]; ok {
			cfg.MTFBearRSI = toFloat(v)
		}
		if v, ok := params["MTF1mThreshold"]; ok {
			cfg.MTF1mThreshold = toFloat(v)
		}
		if v, ok := params["MTFQtyScaleHard"]; ok {
			cfg.MTFQtyScaleHard = toFloat(v)
		}
		if v, ok := params["MTFQtyScaleSoft"]; ok {
			cfg.MTFQtyScaleSoft = toFloat(v)
		}
		if v, ok := params["SwingProximity"]; ok {
			cfg.SwingProximity = toFloat(v)
		}
		if v, ok := params["ConfQtyScale"].(bool); ok {
			cfg.ConfQtyScale = v
		}
		if v, ok := params["MaxRPercent"]; ok {
			cfg.MaxRPercent = toFloat(v)
		}
		if v, ok := params["FeeDragPct"]; ok {
			cfg.FeeDragPct = toFloat(v)
		}
		if v, ok := params["SignalDecay"]; ok {
			cfg.SignalDecay = toFloat(v)
		}
		if v, ok := params["SignalAccumMax"]; ok {
			cfg.SignalAccumMax = toFloat(v)
		}
		if v, ok := params["RegimeN"]; ok {
			cfg.RegimeN = toInt(v)
		}
		if v, ok := params["StrongTrendThreshold"]; ok {
			cfg.StrongTrendThreshold = toFloat(v)
		}
		if v, ok := params["StrongTrendMinVol"]; ok {
			cfg.StrongTrendMinVol = toFloat(v)
		}
		if v, ok := params["SlowTrendThreshold"]; ok {
			cfg.SlowTrendThreshold = toFloat(v)
		}
		if v, ok := params["SlowTrendDirScore"]; ok {
			cfg.SlowTrendDirScore = toFloat(v)
		}
		if v, ok := params["ExpansionATRK"]; ok {
			cfg.ExpansionATRK = toFloat(v)
		}
		if v, ok := params["ExpansionBodyK"]; ok {
			cfg.ExpansionBodyK = toFloat(v)
		}
		if v, ok := params["RegimeEntryConf"]; ok {
			cfg.RegimeEntryConf = toFloat(v)
		}
		if v, ok := params["RangeEntryConf"]; ok {
			cfg.RangeEntryConf = toFloat(v)
		}
		if v, ok := params["RSIPeriod"]; ok {
			cfg.RSIPeriod = toInt(v)
		}
		if v, ok := params["MACDFast"]; ok {
			cfg.MACDFast = toInt(v)
		}
		if v, ok := params["MACDSlow"]; ok {
			cfg.MACDSlow = toInt(v)
		}
		if v, ok := params["MACDSignal"]; ok {
			cfg.MACDSignal = toInt(v)
		}
		if v, ok := params["EMAFast"]; ok {
			cfg.EMAFast = toInt(v)
		}
		if v, ok := params["EMASlow"]; ok {
			cfg.EMASlow = toInt(v)
		}
		if v, ok := params["BBPeriod"]; ok {
			cfg.BBPeriod = toInt(v)
		}
		if v, ok := params["BBStdDev"]; ok {
			cfg.BBStdDev = toFloat(v)
		}
		if v, ok := params["ATRPeriod"]; ok {
			cfg.ATRPeriod = toInt(v)
		}
		if v, ok := params["VolMAPeriod"]; ok {
			cfg.VolMAPeriod = toInt(v)
		}
		if v, ok := params["TrendEfficiencyMin"]; ok {
			cfg.TrendEfficiencyMin = toFloat(v)
		}
		if v, ok := params["VolGateWindow"]; ok {
			cfg.VolGateWindow = toInt(v)
		}
		if v, ok := params["VolGateRatioBars"]; ok {
			cfg.VolGateRatioBars = toInt(v)
		}
		if v, ok := params["VolGateRegimeThresh"]; ok {
			cfg.VolGateRegimeThresh = toFloat(v)
		}
		if v, ok := params["VolGateInterval"].(string); ok {
			cfg.VolGateInterval = v
		}
		if v, ok := params["TrendExhaustPct"]; ok {
			cfg.TrendExhaustPct = toFloat(v)
		}
		if v, ok := params["SwingSLMinATR"]; ok {
			cfg.SwingSLMinATR = toFloat(v)
		}
		if v, ok := params["SwingSLMaxATR"]; ok {
			cfg.SwingSLMaxATR = toFloat(v)
		}
		if v, ok := params["GptTPMinR"]; ok {
			cfg.GptTPMinR = toFloat(v)
		}
		if v, ok := params["GptTPMaxR"]; ok {
			cfg.GptTPMaxR = toFloat(v)
		}
		if v, ok := params["CounterTrendCap"]; ok {
			cfg.CounterTrendCap = toFloat(v)
		}
		if v, ok := params["AccumBaseThresh"]; ok {
			cfg.AccumBaseThresh = toFloat(v)
		}
		if v, ok := params["BoostMinConf"]; ok {
			cfg.BoostMinConf = toFloat(v)
		}
		if v, ok := params["BounceTPR"]; ok {
			cfg.BounceTPR = toFloat(v)
		}
		if v, ok := params["EmergencyPnlR"]; ok {
			cfg.EmergencyPnlR = toFloat(v)
		}
		if v, ok := params["EntryATRK"]; ok {
			cfg.EntryATRK = toFloat(v)
		}
		if v, ok := params["MaxEntryDevPct"]; ok {
			cfg.MaxEntryDevPct = toFloat(v)
		}
		if v, ok := params["LimitTimeoutBars"]; ok {
			cfg.LimitTimeoutBars = toInt(v)
		}
		if v, ok := params["MinHoldBars"]; ok {
			cfg.MinHoldBars = toInt(v)
		}
		if v, ok := params["MinTrendBars"]; ok {
			cfg.MinTrendBars = toInt(v)
		}
		if v, ok := params["GPTTemperature"]; ok {
			cfg.GPTTemperature = toFloat(v)
		}
		if v, ok := params["GPTMaxTokens"]; ok {
			cfg.GPTMaxTokens = toInt(v)
		}
		if v, ok := params["GPTTimeout"]; ok {
			cfg.GPTTimeout = time.Duration(toFloat(v)) * time.Second
		}
		if v, ok := params["ForceTrend"].(bool); ok {
			cfg.ForceTrend = v
		}
		if v, ok := params["DisableTrend"].(bool); ok {
			cfg.DisableTrend = v
		}
		if v, ok := params["HedgeOnDrawdown"].(bool); ok {
			cfg.HedgeOnDrawdown = v
		}
		if v, ok := params["HedgeDrawdownPct"]; ok {
			cfg.HedgeDrawdownPct = toFloat(v)
		}
		if v, ok := params["HedgeCooldown"]; ok {
			cfg.HedgeCooldown = time.Duration(toFloat(v)) * time.Minute
		}
		if v, ok := params["HedgeQtyRatio"]; ok {
			cfg.HedgeQtyRatio = toFloat(v)
		}
		if v, ok := params["HedgeTPRatio"]; ok {
			cfg.HedgeTPRatio = toFloat(v)
		}
		if v, ok := params["Interval"].(string); ok && cfg.PrimaryInterval == "" {
			cfg.PrimaryInterval = v
		}
		if v, ok := params["Intervals"]; ok {
			switch vv := v.(type) {
			case []string:
				cfg.Intervals = vv
			case []any:
				for _, item := range vv {
					if s, ok := item.(string); ok {
						cfg.Intervals = append(cfg.Intervals, s)
					}
				}
			}
		}
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("ai strategy requires APIKey parameter")
		}
		return New(cfg, log), nil
	})

	// ─── UI presets ─────────────────────────────────────────────────────────
	// Surfaced via GET /api/strategies/ai/presets so users pick a starting
	// config without filling 30+ knobs. Selecting a preset just sets `params`
	// — they're free to tweak before clicking Start.
	registry.RegisterPreset("ai", registry.Preset{
		Name:        "Default (de-risked)",
		Description: "Bounded-loss mean-reversion: hard -3R catastrophic stop on ALL positions, grid adds pyramid-only (winning side, no martingale), no drawdown-hedge. Recommended for ETHUSDT live.",
		Params:      map[string]any{}, // empty = use DefaultConfig() as-is
	})
	registry.RegisterPreset("ai", registry.Preset{
		Name:        "Drawdown-hedge only",
		Description: "Single-direction trend; counter-side scalp only when main position draws down >0.5%. Less position turnover.",
		Params: map[string]any{
			"HedgeMode":        false,
			"HedgeOnDrawdown":  true,
			"HedgeDrawdownPct": 0.005,
		},
	})
	registry.RegisterPreset("ai", registry.Preset{
		Name:        "Conservative (no hedge)",
		Description: "No counter-side at all. Tight risk: MaxConsecLoss=2, MaxDailyLossPct=5%.",
		Params: map[string]any{
			"HedgeMode":       false,
			"HedgeOnDrawdown": false,
			"MaxConsecLoss":   2,
			"MaxDailyLossPct": 0.05,
		},
	})
}

// ─── Config ──────────────────────────────────────────────────────────────────

type Config struct {
	Symbol                string
	APIKey                string
	Model                 string
	ConfidenceThreshold   float64
	LookbackBars          int
	CallIntervalBars      int
	RangeCallIntervalBars int // GPT call interval (in bars) when regime=RANGE and no positions (default 3)
	EnableShort           bool
	HedgeMode             bool          // true = long+short simultaneously; false = single strongest direction
	ForceTrend            bool          // true = disable Range mode, always use Trend mode
	DisableTrend          bool          // true = disable Trend mode entries, grid/hedge-scalp only (openHedgeScalp unaffected, but requires HedgeOnDrawdown=true to fire)
	HedgeOnDrawdown       bool          // true = allow counter-trend Range scalp when main position is losing
	HedgeDrawdownPct      float64       // min drawdown % to trigger hedge (default 0.005 = 0.5% of entry)
	HedgeCooldown         time.Duration // cooldown after hedge close before next hedge (default 15m)
	HedgeQtyRatio         float64       // hedge position size as ratio of main position (default 0.3)
	HedgeTPRatio          float64       // hedge TP = min(1U equivalent, mainSL_distance * this ratio) (default 0.5)

	// Multi-timeframe
	PrimaryInterval string   // "5m" — drives GPT signals + entries
	Intervals       []string // all subscribed intervals, e.g. ["1m","5m","15m"]
	// RegimeInterval/HTFInterval/EntryFilterInterval independently select which
	// interval's bars three groups of context-data consumers read, replacing
	// what used to be a hardcoded "15m" at every call site. "" (zero value) is
	// equivalent to "15m" (see resolveInterval in helpers.go) — the long-
	// standing default. Each falls back to primary-interval bars if its
	// configured interval's data isn't loaded (and logs a warning when that
	// happens for a non-default configured value — see regimeBars/htfBars/
	// entryFilterBars in helpers.go).
	//   RegimeInterval:      detectRegime() — Range/SlowTrend/StrongTrend/Expansion classification
	//   HTFInterval:         hourlyTrendDir() (RangeTrendFilter/TrendCutR) + detectHourlyMode() (trailing 3-tier)
	//   EntryFilterInterval: TrendExhaustPct distance check + MTF confidence score
	RegimeInterval      string
	HTFInterval         string
	EntryFilterInterval string

	// Position sizing
	Leverage   float64 // exchange leverage multiplier (default 10)
	PosSizePct float64 // fraction of equity used as margin per trade (default 0.40 = 40%)

	// Grid/Range capital allocation
	GridEquityPct    float64 // fraction of equity reserved for grid mode (default 0.70 = 70%)
	GridRiskPerLayer float64 // risk per grid layer as fraction of grid equity (default 0.008 = 0.8%)
	// GridAgeSizeDecay/GridAgeSizeFloor scale a NEW grid base open's size
	// continuously by how long the current regime has already held
	// (regimeAge) at entry: size = 1.0 - (regimeAge-1)*decay, floored at
	// GridAgeSizeFloor. A freshly-confirmed Range read gets full size; older
	// reads (closer to a possible break) get progressively smaller ones.
	// GridAgeSizeDecay <= 0 disables (always full size — current default).
	// See gridAgeSizeScale in helpers.go.
	GridAgeSizeDecay  float64
	GridAgeSizeFloor  float64
	TrendEquityPct    float64 // fraction of equity reserved for trend mode (default 0.30 = 30%)
	TrendRiskPerTrade float64 // risk per trend trade as fraction of trend equity (default 0.01 = 1%)

	// ── Trend mode — core risk parameters ──
	RiskPerTrade  float64 // fraction of equity risked per trade (default 0.03 = 3%)
	ATRK          float64 // SLOW_TREND stop-loss = ATR × ATRK (default 1.2)
	SwingSLMinATR float64 // STRONG_TREND swing SL floor as ATR multiple (default 1.2)
	SwingSLMaxATR float64 // STRONG_TREND swing SL cap as ATR multiple (default 1.8)
	TrailingATRK  float64 // trailing distance = entryATR × TrailingATRK (default 2.0; safety net, not primary exit)
	// TrailingTightATRK is the trailing distance multiple used BEFORE a strong-
	// trend position reaches 2R profit (the "tight" tier). Most trend-leg losers
	// exit here, never reaching TrailingATRK's wide tier — so this, not
	// TrailingATRK, is the real lever on strong_trend/trailing losses. Default
	// 1.0 (1×ATR) matches the long-standing hardcoded value.
	TrailingTightATRK float64

	// ── GPT TP targeting — use market structure for profit targets ──
	GptTPMinR float64 // min GPT TP distance as R-multiple; floor to cover fees (default 0.5)
	GptTPMaxR float64 // max GPT TP distance as R-multiple; cap to stay reachable (default 1.5)

	// ── Signal quality filtering ──
	CounterTrendCap float64 // hard cap on counter-trend GPT confidence (default 0.40)
	AccumBaseThresh float64 // min GPT conf to contribute to accumulation: add = conf - thresh (default 0.30)
	BoostMinConf    float64 // min GPT conf for swing proximity / MTF momentum boost (default 0.70)
	BounceTPR       float64 // bounce TP: close remaining qty when price retreats this × R from peak (default 0.8)
	EmergencyPnlR   float64 // trigger emergency GPT reversal check when pnlR below this (default -0.9)

	// Range/scalp mode (percentage of entry price)
	RangeTPPct float64 // take-profit % (default 0.004 = 0.4%)
	RangeSLPct float64 // stop-loss % (default 0.0025 = 0.25%)

	// Grid mode (range only)
	GridMaxLayers  int     // max grid orders per position (default 2)
	GridSpacingPct float64 // spacing between grid levels as fallback (default 0.005 = 0.5%)
	GridTPPct      float64 // grid order take-profit as fallback (default 0.004 = 0.4%)
	GridQtyRatio   float64 // grid qty as ratio of base qty (default 0.5)
	GridMaxTPDist  float64 // max TP distance in $ — caps TP when BB is wide (default 8.0)
	GridStaleBars  int     // staleness exit: force-close if barsHeld > this (default 0 = disabled)
	GridStalePnlR  float64 // staleness exit: only fire when pnlR < this (default 0 = disabled)
	// CatastrophicStopR is a HARD stop that applies to ALL modes including
	// range/grid/hedge (which otherwise have no SL and "ride the range"). When a
	// position's pnlR drops to this level the position is market-closed, capping
	// any single loss instead of riding to -15~20R until the staleness timer.
	// Negative to enable (e.g. -3.0); 0 = disabled.
	CatastrophicStopR float64
	// TrendCutR is a trend-CONFIRMED early cut for range/grid positions: when a
	// position is underwater past this R level AND hourlyTrendDir() is confirmed
	// against it, cut now rather than riding to CatastrophicStopR. The trend gate
	// keeps normal pre-reversion dips (hourlyTrendDir==0) on full -3R room, so it
	// only cuts genuinely-wrong-direction trades. Negative to enable; 0 = disabled.
	// Default -2.0: a sweep of -0.5..-2.5 on ETHUSDT alone picked -1.25 as
	// "optimal" on full-period totals, but splitting IS/OOS showed that was
	// IS-driven overfitting (OOS ranking flipped sign across the tested range).
	// Cross-checking against BTCUSDT, -2.0 was the only value with a same-sign
	// OOS improvement on BOTH assets (ETH +$138, BTC +$80 over the OOS window) —
	// the only threshold in the swept range that wasn't noise. 2026-08-03.
	TrendCutR float64
	// TrendCutAgeDecay/TrendCutAgeFloor tighten TrendCutR (move it toward
	// zero, i.e. cut earlier) as the position's entry regimeAge grows —
	// applying the same "older regime = higher risk" signal gridAgeSizeScale
	// uses for entry sizing, but to WHEN to cut instead of HOW MUCH to risk.
	// TrendCutAgeDecay <= 0 disables (TrendCutR used unchanged — current
	// default). See ageAdjustedTrendCutR in manage.go.
	TrendCutAgeDecay float64
	TrendCutAgeFloor float64
	// TrendMaxRegimeAge blocks NEW trend-mode entries (openTrend) once the
	// current regime classification has held for more than this many
	// consecutive primary bars. Rationale: detectRegime() confirms a trend
	// only after enough bars of evidence have accumulated, so an entry taken
	// long after that confirmation is statistically taken later in the move,
	// not at its start — the classification answers "did the last N bars look
	// like a trend", not "will the next N bars continue one". 0 = disabled
	// (no age limit). Does not affect already-open positions or grid/hedge
	// entries (openGrid/openHedgeScalp are unaffected).
	TrendMaxRegimeAge int
	// TrendMomentumLookback + TrendMomentumDecayRatio: block a trend-mode
	// entry when net price momentum over the most recent TrendMomentumLookback
	// bars is below TrendMomentumDecayRatio of the momentum over the
	// preceding window of the same length — a proxy for "the move that
	// triggered this trend confirmation is already losing steam." Lookback
	// <= 0 disables the filter. See momentumDecayed in helpers.go.
	TrendMomentumLookback   int
	TrendMomentumDecayRatio float64
	// TrendVolATRMultiple: block a trend-mode entry when the short-window ATR
	// (TrendVolShortATRPeriod bars) exceeds TrendVolATRMultiple × the
	// config-default (longer) ATR — a proxy for "this regime read is riding
	// the aftershock of a volatility spike" rather than a steadily-developing
	// trend. <= 0 disables the filter. See volatilityElevated in helpers.go.
	TrendVolATRMultiple    float64
	TrendVolShortATRPeriod int
	// TrendMaxExtensionATR: block a trend-mode entry when price has already
	// moved more than this many ATRs from the recent swing extreme (swing low
	// for LONG, swing high for SHORT) — a proxy for "chasing a move already
	// in progress" rather than catching its start. <= 0 disables the filter.
	// Swing window is TrendExtensionSwingBars bars. See priceExtended in helpers.go.
	TrendMaxExtensionATR    float64
	TrendExtensionSwingBars int
	// TrendRequireHTFAlign blocks a trend-mode entry when hourlyTrendDir() (the
	// existing ~4h EMA-slope read off 15m bars, already computed every bar and
	// already used by RangeTrendFilter/TrendCutR for grid positions) actively
	// opposes the entry side. htfDir==0 (no confirmed htf trend) does NOT
	// block. false = disabled (current default: trend entries ignore htf).
	// See htfMisaligned in helpers.go.
	TrendRequireHTFAlign bool
	// TrendMinVolumeMultiple blocks a trend-mode entry when the current bar's
	// Volume is below this multiple of the average Volume over the preceding
	// TrendVolumeLookback bars — a proxy for "price moved on low
	// participation," a common precursor to fake breakouts / reversion.
	// <= 0 disables the filter. See volumeInsufficient in helpers.go.
	TrendMinVolumeMultiple float64
	TrendVolumeLookback    int
	// RangeTrendFilter, when true, also applies the 1h-EMA trend filter to
	// reversion entries (Phase 2): don't fade a CONFIRMED 1h trend even when the
	// regime is classified RANGE. A true range has hourlyTrendDir==0 so both sides
	// still fade; only a sustained 1h trend suppresses the counter-trend side.
	RangeTrendFilter bool
	// HourlyTrendMinSlope is the minimum per-bar 1h-EMA slope (fraction of the EMA,
	// e.g. 0.0002 = 0.02%) for hourlyTrendDir to call a trend. Below it the slow
	// ~20h EMA is treated as flat (neutral), so the 1h-trend filter does NOT
	// over-suppress reversion fades in a flat range where the EMA merely lags a
	// prior trend (the "55 SELL-blocked in a 1744–1750 chop" case).
	HourlyTrendMinSlope float64
	// HourlyTrendEMA is the EMA period (in 15m bars) for the trend-direction filter.
	// Default 16 = 4h. The old 80 (=20h) lagged badly and kept reading "bullish"
	// off a prior high while price had turned down → forced longs into a fall.
	// Lower = tracks turns faster but more whipsaw.
	HourlyTrendEMA int
	// HourlyTrendStickyBars adds hysteresis to the 1h trend direction used by the
	// entry filter: once a ±1 trend is confirmed, hold it for this many primary
	// (5m) bars of neutral readings before decaying to 0. Stops the filter from
	// re-allowing counter-trend entries on every bounce inside a stair-step trend
	// (the gap that let ~half the longs slip through during a decline). 0 = off
	// (default — enabled per-engine via params for gradual rollout, e.g. 12 ≈ 1h).
	HourlyTrendStickyBars int

	// ── Trend-score leg (see docs/superpowers/specs/2026-06-30-aistrat-trend-score-leg-design.md) ──
	// Continuous 5m trend-accumulation score replacing the binary regime gate.
	TrendScoreThreshold        float64 // accumulated score to trigger a trend entry; 0 = no trend entry (offense off)
	TrendAlignFullPenaltyScore float64 // |score| at which counter-trend fade conf → 0; 0 = no penalty (defense off)
	TrendScoreDecay            float64 // per-bar decay of trendScore (e.g. 0.9)
	TrendScorePerBarCap        float64 // per-bar delta clamp in ATR units (e.g. 1.0)
	TrendScoreMax              float64 // |trendScore| cap (e.g. 5.0)
	TrendScoreConfirmTF        string  // higher-TF confirm source: "15m" (lastTrendDir) or "1h" (lastHourlyDir)
	TrendEntryCooldownBars     int     // primary bars to wait after a trend entry before another

	// Staged TP (trend mode) — exchange-native limit orders
	// Default (range/slow_trend) TP levels:
	TPLevels    []float64 // R-multiples for each TP level
	TPQtySplits []float64 // fraction of qty for each level
	// Strong-trend TP levels (wider targets to ride momentum):
	TrendTPLevels    []float64 // R-multiples for STRONG_TREND/EXPANSION (default [1.0, 1.8])
	TrendTPQtySplits []float64 // fraction of qty for trend TP (default [0.50, 0.30])
	// NOTE: TrendTPLevels/TrendTPQtySplits only drive the LIVE exchange-order
	// staged-TP path (placeStagedExitOrders, entry.go) — it requires
	// ctx.Extra["staged_exit"], which backtest/paper never populate, so these
	// two fields have NO effect in cmd/backtest. TrendFixedTPR below is the
	// backtest-safe equivalent (checked bar-level in manageTrend).
	TrendBreakevenR float64 // breakeven R for trend mode (default 0.80)
	// TrendFixedTPR closes the WHOLE trend position at a fixed R target instead
	// of riding trailing — "short-term momentum" framing (bank the win, don't
	// try to ride it) rather than "trend following" (let profits run via
	// trailing). <= 0 disables (trailing-only, current default behavior).
	TrendFixedTPR float64
	BreakevenR    float64 // R threshold to move SL to breakeven (default 0.5)
	BreakevenBuf  float64 // buffer above/below entry for breakeven SL (default 0.001 = 0.1%)

	// Trailing stop (trend fallback when staged orders unavailable)
	TrailBasePct    float64 // base trailing % (default 0.012 = 1.2%)
	TrailLowVolPct  float64 // trailing % for low volatility (default 0.008)
	TrailHighVolPct float64 // trailing % for high volatility (default 0.015)
	TrailFloorPct   float64 // absolute minimum trailing distance % (default 0.005)
	MinSLDistPct    float64 // minimum SL distance from entry (default 0.008 = 0.8%)
	ReversalConf    float64 // confidence threshold for GPT reversal exit (default 0.72)
	MarketEntryConf float64 // confidence threshold for immediate market entry (default 0.90)

	// Range position management
	RangeBEPct      float64 // PnL % to move SL to breakeven (default 0.003)
	RangeLockPct    float64 // PnL % to lock in partial profit (default 0.006)
	RangeLockOffset float64 // profit lock offset % (default 0.003)
	RangeTrailPct   float64 // PnL % to start trailing (default 0.008)
	RangeTrailDist  float64 // trailing distance % (default 0.003)
	BBWidthMin      float64 // min BB width for range TP (default 0.006)
	BBWidthMax      float64 // max BB width for range TP (default 0.015)
	RangeEMAConv    float64 // EMA convergence threshold for regime detection (default 0.003)

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
	RegimeN              int     // lookback bars for trend strength (default 20)
	StrongTrendThreshold float64 // trendStrength > this = STRONG_TREND (default 2.5)
	StrongTrendMinVol    float64 // min ATR/price for STRONG_TREND (default 0.001)
	SlowTrendThreshold   float64 // trendStrength > this = SLOW_TREND (default 1.5)
	SlowTrendDirScore    float64 // min direction score for SLOW_TREND (default 0.60)
	ExpansionATRK        float64 // bar range > ATR * this = breakout candidate (default 2.0)
	ExpansionBodyK       float64 // bar body > ATR * this = confirmed breakout (default 1.0)
	TrendEfficiencyMin   float64 // min efficiency ratio (|net|/sum|moves|) to classify as trend; below = RANGE (default 0.30)
	// VolGateWindow/VolGateRatioBars/VolGateRegimeThresh: when efficiency
	// would force a RANGE classification, this composite volume-percentile
	// score (see volGateScore in helpers.go) can veto it — quiet price action
	// with abnormally high/rising volume skips the early Range return and
	// falls through to the trend-strength classification instead.
	// VolGateWindow <= 0 disables (original always-Range behavior).
	VolGateWindow       int
	VolGateRatioBars    int
	VolGateRegimeThresh float64
	// VolGateInterval selects which interval's bars feed volGateScore.
	// "" (default) = primary-interval bars (original behavior). Set to e.g.
	// "4h" to score against coarser, less noisy aggregated volume instead of
	// raw 5m volume — requires that interval's data to also be loaded via
	// -context-intervals (or it silently reads zero bars → neutral 0.5 score,
	// same fallback-visibility gap flagged for RegimeInterval/HTFInterval).
	VolGateInterval string
	TrendExhaustPct float64 // skip with-trend entry if 4h move > price × this (default 0.035 = 3.5%)
	RegimeEntryConf float64 // GPT confidence threshold for with-trend entry in STRONG_TREND/EXPANSION (default 0.80)
	RangeEntryConf  float64 // GPT confidence threshold when RANGE; 0 = disabled (default 0)

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
	EntryATRK        float64 // entry offset = ATR × EntryATRK (default 0.5; adapts to volatility)
	MaxEntryDevPct   float64 // max GPT entry deviation from spot (default 0.005)
	LimitTimeoutBars int     // bars to wait for limit fill (default 2)
	MinHoldBars      int     // minimum bars before TP/SL checks (default 3)
	MinTrendBars     int     // minimum bars before trend management (default 5)

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
		// ─── 基础 ──────────────────────────────────────────────────────
		Symbol: "ETHUSDT", Model: "tech-rev+brk", // GPT was removed (signal.go:273); name no longer claims an LLM
		Leverage: 10, EnableShort: true, ForceTrend: false, DisableTrend: false,

		// ─── 核心风险参数（最常调整）─────────────────────────────────
		RiskPerTrade:  0.015,                         // 每笔风险 1.5% equity（旧参数，被 Grid/Trend 分仓参数取代）
		GridEquityPct: 0.70, GridRiskPerLayer: 0.008, // 网格: 70% equity, 每层风险 0.8%
		GridAgeSizeDecay: 0, GridAgeSizeFloor: 0.2, // off (2026-08-04): looked cleanly validated on the
		// short Jan25-May9 2026 window (smooth cross-asset non-edge-of-range optimum at 0.20), but
		// extending to 21 months of history (2024-08 to 2026-05, IS/OOS split 2025-11-01) reversed it —
		// OOS was best with the mechanism OFF (-177.6) and got WORSE at every nonzero decay tested
		// (-410 to -504). The short window's clean-looking validation wasn't enough; kept off pending
		// a mechanism that holds up on the longer history. See TrendCutR/TrendCutAgeDecay, which DID
		// hold up (best OOS value on both the short and 21-month windows).
		TrendEquityPct: 0.30, TrendRiskPerTrade: 0.01, // 趋势: 30% equity, 每笔风险 1%
		ATRK:              3.0, // SLOW_TREND SL = ATR × 3.0
		SwingSLMinATR:     2.5, // STRONG_TREND SL 下限 = ATR × 2.5（放宽：给swing SL足够空间）
		SwingSLMaxATR:     4.0, // STRONG_TREND SL 上限 = ATR × 4.0（放宽：强趋势需要更多回撤空间）
		TrailingATRK:      3.0, // trailing = entryATR × 3.0
		TrailingTightATRK: 1.0, // tight-tier trailing (pnlR<2) = entryATR × 1.0 — matches prior hardcoded behavior

		// ─── 入场门槛 ─────────────────────────────────────────────
		ConfidenceThreshold: 0.80, // 默认/逆趋势入场门槛
		RegimeEntryConf:     0.72, // STRONG_TREND/EXPANSION 顺趋势门槛（宽松，抓住更多顺势单）
		RangeEntryConf:      0.80, // RANGE 均值回归入场门槛 — ETH IS/OOS 验证过 0.70/0.75/0.80/0.85（2026-08-03）：
		// grid_tp OOS expectancy 从 0.75 的 +0.37/trade 提升到 0.80 的 +0.92/trade（翻倍以上），
		// 整体 net_pnl/PF 同步改善，0.80→0.85 边际效果消失（171笔IS完全相同）→ 效果在0.80已饱和，取更保守值
		CounterTrendCap: 0.25, // 逆趋势 GPT conf 硬上限（从0.40降低：防止accumulator叠加逆趋势信号到触发阈值）
		BoostMinConf:    0.70, // swing/MTF boost 最低 GPT conf
		ReversalConf:    0.75, // GPT reversal 平仓门槛
		MarketEntryConf: 0.90, // 市价入场门槛（未使用）

		// ─── 信号积累 ─────────────────────────────────────────────
		AccumBaseThresh:  0.30, // conf > 0.30 才积累：add = conf - 0.30
		SignalDecay:      0.80, // 每bar衰减 × 0.80
		SignalAccumMax:   0.85, // 积累上限（降低：溢出信号准确率仅20%）
		CallIntervalBars: 1, RangeCallIntervalBars: 3, LookbackBars: 60,

		// ─── TP 获利 ──────────────────────────────────────────────
		TPLevels: []float64{1.0}, TPQtySplits: []float64{0.50}, // 默认 TP: 1.0R 平 50%
		TrendTPLevels: []float64{2.0}, TrendTPQtySplits: []float64{0.30}, // 强趋势 TP: 2.0R 平 30%，70% 靠 trailing 吃趋势
		GptTPMinR: 0.50, // GPT支撑/阻力位做TP：有效范围 0.5R ~ 2.5R
		GptTPMaxR: 2.50,
		BounceTPR: 0.40, // TP部分成交后，价格从peak回撤 0.4R 平掉剩余（缩小配合0.5R TP）

		// ─── 保护机制 ─────────────────────────────────────────────
		TrendBreakevenR: 0.50, BreakevenR: 0.50, BreakevenBuf: 0.001, // 盈利0.5R后SL移到入场价，防止赚变亏
		EmergencyPnlR: -0.9,  // 亏损超 0.9R 触发紧急GPT检查
		MinSLDistPct:  0.008, // SL最小距离 0.8%
		MaxRPercent:   0.015, // R/price > 1.5% 跳过交易（放宽：旧1%在宽SL下会误拦）

		// ─── 入场微调 ─────────────────────────────────────────────
		EntryATRK:      0.5,    // 入场 offset = ATR × 0.5（自适应波动率，正常≈$2.5，高波动≈$4）
		MaxEntryDevPct: 0.010,  // GPT入场价最大偏差 1.0%
		PosSizePct:     0.40,   // 单笔最大margin占比 40%
		ConfQtyScale:   false,  // 不按confidence缩放qty
		FeeDragPct:     0.0004, // 手续费 Maker 0.02% × 2 = 0.04% round-trip

		// ─── 时间控制 ─────────────────────────────────────────────
		LimitTimeoutBars: 2, // 限价单超时 2 bars (10min)
		MinHoldBars:      3, // 最少持仓 3 bars (15min)
		MinTrendBars:     5, // 趋势管理启动 5 bars (25min)

		// ─── Regime 检测 ──────────────────────────────────────────
		RegimeN: 20, StrongTrendThreshold: 2.5, StrongTrendMinVol: 0.001,
		SlowTrendThreshold: 1.5, SlowTrendDirScore: 0.60,
		ExpansionATRK: 2.0, ExpansionBodyK: 1.0,
		TrendEfficiencyMin: 0.40,                                             // 效率比<0.40=震荡市→不交易（收紧：防止震荡市误判为趋势）
		VolGateWindow:      0, VolGateRatioBars: 8, VolGateRegimeThresh: 0.6, // off by default (enable via params)
		TrendExhaustPct: 0.035, // 4h趋势运行>3.5%时不再追入（ETH≈$80，防止追在顶/底）

		// ─── MTF 评分 ─────────────────────────────────────────────
		MTFStrongTrend: 0.01, MTFWeakTrend: 0.002,
		MTFBullRSI: 60, MTFBearRSI: 40, MTF1mThreshold: 0.001,
		MTFQtyScaleHard: 0.70, MTFQtyScaleSoft: 0.85, SwingProximity: 0.0015,

		// ─── 技术指标周期 ─────────────────────────────────────────
		RSIPeriod: 14, MACDFast: 12, MACDSlow: 26, MACDSignal: 9,
		EMAFast: 20, EMASlow: 50, BBPeriod: 20, BBStdDev: 2.0,
		ATRPeriod: 60, VolMAPeriod: 20,

		// ─── GPT 调用 ─────────────────────────────────────────────
		GPTTemperature: 0.1, GPTMaxTokens: 200, GPTTimeout: 15 * time.Second,

		// ─── Range模式（当前禁用）────────────────────────────────
		RangeTPPct: 0.012, RangeSLPct: 0.010,
		RangeBEPct: 0.003, RangeLockPct: 0.006, RangeLockOffset: 0.003,
		RangeTrailPct: 0.004, RangeTrailDist: 0.003,
		BBWidthMin: 0.006, BBWidthMax: 0.015, RangeEMAConv: 0.003,
		GridMaxLayers: 3, GridSpacingPct: 0.01, GridTPPct: 0.004, GridQtyRatio: 0.5, GridMaxTPDist: 8.0, // layers add PYRAMID-only (winning side, see manageGrid) — never average into losers
		GridStaleBars: 576, GridStalePnlR: -1.5, // 48h @ 5m bars × pnlR < -1.5R → 强制释放槽位
		CatastrophicStopR: -3.0,                         // hard stop for ALL modes — cap any single position loss at ~3R
		TrendCutR:         -2.0,                         // trend-confirmed early cut for range/grid positions — cross-asset OOS-validated, see field doc
		TrendCutAgeDecay:  0.30, TrendCutAgeFloor: -0.5, // ETH-focused, IS/OOS validated (2026-08-04): clean
		// single-peaked sweep (0 to 0.75) with grid-leg net_pnl and overall_net BOTH maximized at 0.30;
		// OOS specifically stays flat/stable through 0.30 (-217 to -249) then degrades sharply beyond
		// (-352 to -412 at 0.35+) — the boundary marks where IS improvement stops being real signal.
		// NOTE: not cross-asset validated — BTC showed OOS degradation at every tested decay value
		// (weaker underlying regimeAge/outcome correlation there, see gridAgeSizeScale's cross-asset
		// pre-check). Per-user direction (2026-08-04): ETH-only focus, so shipped without the BTC gate
		// this session otherwise required — revisit if/when BTC grid is ever revisited.
		TrendMaxRegimeAge: 4, // trend entry signal-age filter — ETH IS/OOS validated (2026-08-04): sweep 0-24
		// showed a genuine single-peaked curve (not monotonic) with per-trade trend expectancy
		// peaking at age=4 (-1.94/trade vs -2.18 baseline); age=1 is WORSE than baseline (-2.39),
		// ruling out "less trend trading is just always better" as the explanation. See field doc.
		TrendMomentumLookback: 0, TrendMomentumDecayRatio: 0.5, // off by default (enable via params)
		TrendVolATRMultiple: 0, TrendVolShortATRPeriod: 14, // off by default (enable via params)
		TrendMaxExtensionATR: 0, TrendExtensionSwingBars: 20, // off by default (enable via params)
		TrendRequireHTFAlign:   false,                      // off by default (enable via params)
		TrendMinVolumeMultiple: 0, TrendVolumeLookback: 20, // off by default (enable via params)
		RegimeInterval: "15m", HTFInterval: "15m", EntryFilterInterval: "15m", // long-standing default; independently overridable via params
		TrendFixedTPR:              0,      // off by default (trailing-only); enable via params, e.g. 1.0-2.0
		RangeTrendFilter:           true,   // Phase 2: don't fade a confirmed 1h trend even in RANGE mode
		HourlyTrendMinSlope:        0.0002, // 0.02%/bar — a flat/lagging EMA reads neutral (don't over-suppress)
		HourlyTrendEMA:             16,     // 4h trend reference (was period-80 = 20h, too laggy → forced longs into falls)
		HourlyTrendStickyBars:      0,      // 1h-dir hysteresis: 0 = off (enable per-engine via params, e.g. 12 ≈ 1h)
		TrendScoreThreshold:        3.5,
		TrendAlignFullPenaltyScore: 2.5,
		TrendScoreDecay:            0.9,
		TrendScorePerBarCap:        1.0,
		TrendScoreMax:              5.0,
		TrendScoreConfirmTF:        "15m",
		TrendEntryCooldownBars:     12,
		TrailBasePct:               0.012, TrailLowVolPct: 0.008, TrailHighVolPct: 0.015, TrailFloorPct: 0.005,

		// ─── 风控 ─────────────────────────────────────────────────
		MaxDailyLossPct: 0.10, MaxConsecLoss: 3,
		HedgeMode: true, HedgeOnDrawdown: false, HedgeDrawdownPct: 0.005, // HedgeOnDrawdown off: cut losers, don't hedge-martingale them
		HedgeCooldown: 15 * time.Minute, HedgeQtyRatio: 0.3, HedgeTPRatio: 0.5,
	}
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}
func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}
