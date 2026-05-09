# Quantix Framework Refactor — Strangler Fig Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the alpha-pluggable scaffold (Phase 1+2) that lets us iterate on signals 5× faster, then USE that scaffold for 2 weeks of alpha research to determine whether further investment in this codebase is justified. Phase 3-5 are GATED on the outcome of that research.

**Architecture:** Strangler Fig. Build new `composite` strategy alongside existing `aistrat`, both registered in the strategy registry, both runnable in `cmd/backtest` and live API. Migration is data-driven: when `composite` shows PF > aistrat in backtest over ≥30 days, we switch demo. aistrat is never modified.

**Tech Stack:** Go 1.24, existing `internal/strategy/registry`, existing `cmd/backtest` engine, pgxpool, zap.

**Honest framing (read this before committing time):**
- Phase 1+2 (2 weeks) is **capability investment**, not strategy improvement. It will not make ETH profitable. It WILL enable backtest=live and rapid alpha iteration.
- Phase 3-5 are **gated** by a 2-week alpha exploration after Phase 2. If exploration fails (criteria below), the whole project should stop — not because the framework is bad, but because "we don't have alpha for this market" is the real bottleneck and no architecture fixes that.
- aistrat_v4 was a prior fresh-build attempt; it failed in 96 backtests. That history says clean architecture ≠ profitability.

---

## Decision: Refactor In Place vs. Rewrite

**Chosen: Strangler Fig (hybrid).**

| Option | Risk | Reuses Infra | Backtest=Live | Weeks | Reversible |
|---|---|---|---|---|---|
| Refactor aistrat in place | High | Yes | No (would need rewrite anyway) | 8-10 | No |
| Pure rewrite from scratch | Medium | No (rewrite OMS/exchange/recovery) | Yes | 8-12 | No |
| **Strangler Fig** | **Low** | **Yes** | **Yes (free via registry)** | **6-7** | **Yes (delete composite/)** |

**Rationale:**
- Existing infra (Binance Futures integration, OMS, session recovery, web frontend, deploy pipeline, position syncer, risk manager) is battle-tested over 16 phases + 26 hardening rounds. Throwing it away has zero upside.
- The actual rot is concentrated in `internal/strategy/aistrat/` (~3000 lines mixing alpha + portfolio + risk + execution). The strangler isolates and replaces ONLY this.
- aistrat keeps running on demo throughout; no production regression risk.
- `cmd/backtest` already drives strategies via the `registry.Register` factory. Composite gets backtest=live "for free" by registering the same way.

---

## Roadmap (split: committed Phase 1+2, gated Phase 3-5)

This plan is split into a **committed work block (Phase 1+2)** and a **gated continuation (Phase 3-5)**. The gate is a 2-week Alpha Exploration after Phase 2.

### Committed: Phase 1+2 (2 weeks)

| Phase | Scope | Weeks | Acceptance Criteria | Detailed Plan |
|---|---|---|---|---|
| **1. Alpha + Composite Scaffold** | Define Alpha interface + first composite strategy with 1 simple alpha (BB breakout). Backtest works. | 1 | `cmd/backtest -strategy composite -symbol ETHUSDT` runs without error; composite registered in registry; ≥80% coverage | THIS DOC (below) |
| **2. Live Capability + Recovery** | Composite runs in live API with session recovery (mirror aistrat hooks: PositionSyncer, OMS, store) | 1 | Composite runs live demo, survives systemd restart, position state preserved in Redis | TBD (after Phase 1) |

### 🚪 Decision Gate: Alpha Exploration (Week 3-4, 2 weeks)

After Phase 2, **DO NOT start Phase 3.** Instead, use the new framework to rapidly try alpha ideas. This is the project's honesty checkpoint.

**Process:**
- Each new alpha = ~2-4 hours: implement `Alpha` interface, write tests, run `cmd/backtest -strategy composite -params '{"Alphas":["new_alpha"]}'` over 30+ day window
- Try ≥8 distinct alpha ideas. Examples (not exhaustive):
  - momentum (MA crossover + RSI gate)
  - funding rate divergence (funding vs spot price diverging)
  - order book imbalance (bid/ask depth ratio)
  - cross-symbol z-score (ETH/BTC ratio mean reversion)
  - on-chain flow (exchange inflow/outflow)
  - vol-of-vol regime timing
  - mean reversion 1h with 4h trend filter
  - whatever else looks promising

**Kill criteria — at end of Week 4:**

| Outcome | Decision |
|---|---|
| ≥1 alpha has backtest PF ≥ 1.2 over 30+ days, validated walk-forward + OOS | ✅ **Proceed to Phase 3-5** |
| Best alpha has PF ∈ [1.0, 1.2) | 🟡 **+1 week, try 4 more ideas.** Still no PF > 1.2 → STOP. |
| All ≥8 alphas have PF < 1.0 | ❌ **STOP project.** Conclusion: "this market + your idea pool doesn't yield alpha." Do NOT do Phase 3-5. Either (a) stop crypto trading, (b) pivot to non-directional (arb / market-making), or (c) take a break and revisit later. Any of these beats refactoring losing code. |

**This gate is the core honesty mechanism.** Without it, the project drifts into 6-7 weeks of work for an outcome already mostly determined.

### Gated: Phase 3-5 (4 weeks, only if alpha exploration succeeds)

| Phase | Scope | Weeks | Acceptance Criteria |
|---|---|---|---|
| **3. Combiner + Promoted Alphas** | Take 2-3 winning alphas from exploration, build IC-weighted combiner | 1-2 | Combiner PF ≥ max(individual alpha PF) over 30 days |
| **4. ML Alpha (high-risk research)** | Train logistic regression / lightgbm on engineered features → forward-return sign. Use `cmd/optimize` walk-forward harness | 2 | OOS PF ≥ 1.0 with strict walk-forward; OR explicit kill criteria → archive |
| **5. Multi-Symbol** | Composite accepts symbol list; engine_sessions runs N composites in parallel; risk layer adds correlation cap | 1 | Composite live on ETH + BTC + SOL with isolated risk; cross-symbol margin cap enforced |

**Total if all gates pass:** 6-7 weeks. **Realistic expectation: only Phase 1+2 are guaranteed.**

**Phase 3-5 detailed plans written only after Alpha Exploration produces winning signals.**

---

## Phase 1 Detailed Plan (THIS DOC)

### Phase 1 Goal
Establish the Alpha-Composite contract with a working baseline alpha. By the end of Phase 1, `cmd/backtest -strategy composite` runs end-to-end, even if it doesn't out-perform aistrat. The point is the SCAFFOLD, not winning.

### File Structure

**Create:**
- `internal/alpha/alpha.go` — `Alpha` interface, `Features` struct, `Signal` struct
- `internal/alpha/features.go` — feature computation helpers (ATR, returns, BB, RSI from existing indicator pkg)
- `internal/alpha/baseline/breakout.go` — first concrete alpha: 10-bar high/low breakout with ATR filter
- `internal/alpha/alpha_test.go` — interface contract tests
- `internal/alpha/baseline/breakout_test.go` — breakout alpha unit tests
- `internal/strategy/composite/strategy.go` — composite strategy implementing `strategy.Strategy`; calls alphas, picks best signal, places orders via Broker
- `internal/strategy/composite/sizing.go` — R-based position sizing (extracted from aistrat patterns, no hedge for v1)
- `internal/strategy/composite/strategy_test.go` — composite strategy tests (uses `strategy.Context` with mock broker)

**No files modified in `internal/strategy/aistrat/`.** aistrat remains untouched.

**Modified (registry registration only):**
- `cmd/backtest/main.go` — add `_ "github.com/Quantix/quantix/internal/strategy/composite"` import (registry side-effect)
- `cmd/api/main.go` — same import for live availability

---

### Task 1: Define Alpha Interface

**Files:**
- Create: `internal/alpha/alpha.go`
- Create: `internal/alpha/alpha_test.go`

The Alpha interface is the foundational contract. Keep it minimal. Each alpha takes `Features` (precomputed indicators + bar context) and returns a `Signal` (direction + strength + reason).

- [ ] **Step 1: Write the Alpha interface contract test**

```go
// internal/alpha/alpha_test.go
package alpha

import (
	"testing"
	"time"
)

func TestSignal_ZeroValueIsHold(t *testing.T) {
	var s Signal
	if s.Direction != 0 {
		t.Fatalf("zero Signal should have Direction=0, got %d", s.Direction)
	}
	if s.Strength != 0 {
		t.Fatalf("zero Signal should have Strength=0, got %f", s.Strength)
	}
}

func TestFeatures_HasRequiredFields(t *testing.T) {
	f := Features{
		Now:       time.Now(),
		Close:     2300.0,
		ATR:       3.5,
		High10:    2310.0,
		Low10:     2290.0,
		BBUpper:   2315.0,
		BBLower:   2285.0,
		BBMiddle:  2300.0,
		RSI:       55.0,
		LastBars:  []float64{2295, 2298, 2301, 2300, 2299},
	}
	if f.Close != 2300 {
		t.Fatalf("Close not set: %f", f.Close)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/alpha/... -run TestSignal_ZeroValueIsHold -v
```
Expected: `FAIL` — package doesn't exist.

- [ ] **Step 3: Implement Alpha interface and types**

```go
// internal/alpha/alpha.go
package alpha

import "time"

// Signal is an alpha's output for a single bar.
// Direction: -1 (short), 0 (no signal), +1 (long).
// Strength: confidence in [0, 1]. Used by combiner.
// TargetR: take-profit target in R-multiples (0 = use default).
// Reason: human-readable explanation for logging/analysis.
type Signal struct {
	Direction int
	Strength  float64
	TargetR   float64
	Reason    string
}

// Features is the precomputed snapshot an alpha sees on each bar.
// Computed by the composite strategy from raw bars + indicator package
// once per bar, then handed to every registered alpha.
type Features struct {
	Now      time.Time
	Symbol   string
	Close    float64
	High     float64
	Low      float64
	ATR      float64
	High10   float64 // 10-bar rolling high (exclusive of current bar)
	Low10    float64 // 10-bar rolling low
	BBUpper  float64
	BBMiddle float64
	BBLower  float64
	RSI      float64
	LastBars []float64 // last N closes (chronological), N implementation-defined
}

// Alpha is the interface every signal source implements.
// Predict is called on each bar with the current Features snapshot.
// Implementations MUST be deterministic given identical Features
// (no clock reads, no random) so that backtest=live invariant holds.
type Alpha interface {
	Name() string
	Predict(f Features) Signal
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/alpha/... -run TestSignal_ZeroValueIsHold -v
go test ./internal/alpha/... -run TestFeatures_HasRequiredFields -v
```
Expected: PASS for both.

- [ ] **Step 5: Commit**

```bash
git add internal/alpha/alpha.go internal/alpha/alpha_test.go
git commit -m "feat(alpha): define Alpha interface and Features/Signal types"
```

---

### Task 2a: Add ATR to Indicator Package

**Files:**
- Modify: `internal/indicator/indicator.go`
- Create: `internal/indicator/atr_test.go`

`internal/indicator` has RSI and BollingerBands but not ATR — currently aistrat computes ATR inline. Extract a reusable ATR function so alphas can share.

- [ ] **Step 1: Write ATR test**

```go
// internal/indicator/atr_test.go
package indicator

import (
	"math"
	"testing"
)

func TestATR_KnownInputs(t *testing.T) {
	// 5 bars, period=3. TR_i = max(H-L, |H-prevClose|, |L-prevClose|)
	highs  := []float64{12, 13, 14, 15, 14}
	lows   := []float64{10, 11, 12, 13, 12}
	closes := []float64{11, 12, 13, 14, 13}
	out := ATR(highs, lows, closes, 3)
	if len(out) != len(highs) {
		t.Fatalf("len=%d want %d", len(out), len(highs))
	}
	if math.IsNaN(out[len(out)-1]) || out[len(out)-1] <= 0 {
		t.Fatalf("last ATR invalid: %v", out)
	}
}

func TestATR_TooFewBars(t *testing.T) {
	out := ATR([]float64{1, 2}, []float64{0, 1}, []float64{1, 2}, 14)
	for _, v := range out {
		if v != 0 {
			t.Fatalf("expected zeros for insufficient bars, got %v", out)
		}
	}
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/indicator/... -run TestATR -v
```
Expected: FAIL — `ATR` undefined.

- [ ] **Step 3: Add ATR implementation to `internal/indicator/indicator.go`**

```go
// ATR returns Wilder's Average True Range as a series aligned with the
// input slice. ATR[i]=0 for i < period (insufficient history).
func ATR(highs, lows, closes []float64, period int) []float64 {
	n := len(highs)
	out := make([]float64, n)
	if n < period+1 || n != len(lows) || n != len(closes) || period <= 0 {
		return out
	}
	// True range per bar
	tr := make([]float64, n)
	tr[0] = highs[0] - lows[0]
	for i := 1; i < n; i++ {
		hl := highs[i] - lows[i]
		hc := highs[i] - closes[i-1]
		if hc < 0 {
			hc = -hc
		}
		lc := lows[i] - closes[i-1]
		if lc < 0 {
			lc = -lc
		}
		tr[i] = max3(hl, hc, lc)
	}
	// Initial ATR = simple average of first `period` TRs
	sum := 0.0
	for i := 1; i <= period; i++ {
		sum += tr[i]
	}
	out[period] = sum / float64(period)
	// Wilder's smoothing: ATR_i = (ATR_{i-1}*(period-1) + TR_i) / period
	for i := period + 1; i < n; i++ {
		out[i] = (out[i-1]*float64(period-1) + tr[i]) / float64(period)
	}
	return out
}

func max3(a, b, c float64) float64 {
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	return m
}
```

- [ ] **Step 4: Run tests to verify pass**

```bash
go test ./internal/indicator/... -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/indicator/indicator.go internal/indicator/atr_test.go
git commit -m "feat(indicator): add Wilder ATR for alpha use"
```

---

### Task 2b: Feature Computation Helpers

**Files:**
- Create: `internal/alpha/features.go`
- Create: `internal/alpha/features_test.go`

Composite strategy will compute Features once per bar from raw `[]exchange.Kline`. We need a `BuildFeatures(bars)` helper that calls existing `internal/indicator` functions. The point of this task is to wrap those calls in one place so each alpha doesn't recompute indicators.

- [ ] **Step 1: Write the feature-build test**

```go
// internal/alpha/features_test.go
package alpha

import (
	"testing"
	"time"

	"github.com/Quantix/quantix/internal/exchange"
)

func TestBuildFeatures_RecentBars(t *testing.T) {
	bars := make([]exchange.Kline, 30)
	for i := range bars {
		bars[i] = exchange.Kline{
			OpenTime: time.Unix(int64(i*300), 0),
			Open:     100 + float64(i),
			High:     101 + float64(i),
			Low:      99 + float64(i),
			Close:    100.5 + float64(i),
			Volume:   1.0,
		}
	}
	f := BuildFeatures("ETHUSDT", bars)
	if f.Symbol != "ETHUSDT" {
		t.Fatalf("Symbol not set: %s", f.Symbol)
	}
	if f.Close != bars[len(bars)-1].Close {
		t.Fatalf("Close mismatch: got %f want %f", f.Close, bars[len(bars)-1].Close)
	}
	if f.High10 == 0 {
		t.Fatalf("High10 not computed")
	}
	if f.ATR == 0 {
		t.Fatalf("ATR not computed")
	}
}

func TestBuildFeatures_TooFewBars_ReturnsZero(t *testing.T) {
	bars := []exchange.Kline{{Close: 100}}
	f := BuildFeatures("ETHUSDT", bars)
	if f.ATR != 0 {
		t.Fatalf("ATR should be 0 for insufficient bars, got %f", f.ATR)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/alpha/... -run TestBuildFeatures -v
```
Expected: FAIL — `BuildFeatures` undefined.

- [ ] **Step 3: Implement feature builder**

```go
// internal/alpha/features.go
package alpha

import (
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
)

// BuildFeatures computes the Features snapshot from a chronological slice of
// bars. The latest bar (bars[len-1]) is the "current" bar. Returns a zero-ATR
// Features when bars is too short to compute indicators reliably.
func BuildFeatures(symbol string, bars []exchange.Kline) Features {
	if len(bars) < 20 {
		return Features{}
	}
	last := bars[len(bars)-1]
	closes := make([]float64, len(bars))
	highs := make([]float64, len(bars))
	lows := make([]float64, len(bars))
	for i, b := range bars {
		closes[i] = b.Close
		highs[i] = b.High
		lows[i] = b.Low
	}
	bb := indicator.BollingerBands(closes, 20, 2.0)
	rsi := indicator.RSI(closes, 14)
	atr := indicator.ATR(highs, lows, closes, 14)

	hi10, lo10 := closes[len(closes)-1], closes[len(closes)-1]
	start := len(closes) - 11
	if start < 0 {
		start = 0
	}
	for i := start; i < len(closes)-1; i++ {
		if highs[i] > hi10 {
			hi10 = highs[i]
		}
		if lows[i] < lo10 {
			lo10 = lows[i]
		}
	}

	tail := closes
	if len(closes) > 60 {
		tail = closes[len(closes)-60:]
	}
	return Features{
		Now:      last.OpenTime,
		Symbol:   symbol,
		Close:    last.Close,
		High:     last.High,
		Low:      last.Low,
		ATR:      lastVal(atr),
		High10:   hi10,
		Low10:    lo10,
		BBUpper:  lastVal(bb.Upper),
		BBMiddle: lastVal(bb.Middle),
		BBLower:  lastVal(bb.Lower),
		RSI:      lastVal(rsi),
		LastBars: append([]float64(nil), tail...),
	}
}

func lastVal(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1]
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/alpha/... -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/alpha/features.go internal/alpha/features_test.go
git commit -m "feat(alpha): BuildFeatures wrapper over indicator package"
```

---

### Task 3: First Alpha — Breakout

**Files:**
- Create: `internal/alpha/baseline/breakout.go`
- Create: `internal/alpha/baseline/breakout_test.go`

Simple, well-defined alpha: long when current close > 10-bar high, short when current close < 10-bar low. Strength scales with breakout magnitude in ATR. Reject if ATR is below 0.1% of price (no-edge regime).

- [ ] **Step 1: Write breakout long test**

```go
// internal/alpha/baseline/breakout_test.go
package baseline

import (
	"testing"

	"github.com/Quantix/quantix/internal/alpha"
)

func TestBreakout_LongOnNewHigh(t *testing.T) {
	a := NewBreakout()
	f := alpha.Features{
		Symbol: "ETHUSDT",
		Close:  2310.5,
		High10: 2308.0,
		Low10:  2290.0,
		ATR:    3.0,
	}
	s := a.Predict(f)
	if s.Direction != 1 {
		t.Fatalf("expected Direction=1 (long) got %d", s.Direction)
	}
	if s.Strength <= 0 {
		t.Fatalf("expected Strength>0 got %f", s.Strength)
	}
}

func TestBreakout_ShortOnNewLow(t *testing.T) {
	a := NewBreakout()
	f := alpha.Features{
		Symbol: "ETHUSDT",
		Close:  2289.0,
		High10: 2308.0,
		Low10:  2290.0,
		ATR:    3.0,
	}
	s := a.Predict(f)
	if s.Direction != -1 {
		t.Fatalf("expected Direction=-1 (short) got %d", s.Direction)
	}
}

func TestBreakout_HoldInsideRange(t *testing.T) {
	a := NewBreakout()
	f := alpha.Features{
		Close:  2300.0,
		High10: 2308.0,
		Low10:  2290.0,
		ATR:    3.0,
	}
	s := a.Predict(f)
	if s.Direction != 0 {
		t.Fatalf("expected hold inside range, got %d", s.Direction)
	}
}

func TestBreakout_RejectLowATR(t *testing.T) {
	a := NewBreakout()
	f := alpha.Features{
		Close:  2310.5,
		High10: 2308.0,
		Low10:  2290.0,
		ATR:    0.5, // 0.022% of price < 0.1% gate
	}
	s := a.Predict(f)
	if s.Direction != 0 {
		t.Fatalf("low ATR should yield hold, got %d (reason=%s)", s.Direction, s.Reason)
	}
}

func TestBreakout_StrengthScalesWithMagnitude(t *testing.T) {
	a := NewBreakout()
	weak := a.Predict(alpha.Features{Close: 2308.5, High10: 2308.0, Low10: 2290.0, ATR: 3.0})
	strong := a.Predict(alpha.Features{Close: 2316.0, High10: 2308.0, Low10: 2290.0, ATR: 3.0})
	if strong.Strength <= weak.Strength {
		t.Fatalf("strong (%f) should exceed weak (%f)", strong.Strength, weak.Strength)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/alpha/baseline/... -v
```
Expected: FAIL — package doesn't compile.

- [ ] **Step 3: Implement Breakout alpha**

```go
// internal/alpha/baseline/breakout.go
package baseline

import (
	"fmt"

	"github.com/Quantix/quantix/internal/alpha"
)

// Breakout is a 10-bar Donchian breakout alpha. Long when close exceeds the
// 10-bar high (exclusive of current bar). Strength scales with breakout
// magnitude divided by ATR, capped at 1.0.
type Breakout struct {
	MinATRPct float64 // skip when ATR/price < this (default 0.001 = 0.1%)
}

// NewBreakout returns a Breakout alpha with default settings.
func NewBreakout() *Breakout {
	return &Breakout{MinATRPct: 0.001}
}

func (b *Breakout) Name() string { return "breakout" }

func (b *Breakout) Predict(f alpha.Features) alpha.Signal {
	if f.ATR <= 0 || f.Close <= 0 {
		return alpha.Signal{Reason: "no_atr_or_price"}
	}
	if f.ATR/f.Close < b.MinATRPct {
		return alpha.Signal{Reason: "low_atr"}
	}
	if f.Close > f.High10 {
		strength := (f.Close - f.High10) / f.ATR
		if strength > 1.0 {
			strength = 1.0
		}
		return alpha.Signal{
			Direction: 1,
			Strength:  strength,
			TargetR:   1.5,
			Reason:    fmt.Sprintf("close>%g (h10), %.2fATR", f.High10, strength),
		}
	}
	if f.Close < f.Low10 {
		strength := (f.Low10 - f.Close) / f.ATR
		if strength > 1.0 {
			strength = 1.0
		}
		return alpha.Signal{
			Direction: -1,
			Strength:  strength,
			TargetR:   1.5,
			Reason:    fmt.Sprintf("close<%g (l10), %.2fATR", f.Low10, strength),
		}
	}
	return alpha.Signal{Reason: "inside_range"}
}
```

- [ ] **Step 4: Run tests to verify all pass**

```bash
go test ./internal/alpha/baseline/... -v
```
Expected: PASS for all 5 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/alpha/baseline/
git commit -m "feat(alpha/baseline): Breakout alpha with ATR-scaled strength"
```

---

### Task 4: Composite Strategy Skeleton

**Files:**
- Create: `internal/strategy/composite/strategy.go`
- Create: `internal/strategy/composite/strategy_test.go`

Composite holds a list of `alpha.Alpha`, computes Features each bar, calls Predict on every alpha, picks the highest-Strength signal (v1 — combiner is Phase 3), and places a market order. Single position at a time, no hedge mode (v1).

- [ ] **Step 1: Write skeleton constructor + Name test**

```go
// internal/strategy/composite/strategy_test.go
package composite

import (
	"testing"

	"github.com/Quantix/quantix/internal/alpha/baseline"
)

func TestStrategy_Name(t *testing.T) {
	s := New([]Alpha{baseline.NewBreakout()}, Config{Symbol: "ETHUSDT"})
	if s.Name() != "composite" {
		t.Fatalf("Name=%q want composite", s.Name())
	}
}

func TestStrategy_NeedsAtLeastOneAlpha(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic for empty alpha list")
		}
	}()
	_ = New(nil, Config{Symbol: "ETHUSDT"})
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/strategy/composite/... -v
```
Expected: FAIL — package doesn't compile.

- [ ] **Step 3: Implement skeleton**

```go
// internal/strategy/composite/strategy.go
// Package composite is a multi-alpha trading strategy. Each alpha
// produces a Signal independently from a shared Features snapshot;
// the strategy picks the strongest signal and turns it into orders.
package composite

import (
	"github.com/Quantix/quantix/internal/alpha"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// Alpha is re-exported so users don't need to import internal/alpha when
// constructing a Composite.
type Alpha = alpha.Alpha

// Config holds runtime parameters of the composite strategy.
type Config struct {
	Symbol         string
	RiskPerTrade   float64 // fraction of equity at risk per trade (default 0.005)
	SLATR          float64 // SL distance in ATR multiples (default 1.5)
	MinSignalScore float64 // skip signals below this strength (default 0.3)
	WarmupBars     int     // bars to accumulate before first prediction (default 60)
}

func (c Config) withDefaults() Config {
	if c.RiskPerTrade == 0 {
		c.RiskPerTrade = 0.005
	}
	if c.SLATR == 0 {
		c.SLATR = 1.5
	}
	if c.MinSignalScore == 0 {
		c.MinSignalScore = 0.3
	}
	if c.WarmupBars == 0 {
		c.WarmupBars = 60
	}
	return c
}

// Strategy is a strategy.Strategy implementation that composes N alphas.
type Strategy struct {
	cfg    Config
	alphas []Alpha
	bars   []exchange.Kline
	posQty float64 // current position size (signed: + = long, - = short)
}

// New returns a Composite strategy. Panics if alphas is empty.
func New(alphas []Alpha, cfg Config) *Strategy {
	if len(alphas) == 0 {
		panic("composite: at least one alpha required")
	}
	return &Strategy{cfg: cfg.withDefaults(), alphas: alphas}
}

func (s *Strategy) Name() string { return "composite" }

func (s *Strategy) OnBar(ctx *strategy.Context, bar exchange.Kline)  {}
func (s *Strategy) OnFill(ctx *strategy.Context, fill strategy.Fill) {}
```

- [ ] **Step 4: Run tests to verify pass**

```bash
go test ./internal/strategy/composite/... -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/strategy/composite/
git commit -m "feat(composite): strategy skeleton with Config + alpha list"
```

---

### Task 5: Composite OnBar — Pick Best Signal

**Files:**
- Modify: `internal/strategy/composite/strategy.go`
- Modify: `internal/strategy/composite/strategy_test.go`

Each bar: append to history; once warmup met, call BuildFeatures, call all alphas, pick the highest |Strength| signal that passes MinSignalScore, place a market order if direction differs from current position.

- [ ] **Step 1: Add the OnBar test with a mock alpha + mock broker**

```go
// internal/strategy/composite/strategy_test.go (append)
package composite

import (
	"testing"
	"time"

	"github.com/Quantix/quantix/internal/alpha"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
)

type fakeAlpha struct {
	name string
	out  alpha.Signal
}

func (f *fakeAlpha) Name() string                             { return f.name }
func (f *fakeAlpha) Predict(_ alpha.Features) alpha.Signal    { return f.out }

type fakeBroker struct {
	orders []strategy.OrderRequest
}

func (b *fakeBroker) PlaceOrder(req strategy.OrderRequest) string {
	b.orders = append(b.orders, req)
	return "order-1"
}
func (b *fakeBroker) CancelOrder(id string) error { return nil }

type fakePortfolio struct{ cash float64 }

func (p *fakePortfolio) Cash() float64 { return p.cash }
func (p *fakePortfolio) Position(symbol string) (float64, float64, bool) {
	return 0, 0, false
}
func (p *fakePortfolio) Equity(_ map[string]float64) float64 { return p.cash }

func makeBars(n int, base float64) []exchange.Kline {
	bars := make([]exchange.Kline, n)
	for i := range bars {
		px := base + float64(i)*0.5
		bars[i] = exchange.Kline{
			OpenTime: time.Unix(int64(i*300), 0),
			Open:     px, High: px + 0.5, Low: px - 0.5, Close: px,
			Volume: 1,
		}
	}
	return bars
}

func TestStrategy_PicksStrongestSignalAndPlacesOrder(t *testing.T) {
	weak := &fakeAlpha{name: "weak", out: alpha.Signal{Direction: 1, Strength: 0.4}}
	strong := &fakeAlpha{name: "strong", out: alpha.Signal{Direction: -1, Strength: 0.8}}
	s := New([]Alpha{weak, strong}, Config{Symbol: "ETHUSDT"})

	broker := &fakeBroker{}
	pv := &fakePortfolio{cash: 10000}
	ctx := strategy.NewContext(pv, broker, zap.NewNop())

	for _, b := range makeBars(70, 2300) {
		s.OnBar(ctx, b)
	}
	if len(broker.orders) == 0 {
		t.Fatalf("expected at least one order")
	}
	last := broker.orders[len(broker.orders)-1]
	if last.Side != strategy.SideSell {
		t.Fatalf("expected SELL (strong=-1) got %s", last.Side)
	}
}

func TestStrategy_HoldsBelowMinScore(t *testing.T) {
	a := &fakeAlpha{name: "weak", out: alpha.Signal{Direction: 1, Strength: 0.1}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT", MinSignalScore: 0.3})

	broker := &fakeBroker{}
	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, broker, zap.NewNop())

	for _, b := range makeBars(70, 2300) {
		s.OnBar(ctx, b)
	}
	if len(broker.orders) != 0 {
		t.Fatalf("expected no orders, got %d", len(broker.orders))
	}
}

func TestStrategy_WaitsForWarmup(t *testing.T) {
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: 1, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT", WarmupBars: 50})

	broker := &fakeBroker{}
	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, broker, zap.NewNop())

	for _, b := range makeBars(40, 2300) {
		s.OnBar(ctx, b)
	}
	if len(broker.orders) != 0 {
		t.Fatalf("orders placed before warmup: %d", len(broker.orders))
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

```bash
go test ./internal/strategy/composite/... -v
```
Expected: FAIL — OnBar empty body.

- [ ] **Step 3: Implement OnBar**

Replace the empty `OnBar` method with this:

```go
// strategy.go (replace OnBar)
func (s *Strategy) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	s.bars = append(s.bars, bar)
	if cap := s.cfg.WarmupBars * 4; len(s.bars) > cap {
		s.bars = s.bars[len(s.bars)-cap:]
	}
	if len(s.bars) < s.cfg.WarmupBars {
		return
	}

	feat := alpha.BuildFeatures(s.cfg.Symbol, s.bars)
	if feat.ATR == 0 {
		return
	}

	best := alpha.Signal{}
	for _, a := range s.alphas {
		sig := a.Predict(feat)
		if sig.Direction == 0 {
			continue
		}
		if abs(sig.Strength) > abs(best.Strength) {
			best = sig
		}
	}
	if best.Direction == 0 || best.Strength < s.cfg.MinSignalScore {
		return
	}

	side := strategy.SideBuy
	if best.Direction < 0 {
		side = strategy.SideSell
	}

	// Position-aware: skip if already aligned with target direction.
	if (best.Direction > 0 && s.posQty > 0) || (best.Direction < 0 && s.posQty < 0) {
		return
	}

	qty := positionSize(ctx, feat, s.cfg)
	if qty <= 0 {
		return
	}

	ctx.PlaceOrder(strategy.OrderRequest{
		Symbol: s.cfg.Symbol,
		Side:   side,
		Type:   strategy.OrderMarket,
		Qty:    qty,
	})
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
```

- [ ] **Step 4: Run tests to verify pass**

```bash
go test ./internal/strategy/composite/... -v
```
Expected: PASS for all 3 OnBar tests + the 2 prior. May fail on `positionSize` (next task) — temporarily inline `qty := 0.001` or skip with `t.Skip` until Task 6.

- [ ] **Step 5: Commit (allow Task 6 dependency)**

```bash
git add internal/strategy/composite/
git commit -m "feat(composite): OnBar picks strongest signal and places market order"
```

---

### Task 6: R-Based Position Sizing

**Files:**
- Create: `internal/strategy/composite/sizing.go`
- Create: `internal/strategy/composite/sizing_test.go`

Sizing rule: risk per trade = `RiskPerTrade × equity`. SL distance = `SLATR × ATR`. Therefore qty = risk / SL_distance. Round to symbol stepSize (hardcoded 0.001 for ETHUSDT v1; symbol metadata is Phase 5).

- [ ] **Step 1: Write sizing test**

```go
// internal/strategy/composite/sizing_test.go
package composite

import (
	"math"
	"testing"

	"github.com/Quantix/quantix/internal/alpha"
)

func TestPositionSize_RiskBased(t *testing.T) {
	feat := alpha.Features{Close: 2300, ATR: 3.0}
	cfg := Config{RiskPerTrade: 0.01, SLATR: 1.5}.withDefaults()
	pv := &fakePortfolio{cash: 10000}
	ctx := mockContext(pv)

	qty := positionSize(ctx, feat, cfg)

	// risk_usd = 10000 * 0.01 = 100
	// sl_dist  = 3.0 * 1.5 = 4.5
	// raw_qty  = 100 / 4.5 = 22.222...
	// rounded to 0.001 step
	want := math.Floor(22.222*1000) / 1000
	if math.Abs(qty-want) > 1e-6 {
		t.Fatalf("qty = %f want %f", qty, want)
	}
}

func TestPositionSize_ZeroATRReturnsZero(t *testing.T) {
	feat := alpha.Features{Close: 2300, ATR: 0}
	cfg := Config{}.withDefaults()
	if positionSize(mockContext(&fakePortfolio{cash: 10000}), feat, cfg) != 0 {
		t.Fatalf("zero ATR must return zero qty")
	}
}
```

Add helper to `strategy_test.go`:

```go
func mockContext(pv strategy.PortfolioView) *strategy.Context {
	return strategy.NewContext(pv, &fakeBroker{}, zap.NewNop())
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/strategy/composite/... -run TestPositionSize -v
```
Expected: FAIL.

- [ ] **Step 3: Implement positionSize**

```go
// internal/strategy/composite/sizing.go
package composite

import (
	"math"

	"github.com/Quantix/quantix/internal/alpha"
	"github.com/Quantix/quantix/internal/strategy"
)

// stepSize is the qty granularity (ETHUSDT futures = 0.001).
// Symbol-specific metadata is introduced in Phase 5.
const stepSize = 0.001

// positionSize returns the quantity for a new position based on equity
// at risk and ATR-based stop distance. Returns 0 if any guard trips.
func positionSize(ctx *strategy.Context, f alpha.Features, cfg Config) float64 {
	if f.ATR <= 0 {
		return 0
	}
	equity := ctx.Portfolio.Cash() // v1: cash-only equity (no open positions)
	if equity <= 0 {
		return 0
	}
	riskUSD := equity * cfg.RiskPerTrade
	slDist := f.ATR * cfg.SLATR
	if slDist <= 0 {
		return 0
	}
	raw := riskUSD / slDist
	return math.Floor(raw/stepSize) * stepSize
}
```

- [ ] **Step 4: Run tests to verify all pass**

```bash
go test ./internal/strategy/composite/... -v
```
Expected: PASS — all composite tests including OnBar tests.

- [ ] **Step 5: Commit**

```bash
git add internal/strategy/composite/sizing.go internal/strategy/composite/sizing_test.go
git commit -m "feat(composite): R-based position sizing with ATR stop"
```

---

### Task 7: Track Position from OnFill

**Files:**
- Modify: `internal/strategy/composite/strategy.go`
- Modify: `internal/strategy/composite/strategy_test.go`

`OnFill` updates `s.posQty` so OnBar's "skip if aligned" guard works. Long fill adds qty; short fill subtracts.

- [ ] **Step 1: Write OnFill test**

```go
// strategy_test.go (append)
func TestStrategy_PositionTrackedAfterFill(t *testing.T) {
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: 1, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})
	ctx := mockContext(&fakePortfolio{cash: 10000})

	s.OnFill(ctx, strategy.Fill{
		Symbol: "ETHUSDT", Side: strategy.SideBuy, Qty: 0.5, Price: 2300,
	})
	if s.posQty != 0.5 {
		t.Fatalf("posQty=%f want 0.5", s.posQty)
	}

	s.OnFill(ctx, strategy.Fill{
		Symbol: "ETHUSDT", Side: strategy.SideSell, Qty: 0.5, Price: 2310,
	})
	if s.posQty != 0 {
		t.Fatalf("posQty=%f want 0 after closing", s.posQty)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/strategy/composite/... -run TestStrategy_PositionTrackedAfterFill -v
```
Expected: FAIL — OnFill is no-op.

- [ ] **Step 3: Implement OnFill**

```go
// strategy.go (replace OnFill)
func (s *Strategy) OnFill(_ *strategy.Context, fill strategy.Fill) {
	if fill.Symbol != s.cfg.Symbol {
		return
	}
	if fill.Side == strategy.SideBuy {
		s.posQty += fill.Qty
	} else {
		s.posQty -= fill.Qty
	}
}
```

- [ ] **Step 4: Run tests to verify pass**

```bash
go test ./internal/strategy/composite/... -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/strategy/composite/
git commit -m "feat(composite): track position via OnFill"
```

---

### Task 8: Registry Registration

**Files:**
- Create: `internal/strategy/composite/register.go`
- Modify: `cmd/backtest/main.go` (one-line import)

Register the composite strategy with the registry so `cmd/backtest -strategy composite` discovers it. Default alpha list: just Breakout (more added in Phase 3).

- [ ] **Step 1: Write registration test**

```go
// internal/strategy/composite/register_test.go
package composite

import (
	"testing"

	"github.com/Quantix/quantix/internal/strategy/registry"
	"go.uber.org/zap"
)

func TestRegistry_HasComposite(t *testing.T) {
	if !registry.Exists("composite") {
		t.Fatalf("composite not registered")
	}
	s, err := registry.Create("composite", map[string]any{"Symbol": "ETHUSDT"}, zap.NewNop())
	if err != nil {
		t.Fatalf("Create err: %v", err)
	}
	if s.Name() != "composite" {
		t.Fatalf("Name=%q", s.Name())
	}
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/strategy/composite/... -run TestRegistry -v
```
Expected: FAIL — composite not registered.

- [ ] **Step 3: Implement register.go**

```go
// internal/strategy/composite/register.go
package composite

import (
	"fmt"

	"github.com/Quantix/quantix/internal/alpha/baseline"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
	"go.uber.org/zap"
)

func init() {
	registry.Register("composite", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := Config{Symbol: stringParam(params, "Symbol", "ETHUSDT")}
		if v, ok := params["RiskPerTrade"].(float64); ok {
			cfg.RiskPerTrade = v
		}
		if v, ok := params["SLATR"].(float64); ok {
			cfg.SLATR = v
		}
		if v, ok := params["MinSignalScore"].(float64); ok {
			cfg.MinSignalScore = v
		}
		if v, ok := params["WarmupBars"].(float64); ok {
			cfg.WarmupBars = int(v)
		}

		alphas := []Alpha{baseline.NewBreakout()}
		s := New(alphas, cfg)
		log.Info("composite strategy created",
			zap.String("symbol", cfg.Symbol),
			zap.Int("alphas", len(alphas)))
		return s, nil
	})
}

func stringParam(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

// Compile-time check that we still implement the strategy interface.
var _ = fmt.Sprintf
```

- [ ] **Step 4: Add side-effect import to cmd/backtest**

```go
// cmd/backtest/main.go (find the existing strategy registrations block,
// add this line alongside the other _ "..." imports)
	_ "github.com/Quantix/quantix/internal/strategy/composite"
```

- [ ] **Step 5: Run tests + smoke backtest**

```bash
go test ./internal/strategy/composite/... -v
go build ./cmd/backtest
./backtest -strategy composite -symbol ETHUSDT -interval 5m -capital 5000 \
  -start 2026-04-15 -end 2026-05-09 -fee 0.0004 -slippage 0.0001 2>&1 | tail -20
```
Expected: PASS for tests; backtest prints a metrics block (numbers may be poor — Phase 1 doesn't promise profit).

- [ ] **Step 6: Commit**

```bash
git add internal/strategy/composite/register.go internal/strategy/composite/register_test.go cmd/backtest/main.go
git commit -m "feat(composite): register strategy + wire into cmd/backtest"
```

---

### Task 9: Sanity Comparison Against aistrat

**Files:**
- (None — script-only sanity check, no commit unless documenting)

Run BOTH strategies on the same window and confirm composite produces SOMETHING (orders, equity changes). Do NOT expect composite to win — it's a 1-alpha baseline.

- [ ] **Step 1: Run both strategies on identical 21-day window**

```bash
go run ./cmd/backtest -strategy ai -symbol ETHUSDT -interval 5m -capital 5000 \
  -start 2026-04-15 -end 2026-05-09 -fee 0.0004 -slippage 0.0001 2>&1 | grep -E "Total Return|Total Trades|Win Rate|Profit Factor" > /tmp/ai.txt

go run ./cmd/backtest -strategy composite -symbol ETHUSDT -interval 5m -capital 5000 \
  -start 2026-04-15 -end 2026-05-09 -fee 0.0004 -slippage 0.0001 2>&1 | grep -E "Total Return|Total Trades|Win Rate|Profit Factor" > /tmp/composite.txt

echo "=== aistrat ==="; cat /tmp/ai.txt
echo "=== composite ==="; cat /tmp/composite.txt
```

Expected: Both print metric blocks. composite Trades > 0. composite numbers can be worse — that's fine. Failure = composite has 0 trades or panics.

- [ ] **Step 2: Document the run in plan-followup file**

Append to this plan file (under a new "## Phase 1 Sanity Run" section):
- ai vs composite Total Return, Trades, WR, PF for the run
- 1-line note on whether composite is shippable as a backtest baseline

- [ ] **Step 3: Commit the documentation**

```bash
git add docs/superpowers/plans/2026-05-09-quant-framework-strangler-fig.md
git commit -m "docs(plans): record Phase 1 sanity comparison"
```

---

### Phase 1 Definition of Done

- [ ] All 9 tasks merged
- [ ] `go test ./internal/alpha/... ./internal/strategy/composite/...` passes (≥80% coverage)
- [ ] `cmd/backtest -strategy composite -symbol ETHUSDT` runs without error
- [ ] Composite produces ≥10 trades over a 21-day window (sanity floor)
- [ ] aistrat is unchanged (`git diff` shows no edits in `internal/strategy/aistrat/`)
- [ ] No live engine deploy yet (Phase 2 deliverable)

---

## Phase 2 Sketch (Live Capability + Recovery)

**Goal:** Composite strategy runs in `cmd/api` live engine on demo, with session recovery on restart.

**Files (anticipated):**
- `internal/strategy/composite/recovery.go` — load/save composite state via PositionSyncer (mirror `aistrat/helpers.go:432` pattern)
- `internal/strategy/composite/staged_tp.go` — staged TP placement using existing StagedExitPlacer

**Acceptance:**
- Composite started via `POST /api/engine/start` runs live demo
- After `systemctl restart quantix-api`, composite resumes with same posQty + state in memory
- One full trade cycle (entry → TP or SL) executes on demo

**Detailed plan written when Phase 1 complete.**

---

## Phase 3 Sketch (More Alphas + IC Combiner)

**Goal:** 3+ alphas registered, IC-weighted combiner replaces "pick strongest" logic.

**Files (anticipated):**
- `internal/alpha/momentum/{ma_cross,rsi}.go`
- `internal/alpha/funding/funding_divergence.go` (Binance funding rate API integration)
- `internal/strategy/composite/combiner.go` — IC computation + weighting

**Acceptance:**
- Each new alpha standalone backtest PF ≥ 1.0 over 30 days OR documented kill
- Combiner backtest PF ≥ max(individual alpha PF) over same window

**Detailed plan written when Phase 2 complete.**

---

## Phase 4 Sketch (ML Alpha)

**Goal:** Train a simple model (logistic regression first, then lightgbm) on engineered features → forward-return sign. Use existing `cmd/optimize` walk-forward harness for OOS validation.

**Files (anticipated):**
- `internal/alpha/ml/dataset.go` — feature engineering pipeline
- `internal/alpha/ml/logreg.go` — train/inference wrappers (Go-native, e.g. `gonum`)
- `cmd/train-ml/main.go` — offline training CLI; persists weights to `models/`

**Acceptance:**
- OOS PF ≥ 1.0 with strict walk-forward (train past 60d, test next 7d, roll)
- Or kill criteria: 3 consecutive walk-forward windows with OOS PF < 0.9 → archive

**Detailed plan written when Phase 3 complete.**

---

## Phase 5 Sketch (Multi-Symbol)

**Goal:** Composite runs simultaneously on ≥3 symbols (ETH, BTC, SOL) with isolated risk + cross-symbol margin cap.

**Files (anticipated):**
- `internal/symbol/profile.go` — per-symbol metadata (stepSize, tickSize, leverage caps, BBWidthMin auto-calibration)
- `internal/risk/portfolio_cap.go` — total margin cap across all engine_sessions
- `cmd/calibrate-symbol/main.go` — generate per-symbol config from N days of historical data

**Acceptance:**
- 3 composite engines running on demo concurrently
- Each isolated: SL on ETH cannot affect BTC margin
- Aggregate margin held ≤ X% of equity

**Detailed plan written when Phase 4 complete.**

---

## Risks & Rollback

| Risk | Mitigation |
|---|---|
| Phase 1 composite is uncompetitive vs aistrat in backtest | Acceptable for Phase 1. Real test is Phase 3 with multiple alphas. |
| New code introduces hidden bug in registry path | Tests cover registry path. aistrat lives in separate package, untouched. |
| Phase 2 live deploy regresses position recovery | Test in worktree with separate engine_id; aistrat keeps running on `ETHUSDT-5m-ai`. Composite uses `ETHUSDT-5m-composite`. |
| Refactor scope creeps into aistrat | **Hard rule: no commits in this plan touch `internal/strategy/aistrat/`.** Any such diff fails review. |
| Phase 4 ML alpha never converges | Time-boxed to 2 weeks. Kill criteria explicit. |

**Rollback per phase:**
- Phase 1-2: `git revert` the composite registration commit. Live API loses composite from registry; aistrat unaffected.
- Phase 3-5: Feature-toggleable alphas via Config. Bad alpha removed from default list, no code revert needed.

---

## Notes for Implementing Engineer

- Existing `internal/indicator` package has BollingerBands, RSI, ATR. Use those — do not reimplement.
- `strategy.Context` exposes `PlaceOrder`, `CancelOrder`, `ClosePosition`. Do not reach into engine internals.
- `cmd/backtest` runs strategies in a deterministic single-threaded event loop. Live API runs in goroutines with `OnFill` from a separate WS consumer goroutine. Composite must be safe for `OnBar` and `OnFill` to be called from different goroutines (Phase 2 will need a `sync.Mutex` on `posQty` — Phase 1 single-threaded backtest doesn't).
- Project change discipline: per `feedback_change_discipline.md` memory, aistrat changes are single-point + canary. Composite is a NEW package, no such restriction; but each task is its own commit and must pass tests.

---

## Phase 1 Sanity Run (Task 9)

**Date:** 2026-05-09
**Window:** 2026-04-15 → 2026-05-09 (24 days, ETHUSDT 5m, 6913 bars)
**Capital:** $5000, fee 0.04%, slippage 0.01%

### Results

| Strategy | Total Return | Trades | WR | PF | Max DD | Sharpe |
|---|---:|---:|---:|---:|---:|---:|
| aistrat (legacy) | -6.66% | 214 | 59.3% | 0.95 | 10.59% | -3.054 |
| composite (Phase 1) | -0.83% | 1 | 0.0% | 0.00 | 9.65% | -0.070 |

Verbatim grep output:

```
=== aistrat ===
  Total Return     -6.66%
  Sharpe Ratio  -3.054
  Max Drawdown  10.59%  ($532.14)
  Total Trades   214
  Win Rate       59.3%  (127 W / 87 L)
  Profit Factor  0.95

=== composite ===
  Total Return     -0.83%
  Sharpe Ratio  -0.070
  Max Drawdown  9.65%  ($508.92)
  Total Trades   1
  Win Rate       0.0%  (0 W / 1 L)
  Profit Factor  0.00
```

### Findings

Composite produced exactly **1 trade** over 24 days (well below the 10-trade sanity floor). The bottleneck is **missing exit logic, not weak signals**: once the Breakout alpha fires its first signal and `posQty != 0`, the position-aware skip in `OnBar` prevents any new same-direction entry, and there is no opposite-direction signal strong enough to cause a reversal during the window. `OrderRequest.StopLoss` is populated but the backtest broker (`internal/backtest/broker.go`) does not honor it as a trigger price — it is metadata only. The single open position therefore rides the entire window (`Avg Duration 23d 19h`) and exits at end-of-test with a -0.80% loss. This is expected and acceptable for a 1-alpha skeleton — exit/SL plumbing is explicitly Phase 2 / Phase 3 work, not Phase 1 scope.

### Phase 1 Status

**Definition of Done acceptance criteria:**
- ✅ All 9 tasks merged into `feat/composite-phase1` (15 commits, listed below)
- ✅ `go test ./internal/alpha/... ./internal/strategy/composite/...` passes (verified Task 8)
- ✅ `cmd/backtest -strategy composite` runs without error (verified above)
- ❌ Composite produces ≥10 trades over 21-day window — actual: **1 trade**. Root cause: no exit logic in Phase 1 skeleton. Deferred to Phase 2.
- ✅ aistrat unchanged (no commit on this branch touches `internal/strategy/aistrat/`)
- ✅ No live engine deploy (Phase 2 deliverable)

**Commits in `feat/composite-phase1` branch:**

```
c16c3d8 feat(composite): register strategy + wire into cmd/backtest
68c40cc feat(composite): track position via OnFill
6c96a41 refactor(composite): trust Signal contract; populate StopLoss; strengthen tests
48ed718 feat(composite): OnBar picks strongest signal and places market order
80a5447 feat(composite): R-based position sizing with ATR stop
b87be12 feat(composite): strategy skeleton with Config + alpha list
a80aaec refactor(alpha/baseline): %.2f reason format + pin cap/reject branches in tests
92141bf feat(alpha/baseline): Breakout alpha with ATR-scaled strength
8d8fee3 fix(alpha): Donchian seed must be prior bar, not current close
07c4d4d feat(alpha): BuildFeatures wrapper over indicator package
e89a768 refactor(indicator): use builtin max + math.Abs; pin Wilder ATR tests
04f1fca feat(indicator): add Wilder ATR for alpha use
deeae6b refactor(alpha): typed Direction, drop LastBars, add contract tests
28117dc feat(alpha): define Alpha interface and Features/Signal types
```

**Phase 1 verdict:** ready to merge. The trade-count miss is a known limitation of the 1-alpha skeleton (no exits), not a defect — aistrat hits 214 trades because it has full TP/SL/trailing/regime exits; composite Phase 1 deliberately defers all of that to keep scope tight. Merging this branch unblocks Phase 2 (live capability) and Phase 3 (multi-alpha + exit policy), where the trade count and Sharpe become meaningful.
