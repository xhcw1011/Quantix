# Cross-Sectional Funding Strategy — Core Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the pure, testable Go core of the validated cross-sectional funding factor — universe filter, ranker, target builder, rebalance-to-target diff, and an in-memory backtest that computes P&L including funding — so it can be validated for parity against `scripts/xsmom_funding.py` before any exchange/live wiring.

**Architecture:** A new `internal/xsfunding` package of small pure functions (no I/O): `Eligible` → `Rank` → `BuildTargets` → `Deltas` (live order generation) and `StepPnL` → `RunBacktest` (validation). The order-level infra (OMS/broker/ORG/pool) and data ingestion are NOT touched here; this package is the deterministic brain. Design: `docs/superpowers/specs/2026-07-07-xs-funding-strategy-design.md`.

**Tech Stack:** Go 1.24, stdlib only (`sort`, `math`, `testing`). Table-driven tests, matching the repo's existing style (e.g. `internal/orgateway/rules_test.go`).

---

### Task 1: Types + universe filter (`Eligible`)

**Files:**
- Create: `internal/xsfunding/xsfunding.go`
- Create: `internal/xsfunding/xsfunding_test.go`

- [ ] **Step 1: Write the failing test**

```go
package xsfunding

import "testing"

func cs(sym string, funding, price, vol float64, days int) CoinState {
	return CoinState{Symbol: sym, TrailFunding: funding, Price: price, TrailVolume: vol, DaysListed: days}
}

func TestEligible(t *testing.T) {
	coins := []CoinState{
		cs("OK", 0.01, 100, 5e6, 30),   // passes
		cs("NEW", 0.01, 100, 5e6, 5),   // too new (days < 20)
		cs("THIN", 0.01, 100, 1e5, 30), // too illiquid (vol < 1e6)
		cs("ZERO", 0.01, 0, 5e6, 30),   // no price
	}
	got := Eligible(coins, 20, 1e6)
	if len(got) != 1 || got[0].Symbol != "OK" {
		t.Fatalf("expected only OK eligible, got %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/xsfunding/ -run TestEligible`
Expected: FAIL — build error `undefined: CoinState` / `undefined: Eligible`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package xsfunding implements the pure core of the cross-sectional funding
// strategy: rank a mid-cap perp universe by funding, long the lowest / short the
// highest, dollar-neutral, rebalanced to target. No I/O — the deterministic brain
// validated against scripts/xsmom_funding.py before any exchange wiring.
package xsfunding

// CoinState is one coin's inputs at a rebalance.
type CoinState struct {
	Symbol       string
	TrailFunding float64 // summed funding over the lookback window
	Price        float64
	TrailVolume  float64 // avg daily $ volume (liquidity filter)
	DaysListed   int     // for the point-in-time universe
}

// Config holds the strategy parameters.
type Config struct {
	K             int     // positions per side
	GrossFrac     float64 // gross exposure = capital × GrossFrac
	MinDaysListed int     // point-in-time listing filter
	MinVolume     float64 // liquidity floor ($ avg daily volume)
	FeeRate       float64 // per-side fee (backtest)
}

// Target is a per-symbol target position (signed notional; + long, − short).
type Target struct {
	Symbol   string
	Notional float64
}

// Order is a per-symbol trade to place (signed notional; + buy, − sell).
type Order struct {
	Symbol   string
	Notional float64
}

// Eligible returns the point-in-time tradeable universe: listed long enough, liquid
// enough, and priced.
func Eligible(coins []CoinState, minDaysListed int, minVolume float64) []CoinState {
	out := make([]CoinState, 0, len(coins))
	for _, c := range coins {
		if c.DaysListed >= minDaysListed && c.TrailVolume >= minVolume && c.Price > 0 {
			out = append(out, c)
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/xsfunding/ -run TestEligible`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/xsfunding/xsfunding.go internal/xsfunding/xsfunding_test.go
git commit -m "feat(xsfunding): types + point-in-time universe filter (Eligible)"
```

---

### Task 2: Ranker (`Rank`)

**Files:**
- Create: `internal/xsfunding/rank.go`
- Modify: `internal/xsfunding/xsfunding_test.go` (append)

- [ ] **Step 1: Write the failing test**

```go
func TestRank(t *testing.T) {
	coins := []CoinState{
		cs("A", -0.02, 100, 5e6, 30), // lowest funding
		cs("B", 0.00, 100, 5e6, 30),
		cs("C", 0.01, 100, 5e6, 30),
		cs("D", 0.05, 100, 5e6, 30), // highest funding
	}
	longs, shorts := Rank(coins, 1)
	if len(longs) != 1 || longs[0] != "A" {
		t.Fatalf("long should be lowest-funding A, got %v", longs)
	}
	if len(shorts) != 1 || shorts[0] != "D" {
		t.Fatalf("short should be highest-funding D, got %v", shorts)
	}
	// insufficient universe → nil,nil
	if l, s := Rank(coins, 3); l != nil || s != nil {
		t.Fatalf("fewer than 2K coins should yield nil, got %v %v", l, s)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/xsfunding/ -run TestRank`
Expected: FAIL — `undefined: Rank`.

- [ ] **Step 3: Write minimal implementation**

```go
package xsfunding

import "sort"

// Rank sorts the eligible universe by trailing funding and returns the K
// lowest-funding symbols (to long) and K highest-funding symbols (to short).
// Returns nil, nil when fewer than 2K coins are available.
func Rank(coins []CoinState, k int) (longs, shorts []string) {
	if k <= 0 || len(coins) < 2*k {
		return nil, nil
	}
	sorted := make([]CoinState, len(coins))
	copy(sorted, coins)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TrailFunding < sorted[j].TrailFunding })
	for i := 0; i < k; i++ {
		longs = append(longs, sorted[i].Symbol)
		shorts = append(shorts, sorted[len(sorted)-1-i].Symbol)
	}
	return longs, shorts
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/xsfunding/ -run TestRank`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/xsfunding/rank.go internal/xsfunding/xsfunding_test.go
git commit -m "feat(xsfunding): funding ranker (long lowest / short highest, K each)"
```

---

### Task 3: Target builder (`BuildTargets`)

**Files:**
- Create: `internal/xsfunding/target.go`
- Modify: `internal/xsfunding/xsfunding_test.go` (append)

- [ ] **Step 1: Write the failing test**

```go
import "math"

func TestBuildTargets(t *testing.T) {
	targets := BuildTargets([]string{"A", "B"}, []string{"C", "D"}, 10000, 1.0)
	if len(targets) != 4 {
		t.Fatalf("expected 4 targets, got %d", len(targets))
	}
	byS := map[string]float64{}
	var net float64
	for _, tg := range targets {
		byS[tg.Symbol] = tg.Notional
		net += tg.Notional
	}
	if byS["A"] != 2500 || byS["C"] != -2500 {
		t.Fatalf("expected ±2500 per position, got %+v", byS)
	}
	if math.Abs(net) > 1e-9 {
		t.Fatalf("book must be dollar-neutral, net=%v", net)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/xsfunding/ -run TestBuildTargets`
Expected: FAIL — `undefined: BuildTargets`.

- [ ] **Step 3: Write minimal implementation**

```go
package xsfunding

// BuildTargets makes a dollar-neutral, equal-weight target book: each of the 2K
// positions gets (capital × grossFrac)/(2K) notional, positive for longs, negative
// for shorts.
func BuildTargets(longs, shorts []string, capital, grossFrac float64) []Target {
	n := len(longs) + len(shorts)
	if n == 0 {
		return nil
	}
	per := capital * grossFrac / float64(n)
	out := make([]Target, 0, n)
	for _, s := range longs {
		out = append(out, Target{Symbol: s, Notional: per})
	}
	for _, s := range shorts {
		out = append(out, Target{Symbol: s, Notional: -per})
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/xsfunding/ -run TestBuildTargets`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/xsfunding/target.go internal/xsfunding/xsfunding_test.go
git commit -m "feat(xsfunding): dollar-neutral equal-weight target builder"
```

---

### Task 4: Rebalance-to-target diff (`Deltas`)

**Files:**
- Create: `internal/xsfunding/rebalance.go`
- Modify: `internal/xsfunding/xsfunding_test.go` (append)

- [ ] **Step 1: Write the failing test**

```go
func TestDeltas(t *testing.T) {
	current := map[string]float64{"A": 2500, "B": -2500, "C": 1000}
	targets := []Target{{Symbol: "A", Notional: 2500}, {Symbol: "D", Notional: -2500}}
	got := Deltas(current, targets, 1.0) // minOrder 1.0
	// A: 2500→2500 delta 0 → skipped. D: new -2500. B: not in target → close +2500. C: close -1000.
	want := map[string]float64{"D": -2500, "B": 2500, "C": -1000}
	if len(got) != 3 {
		t.Fatalf("expected 3 orders, got %d: %+v", len(got), got)
	}
	for _, o := range got {
		if want[o.Symbol] != o.Notional {
			t.Fatalf("order %s = %v, want %v", o.Symbol, o.Notional, want[o.Symbol])
		}
	}
	// output sorted by symbol for determinism
	if got[0].Symbol != "B" || got[1].Symbol != "C" || got[2].Symbol != "D" {
		t.Fatalf("orders must be sorted by symbol, got %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/xsfunding/ -run TestDeltas`
Expected: FAIL — `undefined: Deltas`.

- [ ] **Step 3: Write minimal implementation**

```go
package xsfunding

import (
	"math"
	"sort"
)

// Deltas returns the orders that move current signed notional to the target book:
// per target, trade (target − current); symbols held but no longer targeted are
// closed. Deltas with |notional| < minOrder are skipped (no dust trades). Output is
// sorted by symbol for determinism.
func Deltas(current map[string]float64, targets []Target, minOrder float64) []Order {
	targeted := make(map[string]bool, len(targets))
	var out []Order
	for _, t := range targets {
		targeted[t.Symbol] = true
		d := t.Notional - current[t.Symbol]
		if math.Abs(d) >= minOrder {
			out = append(out, Order{Symbol: t.Symbol, Notional: d})
		}
	}
	for s, cur := range current {
		if !targeted[s] && math.Abs(cur) >= minOrder {
			out = append(out, Order{Symbol: s, Notional: -cur})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/xsfunding/ -run TestDeltas`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/xsfunding/rebalance.go internal/xsfunding/xsfunding_test.go
git commit -m "feat(xsfunding): rebalance-to-target order diff (Deltas)"
```

---

### Task 5: Orchestrator (`Rebalance`)

**Files:**
- Modify: `internal/xsfunding/rebalance.go` (append)
- Modify: `internal/xsfunding/xsfunding_test.go` (append)

- [ ] **Step 1: Write the failing test**

```go
func TestRebalanceOrchestrator(t *testing.T) {
	coins := []CoinState{
		cs("A", -0.02, 100, 5e6, 30), // long
		cs("B", 0.00, 100, 5e6, 30),
		cs("C", 0.01, 100, 5e6, 30),
		cs("D", 0.05, 100, 5e6, 30), // short
		cs("NEW", -0.9, 100, 5e6, 5), // extreme funding but too new → excluded
	}
	cfg := Config{K: 1, GrossFrac: 1.0, MinDaysListed: 20, MinVolume: 1e6}
	orders := Rebalance(coins, map[string]float64{}, 10000, cfg, 1.0)
	// from flat: long A +5000, short D -5000 (NEW excluded despite lowest funding)
	got := map[string]float64{}
	for _, o := range orders {
		got[o.Symbol] = o.Notional
	}
	if got["A"] != 5000 || got["D"] != -5000 || len(orders) != 2 {
		t.Fatalf("expected long A +5000 / short D -5000, got %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/xsfunding/ -run TestRebalanceOrchestrator`
Expected: FAIL — `undefined: Rebalance`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/xsfunding/rebalance.go`:

```go
// Rebalance chains the pure pieces: filter to the point-in-time universe, rank by
// funding, build the dollar-neutral target book, and diff against current positions.
// Returns nil when the eligible universe is too small to form 2K positions (no rotation).
func Rebalance(coins []CoinState, current map[string]float64, capital float64, cfg Config, minOrder float64) []Order {
	longs, shorts := Rank(Eligible(coins, cfg.MinDaysListed, cfg.MinVolume), cfg.K)
	if longs == nil {
		return nil
	}
	return Deltas(current, BuildTargets(longs, shorts, capital, cfg.GrossFrac), minOrder)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/xsfunding/ -run TestRebalanceOrchestrator`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/xsfunding/rebalance.go internal/xsfunding/xsfunding_test.go
git commit -m "feat(xsfunding): Rebalance orchestrator (universe→rank→target→diff)"
```

---

### Task 6: Period P&L including funding (`StepPnL`)

**Files:**
- Create: `internal/xsfunding/backtest.go`
- Modify: `internal/xsfunding/xsfunding_test.go` (append)

- [ ] **Step 1: Write the failing test**

```go
func TestStepPnL(t *testing.T) {
	targets := []Target{{Symbol: "A", Notional: 5000}, {Symbol: "D", Notional: -5000}} // capital 10000
	fwdRet := map[string]float64{"A": 0.10, "D": -0.10}      // long A rises, short D falls → both win
	fwdFund := map[string]float64{"A": -0.01, "D": 0.02}     // long A collects (neg funding), short D collects
	// price P&L: 0.5*0.10 + (-0.5)*(-0.10) = 0.10
	// funding P&L: -0.5*(-0.01) + -(-0.5)*(0.02) = 0.005 + 0.010 = 0.015
	// fees: turnover from flat = 10000 → 10000/10000*0.0005 = 0.0005
	// total = 0.10 + 0.015 - 0.0005 = 0.1145
	got := StepPnL(targets, map[string]float64{}, fwdRet, fwdFund, 10000, 0.0005)
	if math.Abs(got-0.1145) > 1e-9 {
		t.Fatalf("StepPnL = %v, want 0.1145", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/xsfunding/ -run TestStepPnL`
Expected: FAIL — `undefined: StepPnL`.

- [ ] **Step 3: Write minimal implementation**

```go
package xsfunding

import "math"

// StepPnL is one rebalance period's return as a fraction of capital: for each target
// position, price return weighted by its signed weight, plus funding (a long PAYS
// funding, a short COLLECTS it), minus per-side fees on the traded turnover
// (prevNotional → targets). fwdRet/fwdFunding are the forward price return and
// forward summed funding over the holding period, per symbol.
func StepPnL(targets []Target, prevNotional, fwdRet, fwdFunding map[string]float64, capital, feeRate float64) float64 {
	var pnl float64
	targeted := make(map[string]bool, len(targets))
	for _, t := range targets {
		w := t.Notional / capital // signed weight
		pnl += w * fwdRet[t.Symbol]
		pnl += -w * fwdFunding[t.Symbol] // long (w>0) pays funding; short (w<0) collects
		targeted[t.Symbol] = true
	}
	var turnover float64
	for _, t := range targets {
		turnover += math.Abs(t.Notional - prevNotional[t.Symbol])
	}
	for s, cur := range prevNotional {
		if !targeted[s] {
			turnover += math.Abs(cur)
		}
	}
	return pnl - turnover/capital*feeRate
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/xsfunding/ -run TestStepPnL`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/xsfunding/backtest.go internal/xsfunding/xsfunding_test.go
git commit -m "feat(xsfunding): per-period P&L incl. funding + turnover fees (StepPnL)"
```

---

### Task 7: In-memory backtest (`RunBacktest`)

**Files:**
- Modify: `internal/xsfunding/backtest.go` (append)
- Modify: `internal/xsfunding/xsfunding_test.go` (append)

- [ ] **Step 1: Write the failing test**

```go
func TestRunBacktest(t *testing.T) {
	cfg := Config{K: 1, GrossFrac: 1.0, MinDaysListed: 20, MinVolume: 1e6, FeeRate: 0.0}
	base := []CoinState{
		cs("A", -0.02, 100, 5e6, 30), cs("B", 0.00, 100, 5e6, 30),
		cs("C", 0.01, 100, 5e6, 30), cs("D", 0.05, 100, 5e6, 30),
	}
	// Two identical periods; each: long A +5000 / short D -5000, both win 10% on price.
	period := Period{
		Coins:      base,
		FwdRet:     map[string]float64{"A": 0.10, "D": -0.10},
		FwdFunding: map[string]float64{"A": 0.0, "D": 0.0},
	}
	eq, steps := RunBacktest([]Period{period, period}, 10000, cfg, 1.0)
	// step 1 pnl = 0.10 (price only, no fee); step 2 same → eq = 1.10*1.10 = 1.21
	if len(steps) != 2 || math.Abs(steps[0]-0.10) > 1e-9 {
		t.Fatalf("step pnl = %v, want 0.10", steps)
	}
	if math.Abs(eq-1.21) > 1e-9 {
		t.Fatalf("equity = %v, want 1.21", eq)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/xsfunding/ -run TestRunBacktest`
Expected: FAIL — `undefined: RunBacktest` / `undefined: Period`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/xsfunding/backtest.go`:

```go
// Period is one rebalance point's inputs for the in-memory backtest: the universe
// state, and the forward price return + forward summed funding per symbol over the
// following holding period.
type Period struct {
	Coins      []CoinState
	FwdRet     map[string]float64
	FwdFunding map[string]float64
}

// RunBacktest replays periods through the same pure pieces the live path uses,
// compounding StepPnL. Returns final equity (start 1.0) and the per-period returns.
// A period whose eligible universe is too small contributes 0 and holds flat.
func RunBacktest(periods []Period, capital float64, cfg Config, minOrder float64) (equity float64, steps []float64) {
	equity = 1.0
	prev := map[string]float64{}
	for _, p := range periods {
		longs, shorts := Rank(Eligible(p.Coins, cfg.MinDaysListed, cfg.MinVolume), cfg.K)
		if longs == nil {
			steps = append(steps, 0)
			continue
		}
		targets := BuildTargets(longs, shorts, capital, cfg.GrossFrac)
		pnl := StepPnL(targets, prev, p.FwdRet, p.FwdFunding, capital, cfg.FeeRate)
		equity *= 1 + pnl
		steps = append(steps, pnl)
		prev = map[string]float64{}
		for _, t := range targets {
			prev[t.Symbol] = t.Notional
		}
	}
	return equity, steps
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/xsfunding/ -run TestRunBacktest`
Expected: PASS.

- [ ] **Step 5: Run the full package + vet, then commit**

```bash
gofmt -w internal/xsfunding/
go vet ./internal/xsfunding/
go test ./internal/xsfunding/
git add internal/xsfunding/backtest.go internal/xsfunding/xsfunding_test.go
git commit -m "feat(xsfunding): in-memory backtest (RunBacktest) compounding StepPnL"
```

Expected: all package tests PASS, vet clean.

---

## Out of scope (deliberate follow-on plans — do NOT build here)

These come after this core is green and parity-checked; each is its own plan:

1. **Funding data ingestion** — a `funding_rates` DB table + a fetch/backfill job (Binance `/fapi/v1/fundingRate`, paginated), so live and backtest read one source. Reuse the `cmd/backfill` flag-driven pattern.
2. **Parity harness** — a `cmd/xsfunding-backtest` that loads real klines+funding, builds `[]Period`, runs `RunBacktest`, and reproduces `scripts/xsmom_funding.py`'s numbers (the parity gate).
3. **Live rebalancer** — a scheduled runner that reads current positions (position syncer), calls `Rebalance`, and places `[]Order` through **ORG** (Layer-1 order safety) → broker/OMS → exchange; registers the strategy as its own market-neutral **pool**. Execution: limit-with-timeout → market fallback to hold cost ≤ ~20bp.
4. **Paper-forward** — run the live rebalancer on the paper broker over weeks; compare realized cost to the ≤20bp budget (the make-or-break gate, since the edge dies by ~50bp).
5. **Shadow → gradual live** — log target rotations live (no orders), then small real capital with ORG enforce + pool caps.

## Self-review

- **Spec coverage:** Ranker (§3 Ranker), universe (§3 Universe Manager), target (§3 Target builder), rebalance-to-target diff (§3 Rebalancer, §4 "only trade the delta"), funding P&L (§ decomposition / §6 backtest). Data ingestion, live executor, ORG/pool integration, paper-forward, rollout → explicitly deferred to follow-on plans (listed above), matching the design's phased rollout (§7). ✅
- **Placeholder scan:** none — every step has complete code + exact commands. ✅
- **Type consistency:** `CoinState`, `Config`, `Target{Symbol,Notional}`, `Order{Symbol,Notional}` defined in Task 1 and used unchanged in Tasks 2–7; `Rank`→`BuildTargets`→`Deltas`/`StepPnL` signatures consistent across `Rebalance` and `RunBacktest`. ✅
