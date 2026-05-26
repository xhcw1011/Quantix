package aistrat

import "time"

// ─── Regime ─────────────────────────────────────────────────────────────────

type Regime string

const (
	RegimeStrongTrend Regime = "STRONG_TREND"
	RegimeSlowTrend   Regime = "SLOW_TREND"
	RegimeRange       Regime = "RANGE"
	RegimeExpansion   Regime = "EXPANSION"
)

// ─── Hourly Mode ────────────────────────────────────────────────────────────

type hourlyMode int
const (
	hourlyTrendStrong hourlyMode = iota // 1h EMA aligned + slope strong → wide trail, let it run
	hourlyTrendWeak                      // 1h neutral or mixed → normal trail
	hourlyExitMode                       // 1h EMA opposes position → tight trail, prepare exit
)

func (s *AIStrategy) hourlyModeStr(side string) string {
	m := s.detectHourlyMode(side)
	switch m {
	case hourlyTrendStrong: return "STRONG"
	case hourlyExitMode:    return "EXIT"
	default:                return "WEAK"
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
	entryATR   float64 // ATR at entry time — used for trailing so it never tightens when live ATR shrinks
	gptTPPrice float64 // GPT support/resistance level used as TP target (0 = use R-based default)
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
	// firstFillSeen flips true on the first OnFill event matching this position.
	// Distinguishes the "filled=true preset by entry.go for market orders" state
	// from the "actual fill event received" state. Required to avoid double-
	// counting the market order's first fill in the partial-fill accumulator.
	firstFillSeen bool
	orderID    string
	limitBar   int

	closeFailCount int       // consecutive close order failures (reset on success)
	lastPeakAt     time.Time // when peakPrice was last updated (for stale peak detection)
	trailTier      int       // 0=none, 1=tight(1ATR), 2=wide(3ATR) — tracks current trailing tier

	// Staged TP (trend mode): exchange-native limit orders
	stagedTPPlaced bool // true once TP orders are on the exchange
	safetyNetSL    bool // true while a temporary exchange SL protects trend positions during recovery
	stagedTPs      []stagedTPRecord // tracks each TP level for dynamic adjustment

	// Regime at entry time (determines management behavior)
	entryRegime Regime

	// Grid orders (range mode only)
	gridOrders []*gridOrder
}

// stagedTPRecord tracks a single TP level for dynamic adjustment and Redis persistence.
type stagedTPRecord struct {
	Level      int     `json:"level"`
	Price      float64 `json:"price"`
	Qty        float64 `json:"qty"`
	ExchangeID string  `json:"exchange_id"`
	Status     string  `json:"status"` // "pending" or "filled"
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

