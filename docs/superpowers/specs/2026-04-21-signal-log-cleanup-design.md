# Signal Log Cleanup + Dead GPT Code Removal

**Date:** 2026-04-21
**Scope:** `internal/strategy/aistrat/`
**Approach:** two sequential commits, each independently verifiable

---

## Background

AIStrategy was originally GPT-scored. An earlier change stripped GPT from the
per-bar signal loop (`signal.go:273` comment: "Pure technical signals — GPT
removed"), but left behind:

1. Log strings still prefixed with `"AI: ..."` / `"AI signal → ..."` — 69
   occurrences across 7 files. Misleading since the signal path is now 100%
   rule-based.
2. `emergencyReversalCheck` / `processEmergencyResult` in `strategy.go` — still
   compile, reference `callGPT`, but have **zero callers** (verified via
   ripgrep). Dead code.
3. Full `gpt.go` (249 lines, 6 funcs + 4 types). Dead code except for
   `replaySignals` plumbing in `signal.go:40-46, 108-110`.
4. GPT config params (`Model`, `GPTTemperature`, `GPTMaxTokens`, `GPTTimeout`)
   still loaded in `config.go:128-130`. Unused.
5. The per-bar summary log emits `raw_L=0, raw_S=0` with no intermediate values
   — no way to see **why** a signal didn't fire (BB distance? RSI? breakout
   gap?).

---

## Non-goals

- No change to regime detection logic (`detectRegime`).
- No change to signal thresholds or entry gates.
- No change to order flow / position management.
- No rename of package `aistrat` or struct `AIStrategy`.
- **Does not fix the "slow-rise blindspot"** — that is Line 2, a separate
  brainstorm round.

---

## Commit 1 (Phase 1a): Log rename + diagnostic fields

**Intent:** Zero behavior change. Pure observability improvement.

### 1.1 Log prefix rename

Mechanical string replacement in log message literals only (not in comments,
not in struct field names, not in function names):

| From | To |
|---|---|
| `"AI signal → "` | `"SIG → "` |
| `"AI: ..."` | `"SIG: ..."` |
| `"AI warmed up"` | `"SIG warmed up"` |

Affected files (exact occurrence counts from grep):

| File | Count |
|---|---|
| `signal.go` | 26 |
| `exit.go` | 15 |
| `manage.go` | 8 |
| `strategy.go` | 7 |
| `entry.go` | 6 |
| `helpers.go` | 4 |
| `gpt.go` | 3 |

Total: 69. (`gpt.go`'s 3 occurrences will be deleted in Phase 1b anyway;
renaming them in Phase 1a keeps the replacement mechanical.)

### 1.2 Diagnostic fields on the per-bar summary line

Location: `signal.go:377-385` (the `AI signal → HOLD/BUY/SELL/BOTH` line).

Current fields: `price, regime, trend_dir, raw_L, raw_S, eff_L, eff_S, L_entry,
S_entry, accum_L, accum_S, call`.

Add regime-appropriate diagnostic fields via a new helper
`buildDiagFields(regime Regime) []zap.Field` in `helpers.go`:

**When `regime == RANGE`** (reversion signal path):
- `bb_lower`, `bb_middle`, `bb_upper`
- `bb_width_pct` = `(bbUpper-bbLower)/price*100`
- `rsi`
- `px_above_lower_pct` = `(price-bbLower)/bbLower*100`
- `px_below_upper_pct` = `(bbUpper-price)/bbUpper*100`

**When `regime != RANGE`** (breakout signal path):
- `hi10` (10-bar high, excluding current bar)
- `lo10` (10-bar low, excluding current bar)
- `breakout_gap_long_pct` = `(price-hi10)/hi10*100`
- `breakout_gap_short_pct` = `(lo10-price)/lo10*100`
- `rsi`
- `bar_range_atr` = `(curBar.High-curBar.Low)/atr`

The helper **recomputes** its values each call (cannot rely on
`s.lastBBLower` etc. — those are only written if the reversion generator
ran past the BB check, which is exactly the path we want to diagnose when it
doesn't fire). BB/RSI/hi-lo computations are O(period) over a slice already
in memory — negligible.

### 1.3 Reject-reason logging in the 4 signal generators

For each early `return 0, 0` in:
- `breakoutBuySignal` (helpers.go:490)
- `breakoutSellSignal` (helpers.go:539)
- `reversionBuySignal` (helpers.go:590)
- `reversionSellSignal` (helpers.go:632)

Add `s.log.Info("sig_reject", zap.String("fn", "..."), zap.String("reason",
"..."), ...)` immediately before the return.

Reject reason code enum (string literals used in logs):

| Code | Trigger |
|---|---|
| `insufficient_bars` | `len(bars) < 20` or `len(closes) < 30` |
| `no_breakout_long` | `price <= highestHigh` |
| `no_breakout_short` | `price >= lowestLow` |
| `bar_range_blowoff` | `barRange > atr*2` |
| `rsi_overbought` | breakout long with `rsi > 80` |
| `rsi_oversold` | breakout short with `rsi < 20` |
| `bb_narrow` | reversion with `bbWidth < price*BBWidthMin` |
| `not_at_bb_lower` | reversion buy with `price > bbLower*1.005` |
| `not_at_bb_upper` | reversion sell with `price < bbUpper*0.995` |

Each reject log includes the failing value (e.g., `zap.Float64("bb_width_pct",
...)` for `bb_narrow`) to enable triage without adding code.

Log level: **Info** (not Debug). Volume: ≤ 4 lines per 5m bar — trivial.

### 1.4 Verification

- `go build ./...`
- `go test ./...` (must pass unchanged)
- Start engine (`./scripts/start-quantix.sh`), observe one 5m bar cycle, confirm:
  - Summary line uses `SIG → ...` prefix.
  - Diagnostic fields are populated and numerically sane.
  - At least one `sig_reject` line appears per bar (slow rise → both reversion
    generators will reject on `not_at_bb_lower` or `not_at_bb_upper`).

---

## Commit 2 (Phase 1b): Dead GPT code removal

**Prerequisite:** Commit 1 merged, live-observed for ≥ 1 bar cycle.
**Intent:** Delete unreachable code. Compiler catches any residual reference.

### 2.1 Files deleted

- `internal/strategy/aistrat/gpt.go` (entire file)

### 2.2 Symbols removed from `strategy.go`

Functions:
- `emergencyReversalCheck` (line ~404)
- `processEmergencyResult` (line ~438)

Types:
- `emergencySignal`

Struct fields on `AIStrategy` (line ~28 and surrounding):
- `signal gptSignal`
- `replaySignals []gptSignal`
- `replayIdx int` (if present — verify)
- `client *http.Client`
- `emergencyCh chan emergencySignal`
- `emergencyActive atomic.Bool`
- `lastEmergencyAt time.Time` (if only used by emergency path — verify)

Constructor updates in `New()`:
- Remove `client: &http.Client{Timeout: cfg.GPTTimeout}`
- Remove `emergencyCh: make(...)`
- Any other GPT-related field initializers

### 2.3 Symbols removed from `config.go`

Config struct fields:
- `Model`
- `GPTTemperature`
- `GPTMaxTokens`
- `GPTTimeout`
- `APIKey` — **keep or remove?** Currently `factory` errors if empty
  (`config.go:146`). Remove both the field and the check, since no HTTP client
  needs it.

Factory param loading (lines ~128-130):
- Remove `GPTTemperature`, `GPTMaxTokens`, `GPTTimeout` loads.
- Remove `APIKey` required-param check.
- Existing live configs in DB with these param keys will be silently ignored
  (the loader uses `if v, ok := params[...]; ok`, so unknown keys are no-ops).

### 2.4 Symbols removed from `signal.go`

- Lines 33-46: `replaySignals` pre-load block (entire branch).
- Lines 102-114: backtest replay mode branch in `liveReady` check — simplify
  to: skip first stale bar only, no replay path.
- Any `nextReplaySignal` / `hasCachedSignals` call sites (confirmed only
  in gpt.go).

### 2.5 Imports to clean up

Expect at least one of these becomes unused after deletion:
- `net/http` (strategy.go)
- `encoding/json` (gpt.go — deleted)
- `context` (may still be used elsewhere)
- `sync/atomic` (only if `emergencyActive` was the sole user)

`goimports`/`go build` will flag any unused import.

### 2.6 Verification

- `go build ./...` — **primary gate**; any missed reference fails here.
- `go test ./...` — all tests pass.
- Check DB: `SELECT params FROM strategy_configs WHERE strategy='ai'` —
  existing configs with `APIKey`, `Model`, `GPT*` params should load cleanly
  (no-op on unknown keys).
- Start engine, verify:
  - No `emergency GPT` / `replay` / `AI signal cache` log lines ever appear.
  - Signal loop output identical to Phase 1a output (just no dead-code
    artifacts in grep).

### 2.7 Risks and mitigations

| Risk | Mitigation |
|---|---|
| Missed reference to removed symbol | Compiler catches it. |
| Live config has `APIKey=required-string`; engine won't start | Remove the required-param check; unknown keys are ignored. |
| Backtest code path depended on replay | Confirm in code: replay was GPT-only; regular backtest does not use it. |
| Revert needed | Phase 1b is a pure deletion commit; `git revert <hash>` cleanly restores it without touching Phase 1a's log improvements. |

---

## Verification gates (between commits)

Between Commit 1 and Commit 2:
1. `go test ./...` green.
2. Live engine runs ≥ 1 bar cycle with new log format.
3. User confirms log output is more useful (reject reasons visible, new
   diagnostic fields readable).

After Commit 2:
1. `go build ./...` green.
2. `go test ./...` green.
3. Live engine starts cleanly, no errors from missing GPT config.
4. Signal loop log output byte-equivalent in structure to Commit 1 output.

---

## Out of scope (next rounds)

- **Line 2: Architectural blindspot fix** — the `driftBuySignal` /
  `driftSellSignal` design for slow-rise markets. Separate brainstorm round
  after Line 1 is live-observed.
- Renaming package `aistrat` or struct `AIStrategy`. Cosmetic, not worth the
  churn now.
- Config schema migration to drop GPT param keys from the DB. Keys are
  no-op'd by the loader; cleanup is optional cosmetic work.
