# aistrat Trend-Score Leg Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace aistrat's binary regime gate with a continuous 5m trend-accumulation score that (defensively) penalizes counter-trend fade entries proportionally and (offensively) opens a trend-following entry when the score crosses a threshold and a higher timeframe confirms.

**Architecture:** Three pure functions (`updateTrendScore`, `trendEntryDir`, `trendAlignPenalty`) in a new `trendscore.go`, table-tested. Thin wiring in `signal.go` updates the score once per primary (5m) bar, applies the penalty to the technical confidences, and triggers a trend entry. New config fields default to conservative ON values. Layers on top of trend-cut + hysteresis; deletes nothing.

**Tech Stack:** Go 1.24, existing aistrat strategy package, `go test` table-driven tests.

Spec: `docs/superpowers/specs/2026-06-30-aistrat-trend-score-leg-design.md`

---

## File Structure

- **Create** `internal/strategy/aistrat/trendscore.go` — the 3 pure decision functions (single responsibility).
- **Create** `internal/strategy/aistrat/trendscore_test.go` — table tests for the 3 functions.
- **Modify** `internal/strategy/aistrat/config.go` — 7 config fields + param parsing + `DefaultConfig` values.
- **Modify** `internal/strategy/aistrat/strategy.go` — 2 state fields (`trendScore`, `trendEntryCooldown`).
- **Modify** `internal/strategy/aistrat/signal.go` — wiring: per-bar score update + cooldown decrement, penalty on confidences, trend-entry trigger.

---

## Task 1: `updateTrendScore` pure function

**Files:**
- Create: `internal/strategy/aistrat/trendscore.go`
- Test: `internal/strategy/aistrat/trendscore_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/strategy/aistrat/trendscore_test.go`:

```go
package aistrat

import (
	"math"
	"testing"
)

func TestUpdateTrendScore(t *testing.T) {
	// decay 0.9, perBarCap 1.0, scoreMax 5.0 unless noted
	tests := []struct {
		name                              string
		prev, body, atr, decay, cap, max  float64
		want                              float64
	}{
		{"flat warmup: atr<=0 just decays", 2.0, 5, 0, 0.9, 1.0, 5.0, 1.8},
		{"bullish body adds strength-weighted", 0, 5, 10, 0.9, 1.0, 5.0, 0.5},
		{"bearish body subtracts", 0, -5, 10, 0.9, 1.0, 5.0, -0.5},
		{"per-bar cap clamps a spike bar", 0, 30, 10, 0.9, 1.0, 5.0, 1.0},
		{"chop cancels toward zero", 0.5, -5, 10, 0.9, 1.0, 5.0, -0.05},
		{"score cap clamps overflow", 4.8, 10, 10, 0.9, 1.0, 5.0, 5.0},
		{"score cap clamps negative overflow", -4.8, -10, 10, 0.9, 1.0, 5.0, -5.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := updateTrendScore(tt.prev, tt.body, tt.atr, tt.decay, tt.cap, tt.max)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("updateTrendScore(prev=%.2f body=%.0f atr=%.0f) = %.4f, want %.4f",
					tt.prev, tt.body, tt.atr, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/strategy/aistrat/ -run TestUpdateTrendScore`
Expected: FAIL — `undefined: updateTrendScore` (build failed)

- [ ] **Step 3: Write minimal implementation**

Create `internal/strategy/aistrat/trendscore.go`:

```go
package aistrat

import "math"

// updateTrendScore advances the signed 5m trend-accumulation score by one primary
// bar. The bar body strength (body/atr) is clamped to ±perBarCap so one spike bar
// can't dominate; the running score decays by `decay` each bar and is clamped to
// ±scoreMax. In chop, up/down bodies cancel toward 0; in a sustained trend the
// score accumulates one-directionally. atr<=0 (warmup) contributes no delta.
func updateTrendScore(prev, body, atr, decay, perBarCap, scoreMax float64) float64 {
	delta := 0.0
	if atr > 0 {
		delta = body / atr
		if delta > perBarCap {
			delta = perBarCap
		} else if delta < -perBarCap {
			delta = -perBarCap
		}
	}
	s := prev*decay + delta
	if s > scoreMax {
		s = scoreMax
	} else if s < -scoreMax {
		s = -scoreMax
	}
	return s
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/strategy/aistrat/ -run TestUpdateTrendScore -v`
Expected: PASS (all 7 cases)

- [ ] **Step 5: Commit**

```bash
git add internal/strategy/aistrat/trendscore.go internal/strategy/aistrat/trendscore_test.go
git commit -m "feat(aistrat): updateTrendScore — 5m strength-weighted trend accumulation"
```

---

## Task 2: `trendEntryDir` pure function

**Files:**
- Modify: `internal/strategy/aistrat/trendscore.go`
- Test: `internal/strategy/aistrat/trendscore_test.go`

- [ ] **Step 1: Write the failing test** (append to `trendscore_test.go`)

```go
func TestTrendEntryDir(t *testing.T) {
	tests := []struct {
		name             string
		score, threshold float64
		htfDir, want     int
	}{
		{"disabled when threshold<=0", 9, 0, 1, 0},
		{"long: score>=thr and htf confirms up", 3.5, 3.5, 1, 1},
		{"long: score ok but htf neutral → no", 3.5, 3.5, 0, 0},
		{"long: score ok but htf against → no", 3.5, 3.5, -1, 0},
		{"long: below threshold → no", 2.0, 3.5, 1, 0},
		{"short: score<=-thr and htf confirms down", -3.6, 3.5, -1, -1},
		{"short: htf against (up) → no", -3.6, 3.5, 1, 0},
		{"flat score → no", 0, 3.5, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trendEntryDir(tt.score, tt.threshold, tt.htfDir); got != tt.want {
				t.Errorf("trendEntryDir(score=%.2f thr=%.2f htf=%d) = %d, want %d",
					tt.score, tt.threshold, tt.htfDir, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/strategy/aistrat/ -run TestTrendEntryDir`
Expected: FAIL — `undefined: trendEntryDir`

- [ ] **Step 3: Write minimal implementation** (append to `trendscore.go`)

```go
// trendEntryDir returns the direction of a trend-following entry justified by the
// score plus same-direction higher-timeframe confirmation, or 0 for none.
// threshold<=0 disables trend entry. Long fires when score>=+threshold AND
// htfDir==+1; short when score<=-threshold AND htfDir==-1.
func trendEntryDir(trendScore, threshold float64, htfDir int) int {
	if threshold <= 0 {
		return 0
	}
	if trendScore >= threshold && htfDir == 1 {
		return 1
	}
	if trendScore <= -threshold && htfDir == -1 {
		return -1
	}
	return 0
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/strategy/aistrat/ -run TestTrendEntryDir -v`
Expected: PASS (all 8 cases)

- [ ] **Step 5: Commit**

```bash
git add internal/strategy/aistrat/trendscore.go internal/strategy/aistrat/trendscore_test.go
git commit -m "feat(aistrat): trendEntryDir — score + HTF-confirm trend entry trigger"
```

---

## Task 3: `trendAlignPenalty` pure function

**Files:**
- Modify: `internal/strategy/aistrat/trendscore.go`
- Test: `internal/strategy/aistrat/trendscore_test.go`

- [ ] **Step 1: Write the failing test** (append to `trendscore_test.go`)

```go
func TestTrendAlignPenalty(t *testing.T) {
	tests := []struct {
		name             string
		rawConf          float64
		sideSign         int
		score, fullScore float64
		want             float64
	}{
		{"disabled when fullScore<=0", 0.8, 1, -5, 0, 0.8},
		{"long fully penalized at full bearish score", 0.8, 1, -2.5, 2.5, 0.0},
		{"long half penalized at half bearish score", 0.8, 1, -1.25, 2.5, 0.4},
		{"long with-trend (bullish score) unchanged", 0.8, 1, 2.5, 2.5, 0.8},
		{"long flat score unchanged", 0.8, 1, 0, 2.5, 0.8},
		{"short fully penalized at full bullish score", 0.8, -1, 2.5, 2.5, 0.0},
		{"short with-trend (bearish score) unchanged", 0.8, -1, -2.5, 2.5, 0.8},
		{"penalty clamps at zero past full", 0.8, 1, -5, 2.5, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trendAlignPenalty(tt.rawConf, tt.sideSign, tt.score, tt.fullScore)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("trendAlignPenalty(raw=%.2f side=%d score=%.2f full=%.2f) = %.4f, want %.4f",
					tt.rawConf, tt.sideSign, tt.score, tt.fullScore, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/strategy/aistrat/ -run TestTrendAlignPenalty`
Expected: FAIL — `undefined: trendAlignPenalty`

- [ ] **Step 3: Write minimal implementation** (append to `trendscore.go`)

```go
// trendAlignPenalty scales a fade entry's confidence down when it is counter to the
// accumulated trend, proportional to |trendScore|, reaching full (conf→0) at
// fullPenaltyScore. With-trend or flat confidence is unchanged. fullPenaltyScore<=0
// disables it. sideSign is +1 for a long entry, -1 for a short entry.
func trendAlignPenalty(rawConf float64, sideSign int, trendScore, fullPenaltyScore float64) float64 {
	if fullPenaltyScore <= 0 {
		return rawConf
	}
	counter := (sideSign > 0 && trendScore < 0) || (sideSign < 0 && trendScore > 0)
	if !counter {
		return rawConf
	}
	factor := 1.0 - math.Abs(trendScore)/fullPenaltyScore
	if factor < 0 {
		factor = 0
	}
	return rawConf * factor
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/strategy/aistrat/ -run TestTrendAlignPenalty -v`
Expected: PASS (all 8 cases)

- [ ] **Step 5: Commit**

```bash
git add internal/strategy/aistrat/trendscore.go internal/strategy/aistrat/trendscore_test.go
git commit -m "feat(aistrat): trendAlignPenalty — continuous counter-trend conf penalty"
```

---

## Task 4: Config fields + state fields (plumbing)

**Files:**
- Modify: `internal/strategy/aistrat/config.go` (param parse block ~line 45; `Config` struct ~line 279; `DefaultConfig` ~line 454)
- Modify: `internal/strategy/aistrat/strategy.go` (state struct, near `lastHourlyDir` ~line 69)

Config plumbing has no behavior on its own (gated off by callers until Task 5 wires it); verify by build, not a unit test.

- [ ] **Step 1: Add the 7 param-parse lines** in `config.go`, immediately after the `HourlyTrendStickyBars` parse line:

```go
		if v, ok := params["TrendScoreThreshold"]; ok { cfg.TrendScoreThreshold = toFloat(v) }
		if v, ok := params["TrendAlignFullPenaltyScore"]; ok { cfg.TrendAlignFullPenaltyScore = toFloat(v) }
		if v, ok := params["TrendScoreDecay"]; ok { cfg.TrendScoreDecay = toFloat(v) }
		if v, ok := params["TrendScorePerBarCap"]; ok { cfg.TrendScorePerBarCap = toFloat(v) }
		if v, ok := params["TrendScoreMax"]; ok { cfg.TrendScoreMax = toFloat(v) }
		if v, ok := params["TrendScoreConfirmTF"].(string); ok { cfg.TrendScoreConfirmTF = v }
		if v, ok := params["TrendEntryCooldownBars"]; ok { cfg.TrendEntryCooldownBars = toInt(v) }
```

- [ ] **Step 2: Add the 7 struct fields** to `Config` in `config.go`, after the `HourlyTrendStickyBars int` field:

```go
	// ── Trend-score leg (see docs/.../2026-06-30-aistrat-trend-score-leg-design.md) ──
	// Continuous 5m trend-accumulation score replacing the binary regime gate.
	TrendScoreThreshold        float64 // accumulated score to trigger a trend entry; 0 = no trend entry (offense off)
	TrendAlignFullPenaltyScore float64 // |score| at which counter-trend fade conf → 0; 0 = no penalty (defense off)
	TrendScoreDecay            float64 // per-bar decay of trendScore (e.g. 0.9)
	TrendScorePerBarCap        float64 // per-bar delta clamp in ATR units (e.g. 1.0)
	TrendScoreMax              float64 // |trendScore| cap (e.g. 5.0)
	TrendScoreConfirmTF        string  // higher-TF confirm source: "15m" (lastTrendDir) or "1h" (lastHourlyDir)
	TrendEntryCooldownBars     int     // primary bars to wait after a trend entry before another
```

- [ ] **Step 3: Add the `DefaultConfig` values** in `config.go`, after the `HourlyTrendStickyBars: 0,` line:

```go
		TrendScoreThreshold:        3.5,    // conservative: needs a sustained ~0.35-ATR/bar drift
		TrendAlignFullPenaltyScore: 2.5,    // protect earlier than we commit to a trend entry
		TrendScoreDecay:            0.9,
		TrendScorePerBarCap:        1.0,
		TrendScoreMax:              5.0,
		TrendScoreConfirmTF:        "15m",
		TrendEntryCooldownBars:     12,     // ≈1h on 5m bars
```

- [ ] **Step 4: Add the 2 state fields** to the strategy struct in `strategy.go`, after the `hourlyDirCooldown` field:

```go
	trendScore         float64 // signed 5m trend-accumulation score; see trendscore.go
	trendEntryCooldown int     // primary bars remaining before another trend-score entry
```

- [ ] **Step 5: Build to verify it compiles**

Run: `go build ./... && go test ./internal/strategy/aistrat/`
Expected: builds clean; existing aistrat tests still PASS

- [ ] **Step 6: Commit**

```bash
git add internal/strategy/aistrat/config.go internal/strategy/aistrat/strategy.go
git commit -m "feat(aistrat): config + state for trend-score leg (default-on conservative)"
```

---

## Task 5: Wiring in signal.go (score update, penalty, trend entry)

**Files:**
- Modify: `internal/strategy/aistrat/signal.go` (score update after `s.barCount++` ~line 108 area / the regime block ~line 232; penalty after `techBuySignal`/`techSellSignal` ~line 289-290; trend-entry trigger after the normal open block ~line 675)

This is integration glue (needs a full `*strategy.Context` to exercise) — pure-function tests cover the logic; verify wiring by build + existing package tests + vet. Match the exact surrounding code before inserting; line numbers are approximate.

- [ ] **Step 1: Per-bar score update + cooldown decrement.** In `generateSignal`, right after the hysteresis update of `s.lastHourlyDir` (the `stickyHourlyDir(...)` call added by `802ca77`), add:

```go
	// ── Trend-score: accumulate on each primary bar (5m), decay cooldown ──
	s.trendScore = updateTrendScore(s.trendScore, bar.Close-bar.Open, atr,
		s.cfg.TrendScoreDecay, s.cfg.TrendScorePerBarCap, s.cfg.TrendScoreMax)
	if s.trendEntryCooldown > 0 {
		s.trendEntryCooldown--
	}
```

Note: `atr` is computed earlier in `generateSignal` (`atr := s.calcATR()` near the volatility guard). If `atr` is not in scope at this point, call `s.calcATR()` inline.

- [ ] **Step 2: Apply the counter-trend penalty** to the technical confidences. Immediately after:

```go
	longConf, longEntry := s.techBuySignal()
	shortConf, shortEntry := s.techSellSignal()
```

insert:

```go
	// Continuous trend-alignment penalty (replaces dependence on the binary regime
	// gate; with-trend / flat score leaves conf unchanged). See trendscore.go.
	longConf = trendAlignPenalty(longConf, 1, s.trendScore, s.cfg.TrendAlignFullPenaltyScore)
	shortConf = trendAlignPenalty(shortConf, -1, s.trendScore, s.cfg.TrendAlignFullPenaltyScore)
```

- [ ] **Step 3: Trend-score entry trigger.** After the existing "Open LONG/SHORT if confident" block (after the `s.openTrend(ctx, "SHORT", ...)` / short open branch, before the function returns), add:

```go
	// ── Trend-score entry: 5m accumulation + HTF confirm opens a trend position ──
	// Independent of the technical-confidence entries above; guarded by no-rebuy
	// (s.longPos/shortPos == nil) and a cooldown so it can't stack on chop wobble.
	if s.trendEntryCooldown == 0 {
		htfDir := s.lastTrendDir
		if s.cfg.TrendScoreConfirmTF == "1h" {
			htfDir = s.lastHourlyDir
		}
		switch trendEntryDir(s.trendScore, s.cfg.TrendScoreThreshold, htfDir) {
		case 1:
			if s.longPos == nil {
				s.lastConf = 1.0
				s.openTrend(ctx, "LONG", price, math.Round(price*100)/100, atr, 0)
				if s.longPos != nil {
					s.longPos.entryRegime = regime
					s.trendEntryCooldown = s.cfg.TrendEntryCooldownBars
					s.log.Info("AI: TREND-SCORE entry", zap.String("side", "LONG"),
						zap.Float64("score", s.trendScore), zap.Int("htf", htfDir))
				}
			}
		case -1:
			if s.shortPos == nil {
				s.lastConf = 1.0
				s.openTrend(ctx, "SHORT", price, math.Round(price*100)/100, atr, 0)
				if s.shortPos != nil {
					s.shortPos.entryRegime = regime
					s.trendEntryCooldown = s.cfg.TrendEntryCooldownBars
					s.log.Info("AI: TREND-SCORE entry", zap.String("side", "SHORT"),
						zap.Float64("score", s.trendScore), zap.Int("htf", htfDir))
				}
			}
		}
	}
```

Note: confirm `price`, `atr`, `regime`, `ctx`, and `zap` are in scope at the insertion point (they are used by the surrounding open block). `math` is already imported in signal.go.

- [ ] **Step 4: Build, test, vet**

Run:
```bash
go build ./... && go test ./internal/strategy/aistrat/ && go vet ./internal/strategy/aistrat/
```
Expected: builds clean; all aistrat tests PASS (incl. the 3 new pure-fn tests); vet clean.

- [ ] **Step 5: Regression check on neighbours**

Run: `go test ./internal/strategy/... ./internal/live/...`
Expected: all PASS except the pre-existing `TestLiveBroker_DuplicateOrderBlocked` failure in `internal/live` (unrelated — fails on base too; do NOT fix here).

- [ ] **Step 6: Commit**

```bash
git add internal/strategy/aistrat/signal.go
git commit -m "feat(aistrat): wire trend-score leg into generateSignal (penalty + entry)"
```

---

## Self-Review (completed by plan author)

**Spec coverage:**
- §3.1 score accumulation → Task 1 (`updateTrendScore`) + Task 5 Step 1 (per-bar wiring). ✓
- §3.2 entry trigger + HTF confirm + no-rebuy guard + cooldown → Task 2 (`trendEntryDir`) + Task 5 Step 3. ✓
- §3.3 continuous penalty + application location → Task 3 (`trendAlignPenalty`) + Task 5 Step 2. ✓
- §5 config (7 params, default-on conservative, two gates) → Task 4. ✓
- §6 testing (3 table tests) → Tasks 1-3 Step 1. ✓
- §4 keep old mechanisms → nothing deleted; penalty stacks with existing clamp (Task 5 Step 2 inserts, doesn't remove). ✓

**Placeholder scan:** No TBD/TODO; every code step has complete code. The "approximate line numbers / confirm scope" notes are guidance for an existing-file insert, not missing code.

**Type consistency:** `updateTrendScore(prev,body,atr,decay,perBarCap,scoreMax)`, `trendEntryDir(trendScore,threshold,htfDir)`, `trendAlignPenalty(rawConf,sideSign,trendScore,fullPenaltyScore)` — signatures identical across Tasks 1-3 and their call sites in Task 5. Config field names match struct ↔ parse ↔ default ↔ call sites. State fields `trendScore`/`trendEntryCooldown` consistent. ✓

## Deployment (after plan complete, separate from coding)

Build linux binary, back up live binary, scp, checksum, install + restart, verify (rev, both engines active, no panics). Default-on means both demo engines run it immediately (= paper-forward). To A/B: `set TrendScoreThreshold=0 + TrendAlignFullPenaltyScore=0` on one engine via params. Watch `~/quantix-ops/` logs + `TREND-SCORE entry` log lines + per-engine net.
