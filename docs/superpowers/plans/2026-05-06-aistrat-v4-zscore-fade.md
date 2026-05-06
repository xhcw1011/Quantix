# AI Strategy v4 (Z-Score Fade) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `aistrat_v4`, a single-shot z-score fade strategy on ETH 5m, ship as new `ai_v4` registry entry alongside v3, validate via backtest before any deploy.

**Architecture:** Pure functional core (z-score signal, entry/exit decisions, sizing math) wrapped in a thin Strategy interface adapter. Bar-close events only — no tick-level trailing. ≤500 lines total across 5 files.

**Tech Stack:** Go 1.24, existing `internal/strategy` interface, existing `cmd/backtest` binary, `talib` for indicators, exchange-side SL via existing `StagedExitPlacer` interface.

---

## Hard Decision Gates

**Gate 1 (after Task 9 — Baseline Backtest):**
- If `PF >= 1.0` AND `Sharpe > 0` on 105-day backtest → continue to Task 10
- If `PF < 1.0` OR `Sharpe < 0` → **STOP**, write findings to spec append, escalate. Do NOT proceed to grid-search or deploy.

**Gate 2 (after Task 11 — Walk-Forward):**
- If out-of-sample average Sharpe > 0.3 → continue to Task 12
- Else → **STOP**, escalate.

**Gate 3 (after Task 13 — Server Deploy):**
- Stop v3 engine session before starting v4 (hedge mode forbids two engines/same symbol)
- After 2-week live demo: separate decision (not in this plan)

---

## File Structure

New package `internal/strategy/aistrat_v4/`:

| File | Lines (target) | Responsibility |
|---|---|---|
| `config.go` | ~80 | `Config` struct, `DefaultConfig()`, `registry.Register("ai_v4")` |
| `signal.go` | ~50 | `zScore()`, `atr()` — pure math, no state |
| `entry.go` | ~80 | `shouldEnter()` decision + order placement helper |
| `exit.go` | ~80 | `shouldExit()` decision + close order helper |
| `strategy.go` | ~120 | `Strategy` struct, `OnBar`, `OnFill`, internal state |
| `signal_test.go` | — | Z-score and ATR math tests |
| `entry_exit_test.go` | — | Entry/exit decision tests (table-driven) |
| `strategy_test.go` | — | OnBar/OnFill end-to-end with mock context |

Modified files:
- `cmd/api/main.go` — add `_ "github.com/Quantix/quantix/internal/strategy/aistrat_v4"` import
- `cmd/backtest/main.go` — add same import (so `-strategy ai_v4` works)
- `cmd/optimize/main.go` — add same import

---

## Phase 1 — Implementation (Tasks 1–8)

### Task 1: Skeleton, Config, Registry

**Files:**
- Create: `internal/strategy/aistrat_v4/config.go`
- Modify: `cmd/api/main.go` (add import)
- Modify: `cmd/backtest/main.go` (add import)
- Modify: `cmd/optimize/main.go` (add import)

- [ ] **Step 1: Write the failing test**

Create `internal/strategy/aistrat_v4/config_test.go`:

```go
package aistrat_v4

import (
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy/registry"
)

func TestRegistryRegistration(t *testing.T) {
	log := zap.NewNop()
	params := map[string]any{
		"Symbol": "ETHUSDT",
	}
	s, err := registry.Create("ai_v4", params, log)
	if err != nil {
		t.Fatalf("registry.Create returned error: %v", err)
	}
	if s == nil {
		t.Fatal("registry.Create returned nil strategy")
	}
	if name := s.Name(); name == "" {
		t.Fatalf("Name() returned empty")
	}
}

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.Lookback != 20 {
		t.Errorf("Lookback default = %d, want 20", c.Lookback)
	}
	if c.EntryZScore != 2.5 {
		t.Errorf("EntryZScore default = %f, want 2.5", c.EntryZScore)
	}
	if c.StopZScore != 3.5 {
		t.Errorf("StopZScore default = %f, want 3.5", c.StopZScore)
	}
	if c.TimeStopBars != 12 {
		t.Errorf("TimeStopBars default = %d, want 12", c.TimeStopBars)
	}
	if c.CooldownBars != 3 {
		t.Errorf("CooldownBars default = %d, want 3", c.CooldownBars)
	}
	if c.MinATRPct != 0.003 {
		t.Errorf("MinATRPct default = %f, want 0.003", c.MinATRPct)
	}
	if c.RiskPerTrade != 0.005 {
		t.Errorf("RiskPerTrade default = %f, want 0.005", c.RiskPerTrade)
	}
	if c.Leverage != 2 {
		t.Errorf("Leverage default = %f, want 2", c.Leverage)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/strategy/aistrat_v4/... -run TestRegistry -v`
Expected: package not found / undefined symbols.

- [ ] **Step 3: Write minimal implementation**

Create `internal/strategy/aistrat_v4/config.go`:

```go
// Package aistrat_v4 implements a single-shot z-score mean-reversion strategy.
//
// Edge thesis: ETH 5m bars exhibit statistical reversion when |z| >= 2.5
// from SMA(Lookback). Entry on extreme z, exit on z=0 (TP) / |z|>=3.5 (SL) /
// time-stop after TimeStopBars bars. No tick events, no grid layers, no
// regime detection — see docs/superpowers/specs/2026-05-06-aistrat-v4-zscore-fade-design.md.
package aistrat_v4

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// Config holds tunable parameters. Defaults are starting points; real values
// come from grid-search backtest over historical data.
type Config struct {
	Symbol       string  // trading pair, e.g. "ETHUSDT"
	Lookback     int     // SMA / std window in bars (default 20)
	EntryZScore  float64 // |z| threshold to trigger fade (default 2.5)
	StopZScore   float64 // |z| threshold to abort (default 3.5)
	TimeStopBars int     // force market close after N bars (default 12)
	CooldownBars int     // skip same-side entries N bars after close (default 3)
	MinATRPct    float64 // ATR/price floor; below this skip entry (default 0.003)
	RiskPerTrade float64 // fraction of equity at risk per trade (default 0.005)
	Leverage     float64 // exchange leverage multiplier (default 2)
}

// DefaultConfig returns the starting parameter values from the design spec.
func DefaultConfig() Config {
	return Config{
		Lookback:     20,
		EntryZScore:  2.5,
		StopZScore:   3.5,
		TimeStopBars: 12,
		CooldownBars: 3,
		MinATRPct:    0.003,
		RiskPerTrade: 0.005,
		Leverage:     2,
	}
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	}
	return 0
}

func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	}
	return 0
}

func init() {
	registry.Register("ai_v4", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := DefaultConfig()
		if v, ok := params["Symbol"].(string); ok {
			cfg.Symbol = v
		}
		if cfg.Symbol == "" {
			return nil, fmt.Errorf("ai_v4: Symbol required")
		}
		if v, ok := params["Lookback"]; ok {
			cfg.Lookback = toInt(v)
		}
		if v, ok := params["EntryZScore"]; ok {
			cfg.EntryZScore = toFloat(v)
		}
		if v, ok := params["StopZScore"]; ok {
			cfg.StopZScore = toFloat(v)
		}
		if v, ok := params["TimeStopBars"]; ok {
			cfg.TimeStopBars = toInt(v)
		}
		if v, ok := params["CooldownBars"]; ok {
			cfg.CooldownBars = toInt(v)
		}
		if v, ok := params["MinATRPct"]; ok {
			cfg.MinATRPct = toFloat(v)
		}
		if v, ok := params["RiskPerTrade"]; ok {
			cfg.RiskPerTrade = toFloat(v)
		}
		if v, ok := params["Leverage"]; ok {
			cfg.Leverage = toFloat(v)
		}
		return New(cfg, log), nil
	})
}
```

Create `internal/strategy/aistrat_v4/strategy.go` (minimal stub for test to compile):

```go
package aistrat_v4

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// Strategy implements strategy.Strategy.
type Strategy struct {
	cfg Config
	log *zap.Logger
}

// New creates a Strategy with the given config.
func New(cfg Config, log *zap.Logger) *Strategy {
	return &Strategy{cfg: cfg, log: log}
}

// Name returns a human-readable identifier.
func (s *Strategy) Name() string {
	return fmt.Sprintf("AI_v4(z>=%.1f,lb=%d)", s.cfg.EntryZScore, s.cfg.Lookback)
}

// OnBar handles a closed bar. Stub for Task 1; filled in Task 7.
func (s *Strategy) OnBar(_ *strategy.Context, _ exchange.Kline) {}

// OnFill handles fill events. Stub for Task 1; filled in Task 8.
func (s *Strategy) OnFill(_ *strategy.Context, _ strategy.Fill) {}
```

Modify `cmd/api/main.go` after the existing strategy imports (around line 47):

```go
	_ "github.com/Quantix/quantix/internal/strategy/aistrat"
	_ "github.com/Quantix/quantix/internal/strategy/aistrat_v4"  // NEW
	_ "github.com/Quantix/quantix/internal/strategy/grid"
```

Same import added to `cmd/backtest/main.go` (after the existing `_ "github.com/Quantix/quantix/internal/strategy/aistrat"` line) and `cmd/optimize/main.go` (after the same line if present).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/strategy/aistrat_v4/... -v`
Expected: PASS for both `TestRegistryRegistration` and `TestDefaultConfig`.

Run: `go build ./...`
Expected: clean build (no compile errors anywhere).

- [ ] **Step 5: Commit**

```bash
git add internal/strategy/aistrat_v4/ cmd/api/main.go cmd/backtest/main.go cmd/optimize/main.go
git commit -m "feat(aistrat_v4): skeleton + config + registry registration

Adds new strategy package with Config struct, DefaultConfig() per spec,
and registry.Register('ai_v4'). OnBar/OnFill are stubs filled in later tasks.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Z-Score Signal Function

**Files:**
- Create: `internal/strategy/aistrat_v4/signal.go`
- Create: `internal/strategy/aistrat_v4/signal_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/strategy/aistrat_v4/signal_test.go`:

```go
package aistrat_v4

import (
	"math"
	"testing"
)

func TestZScoreInsufficientBars(t *testing.T) {
	closes := []float64{100, 101, 102} // less than lookback
	z := zScore(closes, 20)
	if z != 0 {
		t.Errorf("zScore with insufficient bars = %f, want 0", z)
	}
}

func TestZScoreFlatPrices(t *testing.T) {
	closes := make([]float64, 25)
	for i := range closes {
		closes[i] = 100.0
	}
	z := zScore(closes, 20)
	if z != 0 {
		t.Errorf("zScore with flat prices = %f, want 0 (std=0 case)", z)
	}
}

func TestZScoreKnownInput(t *testing.T) {
	// 21 prices: 20 baseline (100-119), then 130 = clearly above mean
	// SMA of last 20 = mean(100..119) = 109.5
	// std of 100..119 ≈ 5.766
	// z = (130 - 109.5) / 5.766 ≈ 3.55
	closes := make([]float64, 21)
	for i := 0; i < 20; i++ {
		closes[i] = float64(100 + i)
	}
	closes[20] = 130
	z := zScore(closes, 20)
	want := 3.55
	if math.Abs(z-want) > 0.05 {
		t.Errorf("zScore = %f, want approx %f", z, want)
	}
}

func TestZScoreNegative(t *testing.T) {
	// Symmetric: 130→90 with same baseline → z ≈ -3.38
	closes := make([]float64, 21)
	for i := 0; i < 20; i++ {
		closes[i] = float64(100 + i)
	}
	closes[20] = 90
	z := zScore(closes, 20)
	if z >= 0 {
		t.Errorf("zScore for low price = %f, want negative", z)
	}
}

func TestZScoreUsesLastNBars(t *testing.T) {
	// Insert noise early; SMA/std should only see last 20 bars
	closes := []float64{1, 2, 3, 4, 5} // ignored
	for i := 0; i < 20; i++ {
		closes = append(closes, 100.0)
	}
	closes = append(closes, 110.0)
	z := zScore(closes, 20)
	if z <= 0 {
		t.Errorf("zScore = %f, want positive (110 > flat 100)", z)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/strategy/aistrat_v4/... -run TestZScore -v`
Expected: FAIL with "undefined: zScore".

- [ ] **Step 3: Write minimal implementation**

Create `internal/strategy/aistrat_v4/signal.go`:

```go
package aistrat_v4

import "math"

// zScore returns (current - SMA) / std using the last `lookback` bars
// (excluding the current bar from the mean/std window so the z score
// measures how far the current close is from its recent baseline).
//
// Returns 0 if there are fewer than lookback+1 bars or if std == 0.
func zScore(closes []float64, lookback int) float64 {
	n := len(closes)
	if n < lookback+1 || lookback < 2 {
		return 0
	}
	current := closes[n-1]
	window := closes[n-1-lookback : n-1] // exclude current
	mean := 0.0
	for _, v := range window {
		mean += v
	}
	mean /= float64(lookback)
	sumSq := 0.0
	for _, v := range window {
		d := v - mean
		sumSq += d * d
	}
	variance := sumSq / float64(lookback)
	std := math.Sqrt(variance)
	if std == 0 {
		return 0
	}
	return (current - mean) / std
}

// atr computes Average True Range over the last `period` bars from highs/lows/closes.
// Returns 0 if there are fewer than period+1 bars.
//
// True range for bar i: max(high - low, |high - prevClose|, |low - prevClose|)
// ATR = simple average of true ranges over period.
func atr(highs, lows, closes []float64, period int) float64 {
	n := len(closes)
	if n < period+1 || period < 1 {
		return 0
	}
	sum := 0.0
	for i := n - period; i < n; i++ {
		hl := highs[i] - lows[i]
		hc := math.Abs(highs[i] - closes[i-1])
		lc := math.Abs(lows[i] - closes[i-1])
		tr := math.Max(hl, math.Max(hc, lc))
		sum += tr
	}
	return sum / float64(period)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/strategy/aistrat_v4/... -run TestZScore -v`
Expected: PASS for all 5 z-score tests.

- [ ] **Step 5: Commit**

```bash
git add internal/strategy/aistrat_v4/signal.go internal/strategy/aistrat_v4/signal_test.go
git commit -m "feat(aistrat_v4): zScore() and atr() pure math helpers

zScore measures distance of current close from SMA of last N bars in
units of std. Returns 0 on insufficient bars or zero variance.

atr is standard True Range average. Both are pure functions, no state,
fully unit tested.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: ATR Helper Tests

**Files:**
- Modify: `internal/strategy/aistrat_v4/signal_test.go`

- [ ] **Step 1: Add ATR tests**

Append to `signal_test.go`:

```go
func TestATRInsufficientBars(t *testing.T) {
	highs := []float64{105, 106}
	lows := []float64{95, 96}
	closes := []float64{100, 101}
	a := atr(highs, lows, closes, 14)
	if a != 0 {
		t.Errorf("atr with insufficient bars = %f, want 0", a)
	}
}

func TestATRKnownInput(t *testing.T) {
	// 15 bars where each has range 10 and consecutive closes flat at 100.
	// True range = high - low = 10 for every bar (no gap from prevClose).
	// ATR = 10.
	highs := make([]float64, 15)
	lows := make([]float64, 15)
	closes := make([]float64, 15)
	for i := range closes {
		highs[i] = 105
		lows[i] = 95
		closes[i] = 100
	}
	a := atr(highs, lows, closes, 14)
	if math.Abs(a-10) > 0.001 {
		t.Errorf("atr = %f, want 10.0", a)
	}
}

func TestATRWithGaps(t *testing.T) {
	// 15 bars: bar 0 close 100, bar 1 high 110 low 105 close 108
	// (gap up: prevClose 100, high 110 → TR = max(5, 10, 5) = 10)
	highs := []float64{100}
	lows := []float64{100}
	closes := []float64{100}
	for i := 1; i < 15; i++ {
		highs = append(highs, 110)
		lows = append(lows, 105)
		closes = append(closes, 108)
	}
	a := atr(highs, lows, closes, 14)
	// First TR is 10 (gap), subsequent TRs are 5 (no gap, prev close 108 in range).
	// Avg = (10 + 13*5) / 14 = 75/14 ≈ 5.357
	if math.Abs(a-5.357) > 0.05 {
		t.Errorf("atr with gap = %f, want approx 5.357", a)
	}
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/strategy/aistrat_v4/... -run TestATR -v`
Expected: PASS for all 3 ATR tests.

- [ ] **Step 3: Commit**

```bash
git add internal/strategy/aistrat_v4/signal_test.go
git commit -m "test(aistrat_v4): add atr() tests covering insufficient bars, flat, gaps

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Entry Decision Function

**Files:**
- Create: `internal/strategy/aistrat_v4/entry.go`
- Create: `internal/strategy/aistrat_v4/entry_exit_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/strategy/aistrat_v4/entry_exit_test.go`:

```go
package aistrat_v4

import (
	"testing"
)

// barsAtZ builds (highs, lows, closes) such that the latest close has the given z-score
// from a flat SMA-window of `mean` price with given `std`.
func barsAtZ(t *testing.T, lookback int, mean, std, targetZ float64) (h, l, c []float64) {
	t.Helper()
	c = make([]float64, lookback+1)
	h = make([]float64, lookback+1)
	l = make([]float64, lookback+1)
	// Build window with std deviation: alternate (mean+std) (mean-std)
	for i := 0; i < lookback; i++ {
		if i%2 == 0 {
			c[i] = mean + std
		} else {
			c[i] = mean - std
		}
		h[i] = c[i] + 0.5
		l[i] = c[i] - 0.5
	}
	c[lookback] = mean + targetZ*std
	h[lookback] = c[lookback] + 0.5
	l[lookback] = c[lookback] - 0.5
	return
}

func TestShouldEnterAboveThreshold_ShortSignal(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 3.0) // z = +3 → SHORT signal
	side, reason := shouldEnter(c, h, l, cfg, nil, 0)
	if side != "SHORT" {
		t.Errorf("side = %q (reason=%s), want SHORT", side, reason)
	}
}

func TestShouldEnterBelowThreshold_LongSignal(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, -3.0) // z = -3 → LONG signal
	side, reason := shouldEnter(c, h, l, cfg, nil, 0)
	if side != "LONG" {
		t.Errorf("side = %q (reason=%s), want LONG", side, reason)
	}
}

func TestShouldEnterBelowZThreshold_NoSignal(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 1.5) // z = +1.5 (below 2.5 threshold)
	side, _ := shouldEnter(c, h, l, cfg, nil, 0)
	if side != "" {
		t.Errorf("side = %q, want empty (z below threshold)", side)
	}
}

func TestShouldEnterPositionExists_NoSignal(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 3.0)
	pos := &positionState{Side: "LONG"} // already in a position
	side, _ := shouldEnter(c, h, l, cfg, pos, 0)
	if side != "" {
		t.Errorf("side = %q, want empty (already in position)", side)
	}
}

func TestShouldEnterATRFloorBlocks(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	cfg.MinATRPct = 0.05 // 5% — way above what flat alternating bars produce
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 3.0)
	side, reason := shouldEnter(c, h, l, cfg, nil, 0)
	if side != "" {
		t.Errorf("side = %q (reason=%s), want empty (ATR floor block)", side, reason)
	}
}

func TestShouldEnterCooldownBlocksSameSide(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	cfg.CooldownBars = 5
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 3.0) // z = +3 → SHORT
	cooldownState := cooldown{LastShortCloseBar: 18}
	side, _ := shouldEnter(c, h, l, cfg, nil, 20, withCooldown(cooldownState))
	if side != "" {
		t.Errorf("side = %q, want empty (SHORT cooldown active: bar 20 - 18 = 2 < 5)", side)
	}
}

func TestShouldEnterCooldownAllowsOppositeSide(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	cfg.CooldownBars = 5
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, -3.0) // z = -3 → LONG
	cooldownState := cooldown{LastShortCloseBar: 18}  // SHORT cooldown, LONG fine
	side, _ := shouldEnter(c, h, l, cfg, nil, 20, withCooldown(cooldownState))
	if side != "LONG" {
		t.Errorf("side = %q, want LONG (SHORT cooldown does not block LONG)", side)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/strategy/aistrat_v4/... -run TestShouldEnter -v`
Expected: FAIL — `shouldEnter`, `positionState`, `cooldown`, `withCooldown` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/strategy/aistrat_v4/entry.go`:

```go
package aistrat_v4

import "math"

// positionState is the internal record of an open position.
type positionState struct {
	Side          string  // "LONG" or "SHORT"
	EntryPrice    float64
	EntryBar      int
	Qty           float64
	EntryZScore   float64
	StopLossPx    float64
	TakeProfitPx  float64
	OrderID       string // OMS id of the open order, until filled
}

// cooldown tracks per-side cooldown windows (last close bar index for LONG and SHORT).
type cooldown struct {
	LastLongCloseBar  int
	LastShortCloseBar int
}

// shouldEnterOption is a functional option for shouldEnter (keeps signature small for default callers).
type shouldEnterOption func(*shouldEnterCtx)

type shouldEnterCtx struct {
	cd cooldown
}

// withCooldown injects per-side cooldown state.
func withCooldown(cd cooldown) shouldEnterOption {
	return func(c *shouldEnterCtx) { c.cd = cd }
}

// shouldEnter decides whether to open a new position on the current bar.
// Returns (side, reason). side == "" means do not enter; reason describes why.
//
// Inputs:
//   closes / highs / lows  — bar arrays ending at the current closed bar
//   cfg                    — strategy configuration
//   pos                    — current open position, nil if none
//   currentBar             — bar index of the current bar (monotonically increasing)
//   opts                   — optional cooldown state etc.
func shouldEnter(closes, highs, lows []float64, cfg Config, pos *positionState, currentBar int, opts ...shouldEnterOption) (side, reason string) {
	if pos != nil {
		return "", "position_exists"
	}

	c := shouldEnterCtx{}
	for _, opt := range opts {
		opt(&c)
	}

	// Compute z-score and ATR floor
	z := zScore(closes, cfg.Lookback)
	if math.Abs(z) < cfg.EntryZScore {
		return "", "z_below_threshold"
	}

	a := atr(highs, lows, closes, 14)
	if a == 0 {
		return "", "atr_unavailable"
	}
	currentPrice := closes[len(closes)-1]
	if a/currentPrice < cfg.MinATRPct {
		return "", "atr_floor"
	}

	// Determine intended side: positive z (price above mean) → SHORT (fade up); negative z → LONG
	intent := "SHORT"
	if z < 0 {
		intent = "LONG"
	}

	// Cooldown check: if same-side close within CooldownBars, skip
	if cfg.CooldownBars > 0 {
		lastClose := -1
		if intent == "LONG" {
			lastClose = c.cd.LastLongCloseBar
		} else {
			lastClose = c.cd.LastShortCloseBar
		}
		if lastClose > 0 && currentBar-lastClose < cfg.CooldownBars {
			return "", "cooldown"
		}
	}

	return intent, "z_signal"
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/strategy/aistrat_v4/... -run TestShouldEnter -v`
Expected: PASS for all 7 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/strategy/aistrat_v4/entry.go internal/strategy/aistrat_v4/entry_exit_test.go
git commit -m "feat(aistrat_v4): shouldEnter() decision + 7 unit tests

Pure function: returns side + reason. Filters: position-exists, z-below-threshold,
atr-floor, cooldown. Cooldown is per-side (LONG cooldown does not block SHORT).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Exit Decision Function

**Files:**
- Create: `internal/strategy/aistrat_v4/exit.go`
- Modify: `internal/strategy/aistrat_v4/entry_exit_test.go`

- [ ] **Step 1: Write failing tests**

Append to `entry_exit_test.go`:

```go
func TestShouldExitNoPosition(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	closes := []float64{100, 100, 100}
	highs := []float64{101, 101, 101}
	lows := []float64{99, 99, 99}
	close, reason := shouldExit(closes, highs, lows, cfg, nil, 0)
	if close {
		t.Errorf("shouldExit = (true, %s), want (false, _) when no position", reason)
	}
}

func TestShouldExitTPHitFromShort(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	// Build window so current z ≈ 0 (price = SMA)
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 0.0)
	pos := &positionState{Side: "SHORT", EntryBar: 5}
	close, reason := shouldExit(c, h, l, cfg, pos, 10)
	if !close || reason != "tp" {
		t.Errorf("shouldExit = (%v, %s), want (true, tp)", close, reason)
	}
}

func TestShouldExitTPHitFromLong(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 0.0)
	pos := &positionState{Side: "LONG", EntryBar: 5}
	close, reason := shouldExit(c, h, l, cfg, pos, 10)
	if !close || reason != "tp" {
		t.Errorf("shouldExit = (%v, %s), want (true, tp)", close, reason)
	}
}

func TestShouldExitSLHitFromShort(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	// SHORT entry expected price below mean; if z >= +3.5 then price even more above mean → SL
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 3.6)
	pos := &positionState{Side: "SHORT", EntryBar: 5}
	close, reason := shouldExit(c, h, l, cfg, pos, 10)
	if !close || reason != "sl" {
		t.Errorf("shouldExit = (%v, %s), want (true, sl)", close, reason)
	}
}

func TestShouldExitSLHitFromLong(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, -3.6)
	pos := &positionState{Side: "LONG", EntryBar: 5}
	close, reason := shouldExit(c, h, l, cfg, pos, 10)
	if !close || reason != "sl" {
		t.Errorf("shouldExit = (%v, %s), want (true, sl)", close, reason)
	}
}

func TestShouldExitTimeStop(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	cfg.TimeStopBars = 5
	// z is between thresholds, but bar 5 - bar 0 = 5 → time stop
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 1.0)
	pos := &positionState{Side: "SHORT", EntryBar: 0}
	close, reason := shouldExit(c, h, l, cfg, pos, 5)
	if !close || reason != "time" {
		t.Errorf("shouldExit = (%v, %s), want (true, time)", close, reason)
	}
}

func TestShouldExitNoExitConditions(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	// z = 1.0 (below SL), bars held = 2 (below time stop 12)
	h, l, c := barsAtZ(t, cfg.Lookback, 100, 5, 1.0)
	pos := &positionState{Side: "SHORT", EntryBar: 8}
	close, _ := shouldExit(c, h, l, cfg, pos, 10)
	if close {
		t.Error("shouldExit returned true, want false (no condition met)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/strategy/aistrat_v4/... -run TestShouldExit -v`
Expected: FAIL — `shouldExit` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/strategy/aistrat_v4/exit.go`:

```go
package aistrat_v4

import "math"

// shouldExit decides whether to close the open position on the current bar.
// Returns (close, reason). close == false means hold; reason is "" in that case.
// Reasons: "tp" (z returned to 0), "sl" (|z| >= StopZScore), "time" (held too long).
func shouldExit(closes, highs, lows []float64, cfg Config, pos *positionState, currentBar int) (close bool, reason string) {
	if pos == nil {
		return false, ""
	}

	z := zScore(closes, cfg.Lookback)

	// TP: z returned to ~0
	// For SHORT (entered on +z): TP fires when z drops back to <= 0
	// For LONG  (entered on -z): TP fires when z rises back to >= 0
	if pos.Side == "SHORT" && z <= 0 {
		return true, "tp"
	}
	if pos.Side == "LONG" && z >= 0 {
		return true, "tp"
	}

	// SL: |z| crosses StopZScore
	if math.Abs(z) >= cfg.StopZScore {
		return true, "sl"
	}

	// Time stop: held >= TimeStopBars
	if currentBar-pos.EntryBar >= cfg.TimeStopBars {
		return true, "time"
	}

	return false, ""
}

// _ keeps highs/lows in the signature for future use (e.g., intra-bar SL trigger
// using bar.High/Low). Currently unused but reserves the parameter for symmetry
// with shouldEnter.
var _ = func() { _, _, _ = []float64{}, []float64{}, []float64{} }()
```

Note: highs/lows are currently unused in shouldExit — remove the trailing var-stub if your linter complains. They're in the signature for symmetry with shouldEnter and possible v4.1 expansion.

Use this single clean version:

```go
package aistrat_v4

import "math"

// shouldExit decides whether to close the open position on the current bar.
// highs, lows are accepted for signature symmetry with shouldEnter; currently unused.
//
//nolint:unparam // highs/lows reserved for future use (intra-bar SL trigger in v4.1)
func shouldExit(closes, highs, lows []float64, cfg Config, pos *positionState, currentBar int) (close bool, reason string) {
	_ = highs
	_ = lows
	if pos == nil {
		return false, ""
	}
	z := zScore(closes, cfg.Lookback)
	if pos.Side == "SHORT" && z <= 0 {
		return true, "tp"
	}
	if pos.Side == "LONG" && z >= 0 {
		return true, "tp"
	}
	if math.Abs(z) >= cfg.StopZScore {
		return true, "sl"
	}
	if currentBar-pos.EntryBar >= cfg.TimeStopBars {
		return true, "time"
	}
	return false, ""
}
```

(Replace the entire `internal/strategy/aistrat_v4/exit.go` content with the above — discard the earlier `var _ = ...` version shown above it.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/strategy/aistrat_v4/... -run TestShouldExit -v`
Expected: PASS for all 7 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/strategy/aistrat_v4/exit.go internal/strategy/aistrat_v4/entry_exit_test.go
git commit -m "feat(aistrat_v4): shouldExit() decision + 7 unit tests

Three exit reasons: tp (z returns to 0), sl (|z| >= StopZScore), time (bars held).
Pure function, no side effects.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Position Sizing

**Files:**
- Modify: `internal/strategy/aistrat_v4/entry.go`
- Modify: `internal/strategy/aistrat_v4/entry_exit_test.go`

- [ ] **Step 1: Add failing tests**

Append to `entry_exit_test.go`:

```go
func TestCalcQtyNormal(t *testing.T) {
	// equity 5000, risk 0.5%, sl distance $20 → qty = $25 / $20 = 1.25
	q := calcQty(5000, 0.005, 20.0, 100.0, 0.20, 2.0)
	if math.Abs(q-1.25) > 0.001 {
		t.Errorf("calcQty = %f, want 1.25", q)
	}
}

func TestCalcQtyZeroDistance(t *testing.T) {
	// SL distance 0 → fall back to leverage cap
	// equity 5000, leverage 2, price 100 → max qty = 5000 * 0.20 * 2 / 100 = 20
	q := calcQty(5000, 0.005, 0.0, 100.0, 0.20, 2.0)
	if q != 20 {
		t.Errorf("calcQty zero-distance = %f, want 20 (leverage cap)", q)
	}
}

func TestCalcQtyMaxPositionCap(t *testing.T) {
	// equity 5000, risk 5% (huge), sl distance $1, price $100, max position 20%, leverage 2
	// risk-based: 5000 * 0.05 / 1 = 250 qty
	// max:        5000 * 0.20 * 2 / 100 = 20 qty
	// → 20 (cap binds)
	q := calcQty(5000, 0.05, 1.0, 100.0, 0.20, 2.0)
	if q != 20 {
		t.Errorf("calcQty = %f, want 20 (max cap)", q)
	}
}
```

Add to imports of entry_exit_test.go (top of file):
```go
import (
	"math"
	"testing"
)
```
(only `math` is new — likely already there from earlier tests)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/strategy/aistrat_v4/... -run TestCalcQty -v`
Expected: FAIL — `calcQty` undefined.

- [ ] **Step 3: Write minimal implementation**

Append to `entry.go`:

```go
// calcQty returns the order quantity given equity, risk parameters, and price levels.
//
//   equity        — current account equity in quote currency (USD)
//   riskPct       — fraction of equity at risk per trade (e.g. 0.005 = 0.5%)
//   slDistance    — absolute distance from entry to stop (in quote currency / coin price)
//   price         — current price (used for max-position cap)
//   maxPosPct     — max position size as fraction of equity (default 0.20)
//   leverage      — exchange leverage multiplier
//
// Returns qty = min(risk-based, max-position cap). If slDistance == 0, falls back
// to max-position cap only (no R-based sizing possible).
func calcQty(equity, riskPct, slDistance, price, maxPosPct, leverage float64) float64 {
	if equity <= 0 || price <= 0 {
		return 0
	}
	maxCap := equity * maxPosPct * leverage / price
	if slDistance <= 0 {
		return maxCap
	}
	riskBased := equity * riskPct / slDistance
	if riskBased > maxCap {
		return maxCap
	}
	return riskBased
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/strategy/aistrat_v4/... -run TestCalcQty -v`
Expected: PASS for all 3 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/strategy/aistrat_v4/entry.go internal/strategy/aistrat_v4/entry_exit_test.go
git commit -m "feat(aistrat_v4): calcQty() position sizing + 3 tests

R-based sizing capped at maxPosPct × equity × leverage / price. Falls
back to max cap when sl_distance is zero.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Strategy.OnBar Wiring

**Files:**
- Modify: `internal/strategy/aistrat_v4/strategy.go`
- Create: `internal/strategy/aistrat_v4/strategy_test.go`

- [ ] **Step 1: Write failing integration test**

Create `internal/strategy/aistrat_v4/strategy_test.go`:

```go
package aistrat_v4

import (
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// fakePortfolio implements strategy.PortfolioView with fixed equity.
type fakePortfolio struct{ equity float64 }

func (f *fakePortfolio) Cash() float64 { return f.equity }
func (f *fakePortfolio) Equity(_ map[string]float64) float64 { return f.equity }
func (f *fakePortfolio) Position(_ string) (qty, avgEntry float64, ok bool) { return 0, 0, false }
func (f *fakePortfolio) Positions() map[string]any { return nil }

// fakeBroker implements strategy.Broker, capturing orders into a log.
type fakeBroker struct {
	orders []strategy.OrderRequest
	idCnt  int
}

func (b *fakeBroker) PlaceOrder(req strategy.OrderRequest) string {
	b.orders = append(b.orders, req)
	b.idCnt++
	return "OMS-FAKE-" + string(rune('0'+b.idCnt))
}
func (b *fakeBroker) CancelOrder(_ string) error { return nil }

func TestOnBarOpenShortOnHighZ(t *testing.T) {
	log := zap.NewNop()
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	s := New(cfg, log)

	pv := &fakePortfolio{equity: 5000}
	br := &fakeBroker{}
	ctx := strategy.NewContext(pv, br, log)

	// Feed bars: 20 baseline alternating around 100 with std 5,
	// then one bar at 100 + 3*5 = 115 (z = +3 → SHORT signal)
	now := time.Now()
	for i := 0; i < 20; i++ {
		c := 105.0
		if i%2 == 1 {
			c = 95.0
		}
		bar := exchange.Kline{
			Symbol: "ETHUSDT", Time: now.Add(time.Duration(i) * 5 * time.Minute),
			Open: c, High: c + 0.5, Low: c - 0.5, Close: c, Volume: 1,
		}
		s.OnBar(ctx, bar)
	}
	signalBar := exchange.Kline{
		Symbol: "ETHUSDT", Time: now.Add(20 * 5 * time.Minute),
		Open: 100, High: 116, Low: 99.5, Close: 115, Volume: 1,
	}
	s.OnBar(ctx, signalBar)

	if len(br.orders) == 0 {
		t.Fatal("no order placed despite high-z signal")
	}
	o := br.orders[0]
	if o.PositionSide != strategy.PositionSideShort {
		t.Errorf("PositionSide = %v, want SHORT", o.PositionSide)
	}
	if o.Side != strategy.SideSell {
		t.Errorf("Side = %v, want SELL (open SHORT)", o.Side)
	}
	if o.Type != strategy.OrderMarket {
		t.Errorf("Type = %v, want MARKET", o.Type)
	}
	if o.Qty <= 0 {
		t.Errorf("Qty = %f, want positive", o.Qty)
	}
}

func TestOnBarNoOrderBelowThreshold(t *testing.T) {
	log := zap.NewNop()
	cfg := DefaultConfig()
	cfg.Symbol = "ETHUSDT"
	s := New(cfg, log)

	pv := &fakePortfolio{equity: 5000}
	br := &fakeBroker{}
	ctx := strategy.NewContext(pv, br, log)

	now := time.Now()
	for i := 0; i < 21; i++ {
		c := 100.0 + float64(i%2) // tiny variation, low z
		bar := exchange.Kline{
			Symbol: "ETHUSDT", Time: now.Add(time.Duration(i) * 5 * time.Minute),
			Open: c, High: c + 0.5, Low: c - 0.5, Close: c, Volume: 1,
		}
		s.OnBar(ctx, bar)
	}

	if len(br.orders) > 0 {
		t.Errorf("got %d orders, want 0 (no signal)", len(br.orders))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/strategy/aistrat_v4/... -run TestOnBar -v`
Expected: FAIL — current `OnBar` is a stub, no orders placed.

- [ ] **Step 3: Write OnBar implementation**

Replace `internal/strategy/aistrat_v4/strategy.go` with:

```go
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

// Name returns a human-readable identifier (used in logs and metrics).
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

	// 1. Check exit conditions on existing position
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

	// 2. Check entry conditions when flat
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

// placeOpenOrder places a market open order and records pending position state.
func (s *Strategy) placeOpenOrder(ctx *strategy.Context, side string, price, z float64) {
	// SL price: distance such that exit at |z|=StopZScore (1 std past entry)
	a := atr(s.highs, s.lows, s.closes, 14)
	if a == 0 {
		s.log.Warn("SIG_v4: skip entry — ATR is zero")
		return
	}
	// Use ATR as a proxy for std for SL/TP price calculation in the order itself
	// (z-score uses Lookback std; ATR is similar magnitude, simpler from OHLC).
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
		tpPrice = price + s.cfg.EntryZScore*a // back to mean = +EntryZ * std distance
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

// placeCloseOrder places a market close order and records the close into cooldown.
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
// from bar events and broker confirmations; no exchange-side recovery logic
// needed for MVP. Placeholder for future v4.1 enhancements.
func (s *Strategy) OnFill(_ *strategy.Context, _ strategy.Fill) {}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/strategy/aistrat_v4/... -v`
Expected: PASS for all tests including new TestOnBar tests.

Run: `go build ./...`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/strategy/aistrat_v4/strategy.go internal/strategy/aistrat_v4/strategy_test.go
git commit -m "feat(aistrat_v4): OnBar dispatch + open/close order placement

Bar buffer rolling at maxBarBuffer=256. Each bar: check exit on existing
position, then check entry when flat. Open uses calcQty for sizing.
Per-side cooldown updated on close. No tick events.

Integration tests verify SHORT signal triggers SELL+SHORT order, low-z
bars produce no order.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Smoke Test for Build + Registry Across Binaries

**Files:**
- (no new files; verification only)

- [ ] **Step 1: Verify all three binaries build with v4 registered**

Run: `go build ./cmd/api ./cmd/backtest ./cmd/optimize`
Expected: clean, no errors.

- [ ] **Step 2: Run all v4 unit tests**

Run: `go test ./internal/strategy/aistrat_v4/... -v -count=1`
Expected: PASS, no skipped tests.

- [ ] **Step 3: Confirm full test suite still passes**

Run: `go test ./... -count=1 -timeout 120s 2>&1 | tail -30`
Expected: PASS or pre-existing failures only (e.g., the `TestLiveBroker_DuplicateOrderBlocked` flake we noted on 4/30 — that one is pre-existing, unrelated).

If a NEW failure appears anywhere, debug and fix before proceeding.

- [ ] **Step 4: Commit a checkpoint marker (no code change, just note)**

```bash
git commit --allow-empty -m "checkpoint(aistrat_v4): Phase 1 complete — implementation + unit tests

Phase 1 deliverable: aistrat_v4 package with config, signal, entry, exit,
sizing, OnBar wiring. All unit + integration tests pass. No live impact.

Next: Phase 2 — backtest validation (Tasks 9-11).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase 2 — Validation (Tasks 9–11) ← HARD GATE

### Task 9: Baseline Backtest

**Files:**
- (no new files; uses existing cmd/backtest binary)

- [ ] **Step 1: Build backtest binary**

Run:
```bash
go build -o ./bin/backtest ./cmd/backtest
```
Expected: clean. Binary at `./bin/backtest`.

- [ ] **Step 2: Run baseline backtest with default v4 params**

Run:
```bash
./bin/backtest -strategy ai_v4 -symbol ETHUSDT -interval 5m \
  -start 2026-01-21 -end 2026-05-06 \
  -capital 5000 -fee 0.0004 -slippage 0.0001 \
  -params '{"Symbol":"ETHUSDT"}' \
  -out-json /tmp/qbt/v4_baseline.json
```

Expected output: a backtest report with PF, Sharpe, MaxDD, Total Return, Trade Count.

- [ ] **Step 3: Examine the result**

Read the report. Specifically look at:
- **Profit Factor (PF)**: total wins / total losses
- **Sharpe Ratio**: risk-adjusted return
- **Total Return %**: bottom line over 105 days
- **Total Trades**: should be 30-200 range (less = sample too small; more = over-trading)
- **Win Rate**: should be ~55-70% if z-score thesis holds

Example acceptable result:
```
Total Return     +X.XX%
Sharpe Ratio     0.5+
Profit Factor    1.10+
Max Drawdown     <8%
Total Trades     50-150
Win Rate         55-70%
```

- [ ] **Step 4: HARD GATE — decide whether to proceed**

**If PF >= 1.0 AND Sharpe > 0:**
- Document the exact numbers in `docs/superpowers/specs/2026-05-06-aistrat-v4-zscore-fade-design.md` under a new "## Baseline Backtest Result" section
- Continue to Task 10

**If PF < 1.0 OR Sharpe < 0:**
- **STOP**. Do NOT proceed to grid-search or deploy.
- Append a "## Validation Failure (2026-XX-XX)" section to the spec documenting:
  - The numbers
  - Possible reasons (thesis weak? bug in implementation? bad params?)
  - Whether to scrap, tune, or pivot to another archetype
- Notify user with specific numbers and ask for direction.

- [ ] **Step 5: Commit baseline report**

```bash
mkdir -p reports/v4_validation
cp /tmp/qbt/v4_baseline.json reports/v4_validation/baseline.json
git add reports/v4_validation/baseline.json docs/superpowers/specs/2026-05-06-aistrat-v4-zscore-fade-design.md
git commit -m "validate(aistrat_v4): baseline backtest 2026-01-21 → 2026-05-06

[Include actual numbers in commit body, e.g.:]
Result: Return X%, Sharpe X, PF X, Trades N, Win% X
Gate: [PASS / FAIL]

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: Grid-Search Script

**Files:**
- Create: `scripts/v4_gridsearch.sh`

Note: Only run this task if Task 9 gate passes.

- [ ] **Step 1: Write the grid-search shell script**

Create `scripts/v4_gridsearch.sh`:

```bash
#!/bin/bash
# v4 parameter grid-search over backtest.
# Tries combinations of Lookback, EntryZScore, StopZScore, TimeStopBars
# and writes summary CSV.

set -e

OUT_DIR="${OUT_DIR:-/tmp/qbt/v4_grid}"
mkdir -p "$OUT_DIR"
SUMMARY="$OUT_DIR/summary.csv"
echo "lookback,entry_z,stop_z,time_stop,return_pct,sharpe,max_dd_pct,trades,win_rate,profit_factor" > "$SUMMARY"

START="${START:-2026-01-21}"
END="${END:-2026-05-06}"
CAPITAL="${CAPITAL:-5000}"
FEE="${FEE:-0.0004}"
SLIP="${SLIP:-0.0001}"

LOOKBACKS=(10 20 30 50)
ENTRY_ZS=(2.0 2.5 3.0)
STOP_ZS=(3.0 3.5 4.0)
TIME_STOPS=(6 12 24)

count=0
total=$(( ${#LOOKBACKS[@]} * ${#ENTRY_ZS[@]} * ${#STOP_ZS[@]} * ${#TIME_STOPS[@]} ))

for lb in "${LOOKBACKS[@]}"; do
  for ez in "${ENTRY_ZS[@]}"; do
    for sz in "${STOP_ZS[@]}"; do
      # skip nonsensical combos: stop must be > entry
      if (( $(echo "$sz <= $ez" | bc -l) )); then
        continue
      fi
      for ts in "${TIME_STOPS[@]}"; do
        count=$((count + 1))
        out="$OUT_DIR/lb${lb}_ez${ez}_sz${sz}_ts${ts}.json"
        echo "[$count/$total] lb=$lb ez=$ez sz=$sz ts=$ts"
        ./bin/backtest -strategy ai_v4 -symbol ETHUSDT -interval 5m \
          -start "$START" -end "$END" -capital "$CAPITAL" \
          -fee "$FEE" -slippage "$SLIP" \
          -params "{\"Symbol\":\"ETHUSDT\",\"Lookback\":$lb,\"EntryZScore\":$ez,\"StopZScore\":$sz,\"TimeStopBars\":$ts}" \
          -out-json "$out" > /dev/null 2>&1 || { echo "  FAILED, skip"; continue; }

        # Parse and append to CSV
        python3 -c "
import json
d = json.load(open('$out'))
m = d.get('metrics', d)
print(f\"$lb,$ez,$sz,$ts,{m.get('total_return_pct',0):.4f},{m.get('sharpe_ratio',0):.4f},{m.get('max_drawdown_pct',0):.4f},{m.get('total_trades',0)},{m.get('win_rate',0):.4f},{m.get('profit_factor',0):.4f}\")
" >> "$SUMMARY"
      done
    done
  done
done

echo
echo "=== TOP 10 by Sharpe (PF > 1.0 only) ==="
python3 -c "
import csv
rows = []
with open('$SUMMARY') as f:
    r = csv.DictReader(f)
    for row in r:
        try:
            if float(row['profit_factor']) >= 1.0:
                rows.append(row)
        except: pass
rows.sort(key=lambda r: float(r['sharpe']), reverse=True)
hdr = ['lookback','entry_z','stop_z','time_stop','return_pct','sharpe','max_dd_pct','trades','profit_factor']
print(' | '.join(f'{h:>9}' for h in hdr))
for r in rows[:10]:
    print(' | '.join(f'{r[h]:>9}' for h in hdr))
"
```

- [ ] **Step 2: Make executable**

```bash
chmod +x scripts/v4_gridsearch.sh
```

- [ ] **Step 3: Run grid search**

Run: `./scripts/v4_gridsearch.sh 2>&1 | tee /tmp/qbt/v4_grid/run.log`
Expected: ~96 backtest runs (4 × 3 × 3 × 3 minus invalid combos). Roughly 10-30 minutes total wall time.

- [ ] **Step 4: Pick best combo**

The script's last block prints "TOP 10 by Sharpe (PF > 1.0 only)". Pick the row with:
- Best Sharpe
- AND MaxDD < 12%
- AND Trades > 30 (avoid lucky 5-trade outliers)

If multiple rows are very close, prefer the one with **larger Lookback** (more robust, less noise-fitting).

- [ ] **Step 5: Document chosen params**

Append to `docs/superpowers/specs/2026-05-06-aistrat-v4-zscore-fade-design.md`:

```markdown
## Grid-Search Result (2026-XX-XX)

Top combo selected:
- Lookback: X
- EntryZScore: X
- StopZScore: X
- TimeStopBars: X

Metrics: Return X%, Sharpe X, PF X, MaxDD X%, Trades N, Win% X.

Full grid summary: reports/v4_validation/grid_summary.csv
```

- [ ] **Step 6: Commit**

```bash
mkdir -p reports/v4_validation
cp /tmp/qbt/v4_grid/summary.csv reports/v4_validation/grid_summary.csv
git add scripts/v4_gridsearch.sh reports/v4_validation/grid_summary.csv docs/superpowers/specs/2026-05-06-aistrat-v4-zscore-fade-design.md
git commit -m "validate(aistrat_v4): parameter grid-search + chosen config

Grid: lookback × entry_z × stop_z × time_stop = N combos tested.
Selected: lookback=X entry_z=X stop_z=X time_stop=X.
Result: Sharpe=X PF=X return=X% MaxDD=X%.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 11: Walk-Forward Validation

**Files:**
- (uses cmd/optimize binary if available, else manual chunked backtest)

- [ ] **Step 1: Choose walk-forward windows**

Split 105-day data into 3 windows of ~35 days each:
- Window 1: 2026-01-21 → 2026-02-25 (in-sample)
- Window 2: 2026-02-26 → 2026-04-01 (out-of-sample 1)
- Window 3: 2026-04-02 → 2026-05-06 (out-of-sample 2)

- [ ] **Step 2: Backtest chosen params on Window 2 (out-of-sample 1)**

Substitute the params from Task 10 into the command:
```bash
./bin/backtest -strategy ai_v4 -symbol ETHUSDT -interval 5m \
  -start 2026-02-26 -end 2026-04-01 \
  -capital 5000 -fee 0.0004 -slippage 0.0001 \
  -params '{"Symbol":"ETHUSDT","Lookback":<X>,"EntryZScore":<X>,"StopZScore":<X>,"TimeStopBars":<X>}' \
  -out-json /tmp/qbt/v4_oos1.json
```

Read the report. Note Sharpe.

- [ ] **Step 3: Backtest chosen params on Window 3 (out-of-sample 2)**

```bash
./bin/backtest -strategy ai_v4 -symbol ETHUSDT -interval 5m \
  -start 2026-04-02 -end 2026-05-06 \
  -capital 5000 -fee 0.0004 -slippage 0.0001 \
  -params '{"Symbol":"ETHUSDT","Lookback":<X>,"EntryZScore":<X>,"StopZScore":<X>,"TimeStopBars":<X>}' \
  -out-json /tmp/qbt/v4_oos2.json
```

- [ ] **Step 4: HARD GATE — out-of-sample average Sharpe**

Compute `avg_oos_sharpe = (sharpe_oos1 + sharpe_oos2) / 2`.

**If avg_oos_sharpe > 0.3:** continue to Task 12.

**Else:** STOP. The chosen params are over-fit to Window 1. Append findings to spec under "## Walk-Forward Failure" section, escalate to user. Options: re-run grid-search with stricter criteria (Sharpe > 1 in all windows simultaneously) or scrap v4.

- [ ] **Step 5: Commit walk-forward report**

```bash
cp /tmp/qbt/v4_oos1.json /tmp/qbt/v4_oos2.json reports/v4_validation/
git add reports/v4_validation/v4_oos*.json docs/superpowers/specs/2026-05-06-aistrat-v4-zscore-fade-design.md
git commit -m "validate(aistrat_v4): walk-forward 2 OOS windows

OOS Window 2 (Feb 26 - Apr 01): Sharpe X
OOS Window 3 (Apr 02 - May 06): Sharpe X
Avg OOS Sharpe: X
Gate: [PASS/FAIL]

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase 3 — Deploy (Tasks 12–13)

### Task 12: Pre-Deploy Server State Capture

**Files:**
- (no new files; safety procedure)

- [ ] **Step 1: SSH to server, capture current v3 state**

Run:
```bash
ssh -i ~/work/pem/calvin.chan_zttrust_go_20250821.pem ubuntu@54.46.102.153 \
  'echo "=== current equity ==="
   sudo tail -50 /opt/quantix/logs/quantix-*.log | grep engine_status | tail -1
   echo ""
   echo "=== open positions ==="
   sudo -u postgres psql -d quantix -c "SELECT user_id, engine_id, request_json->\"strategy_id\" AS strat, is_active FROM engine_sessions;"'
```

Note the equity value, position count, and current strategy_id.

- [ ] **Step 2: Stop v3 engine via API**

Run:
```bash
TOKEN=$(ssh -i ~/work/pem/calvin.chan_zttrust_go_20250821.pem ubuntu@54.46.102.153 \
  "printf '{\"username\":\"stresstest\",\"password\":\"StressTest123!\"}' | curl -s -X POST http://localhost:9300/api/auth/login -H 'Content-Type: application/json' -d @- | python3 -c 'import sys,json;print(json.load(sys.stdin)[\"token\"])'")

ssh -i ~/work/pem/calvin.chan_zttrust_go_20250821.pem ubuntu@54.46.102.153 \
  "curl -s -X POST -H 'Authorization: Bearer $TOKEN' http://localhost:9300/api/engine/stop -H 'Content-Type: application/json' -d '{\"engine_id\":\"ETHUSDT-5m-ai\"}'"
```

Expected: `{"message":"engine stopped"}` or similar.

- [ ] **Step 3: Wait for any open v3 positions to close cleanly**

```bash
ssh -i ~/work/pem/calvin.chan_zttrust_go_20250821.pem ubuntu@54.46.102.153 \
  "curl -s -H 'Authorization: Bearer $TOKEN' http://localhost:9300/api/positions"
```

Expected: positions array empty, OR you decide to manually close any leftover via the API.

If positions are still open and you want them closed, run:
```bash
ssh -i ~/work/pem/calvin.chan_zttrust_go_20250821.pem ubuntu@54.46.102.153 \
  "curl -s -X POST -H 'Authorization: Bearer $TOKEN' http://localhost:9300/api/positions/close-all"
```
(This endpoint may need verification — fallback: close manually via Binance UI.)

- [ ] **Step 4: Mark v3 session inactive in DB**

```bash
ssh -i ~/work/pem/calvin.chan_zttrust_go_20250821.pem ubuntu@54.46.102.153 \
  "sudo -u postgres psql -d quantix -c \"UPDATE engine_sessions SET is_active=false, stopped_at=NOW() WHERE user_id=4 AND engine_id='ETHUSDT-5m-ai';\""
```

This prevents v3 from auto-restarting after the next deploy.

- [ ] **Step 5: Commit a deploy-prep note (no code)**

```bash
git commit --allow-empty -m "ops(aistrat_v4): v3 engine stopped on server, ready for v4 deploy

Captured pre-deploy v3 state: equity \$X, positions N.
v3 session marked is_active=false to prevent auto-restart.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 13: Deploy v4 Binary and Start Engine

**Files:**
- (uses existing `deploy/deploy.sh`)

- [ ] **Step 1: Build + push v4-enabled binary**

Run:
```bash
SSH_KEY=/Users/apexis-backdesk/work/pem/calvin.chan_zttrust_go_20250821.pem \
  ./deploy/deploy.sh --binary-only
```

Expected: `Binary updated. Done.` and final health check returns `healthy`.

- [ ] **Step 2: Verify v4 strategy is registered on server**

Run:
```bash
TOKEN=$(ssh -i ~/work/pem/calvin.chan_zttrust_go_20250821.pem ubuntu@54.46.102.153 \
  "printf '{\"username\":\"stresstest\",\"password\":\"StressTest123!\"}' | curl -s -X POST http://localhost:9300/api/auth/login -H 'Content-Type: application/json' -d @- | python3 -c 'import sys,json;print(json.load(sys.stdin)[\"token\"])'")

ssh -i ~/work/pem/calvin.chan_zttrust_go_20250821.pem ubuntu@54.46.102.153 \
  "curl -s -H 'Authorization: Bearer $TOKEN' http://localhost:9300/api/strategies"
```

Expected: response contains an entry for `"ai_v4"` alongside `"ai"`, `"grid"`, `"meanreversion"`, etc.

- [ ] **Step 3: Start v4 engine via API with chosen params**

Substitute params from Task 10:
```bash
PARAMS='{"Symbol":"ETHUSDT","Lookback":<X>,"EntryZScore":<X>,"StopZScore":<X>,"TimeStopBars":<X>,"CooldownBars":3,"MinATRPct":0.003,"RiskPerTrade":0.005,"Leverage":2}'

ssh -i ~/work/pem/calvin.chan_zttrust_go_20250821.pem ubuntu@54.46.102.153 \
  "printf '{
    \"credential_id\": 3,
    \"strategy_id\": \"ai_v4\",
    \"symbol\": \"ETHUSDT\",
    \"interval\": \"5m\",
    \"mode\": \"live\",
    \"confirm_live\": true,
    \"params\": $PARAMS,
    \"risk\": {\"max_position_pct\":0.20,\"max_drawdown_pct\":0.10,\"max_single_loss_pct\":0.02}
  }' | curl -s --max-time 60 -X POST http://localhost:9300/api/engine/start \
    -H 'Authorization: Bearer $TOKEN' \
    -H 'Content-Type: application/json' -d @-"
```

Expected: `{"engine_id":"ETHUSDT-5m-ai_v4", "status":"started"}` or similar.

- [ ] **Step 4: Watch first 5 minutes of v4 in live**

```bash
ssh -i ~/work/pem/calvin.chan_zttrust_go_20250821.pem ubuntu@54.46.102.153 \
  "sudo tail -f /opt/quantix/logs/quantix-\$(date +%Y%m%d).log | grep -E 'SIG_v4|engine_status|ERROR'" &
sleep 300
kill %1 2>/dev/null
```

Verify:
- "engine started" log line for `ETHUSDT-5m-ai_v4`
- `SIG_v4` log lines appear (z-score signal computation)
- No ERROR lines
- engine_status snapshots show `wallet_balance` matching what we expected

- [ ] **Step 5: Confirm TG notification chain still works**

```bash
ssh -i ~/work/pem/calvin.chan_zttrust_go_20250821.pem ubuntu@54.46.102.153 \
  "curl -s -X POST -H 'Authorization: Bearer $TOKEN' http://localhost:9300/api/users/me/notifications/test"
```

Expected: TG message arrives on user's phone.

- [ ] **Step 6: Commit deploy notes + close out plan**

```bash
git commit --allow-empty -m "deploy(aistrat_v4): v4 live on server with chosen params

Engine: ETHUSDT-5m-ai_v4
Params: lookback=X entry_z=X stop_z=X time_stop=X
Initial equity: \$X
v3 was previously stopped (session is_active=false).

Next: 2-week observation period. Decision (v4 promote / kill) is a
separate operational task, not part of this implementation plan.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## End State

After all 13 tasks:
- New strategy package `internal/strategy/aistrat_v4/` with full unit + integration test coverage
- Backtest validated: PF > 1.0, Sharpe > 0, OOS Sharpe > 0.3
- v4 deployed to server, v3 stopped
- Reports archived under `reports/v4_validation/`
- Spec doc updated with actual chosen params and validation results

The "promote v4 / kill v4" decision after 2 weeks of live observation is **out of scope for this plan** — it's an operational decision that needs the live data this plan doesn't yet have.

---

## Rollback Procedure (if v4 misbehaves in live)

If at any point v4 in live shows worse behavior than expected (rapid loss, errors flooding logs, unexpected behavior):

1. Stop v4: `curl -s -X POST -H "Authorization: Bearer $TOKEN" http://...:9300/api/engine/stop -d '{"engine_id":"ETHUSDT-5m-ai_v4"}'`
2. Mark v4 session inactive in DB
3. Re-activate v3 session: `UPDATE engine_sessions SET is_active=true WHERE engine_id='ETHUSDT-5m-ai';`
4. Restart server: `sudo systemctl restart quantix-api`
5. Verify v3 came back via `engine_status` log

The previous binary that ran v3 is still on the server as `/opt/quantix/bin/quantix-api.prev` (created by `deploy/deploy.sh --binary-only`). To roll back the binary itself:
```bash
ssh ... 'sudo mv /opt/quantix/bin/quantix-api.prev /opt/quantix/bin/quantix-api && sudo systemctl restart quantix-api'
```
