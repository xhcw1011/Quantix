# Position Guardian — Implementation Plan

> **For agentic workers:** Execute task-by-task with TDD. Steps use `- [ ]` checkboxes.

**Goal:** A protective stop + trailing-stop + monitoring/alert layer for user-opened
positions, implemented as a `strategy.Strategy` for maximal reuse of the live engine.

**Architecture:** `internal/guardian/` (pure logic, unit-tested) + factory registration
+ API + React UI. See `docs/superpowers/specs/2026-07-14-position-guardian-design.md`.

**Tech Stack:** Go 1.24, existing live engine/OMS/ORG/notify, React+Zustand+Vite.

---

## Phase 1 — Core protective logic (`internal/guardian/`)

### Task 1: ATR helper
**Files:** Create `internal/guardian/atr.go`, `internal/guardian/atr_test.go`
- [ ] Test: `NewATR(window)` fed a known TR sequence returns expected rolling ATR; zero before warmup.
- [ ] Implement `ATR` struct: `Update(high, low, prevClose) float64`, `Value() float64`, `Ready() bool`.
- [ ] Green + `go test ./internal/guardian/`.

### Task 2: Protection state machine
**Files:** Create `internal/guardian/protection.go`, `protection_test.go`
- [ ] Types: `StopMode` (`price|pct|atr`), `Protection` config, `NewProtection(side, entry, qty, cfg, atr)`.
- [ ] Test: initial stop computed correctly for each mode; `R = |entry-stop|`.
- [ ] Test: `pnlR(price)` sign correct for long/short.
- [ ] Test: `updateStop(price, atr)` — before activation stop is fixed; after `pnlR>=activateR` the stop ratchets in favour and never loosens.
- [ ] Test: `stopHit`/`tpHit` fire at the right prices for long AND short.
- [ ] Implement; green.

### Task 3: Guardian strategy
**Files:** Create `internal/guardian/guardian.go`, `guardian_test.go`
- [ ] Implements `strategy.Strategy` (`Name`,`OnBar`,`OnFill`) + `TickReceiver` (`OnTick`) + `StatusReporter` (`Status`).
- [ ] Test (fake Context + fake broker): long position, price runs up then pulls back past trail → exactly one reduce-only close placed; no re-entry after.
- [ ] Test: stop hit on a tick between bars → close placed on `OnTick`.
- [ ] Test: TP hit → close placed.
- [ ] Test: `OnFill` of the protective close marks state `done`, emits an alert.
- [ ] Test: `Status()` exposes stop, pnl_r, trail_active, peak_r.
- [ ] Implement (port trailing from `aistrat/hedge_tier.go: manageGridTrail`); green + `go vet`.

## Phase 2 — Alerts

### Task 4: Alert rules + engine
**Files:** Create `internal/guardian/alerts.go`, `alerts_test.go`
- [ ] `AlertRule` interface `Eval(ctx alertCtx) (fire bool, msg string)`; `alertCtx` carries price, pnlR, atr, barsHeld, MA, etc.
- [ ] Position rules: `ProfitMilestone`, `StopProximity`, `Stagnation`, `VolSpike`.
- [ ] Market-fact rules: `LevelCross`, `MAState`, `VolRegime`.
- [ ] `AlertEngine` with per-rule cooldown; `Dispatcher` interface (send(title,msg)).
- [ ] Test each rule fires only under its condition; cooldown suppresses repeats; messages are factual.
- [ ] Implement; green.

### Task 5: Notify dispatcher adapter
**Files:** Create `internal/guardian/dispatch.go`, `dispatch_test.go`
- [ ] Adapter implementing `Dispatcher` over `internal/notify` (Telegram + optional email); fake in tests.
- [ ] Wire `AlertEngine` into `Guardian.OnBar/OnTick`.
- [ ] Test: Guardian fires ProfitMilestone at +1R via a fake Dispatcher.

## Phase 3 — Engine integration

### Task 6: Strategy factory + config parsing
**Files:** Modify the strategy factory/registry; `internal/guardian/config.go`
- [ ] Register type `"guardian"`; parse Protection + alert-rule config from params JSON.
- [ ] Defaults (ATR-adaptive): initialStop `atr` k=2, trail activate 1R, trail 1.5R (ATR-scaled), TP off.
- [ ] Test: params JSON → correct Guardian config.

### Task 7: Position adoption + persistence/recovery
**Files:** wiring in engine startup path
- [ ] `adoptExisting`: read live position via `ctx.Portfolio.Position` at start; init Protection from avg price.
- [ ] Persist trailing peak/stop with the engine session; on restart re-adopt + restore trail state (reuse session recovery).
- [ ] Test: restart mid-trade restores stop/peak (not reset).

## Phase 4 — API

### Task 8: Guardian CRUD + status endpoints
**Files:** `internal/api/handlers_guardian.go`, routes in `server.go`
- [ ] `POST/GET/GET{id}/PATCH/DELETE /api/guardians`; status from `Strategy.Status()`.
- [ ] Tests: handler validation (bad body, unknown id).

### Task 9: Manual open-order endpoint
**Files:** `internal/api/handlers_trading.go`, `manager.go`
- [ ] `POST /api/orders` → routed through ORG → broker (for arm-with-entry).
- [ ] Tests: validation + ORG rejection path.

## Phase 5 — Web UI

### Task 10: Guardian page + store + client
**Files:** `web/src/pages/Guardian.tsx`, store slice, API client, nav in `Layout.tsx`
- [ ] Arm panel (symbol/side/qty or adopt; stop/trail/TP; alert rules).
- [ ] Live table (price, stop, distance, pnl R, trail state) + disarm/flatten.
- [ ] Alerts log.
- [ ] `npm run build` clean.

## Phase 6 — E2E + docs

### Task 11: Paper/demo E2E + docs
- [ ] Arm a guardian on the user's demo account; verify reduce-only close fires and Telegram alert delivered.
- [ ] Update memory + `docs/` with usage.

---

## Self-review checklist
- Every stop mode (price/pct/atr) tested for long AND short.
- Trailing ratchets only in favour; never loosens; survives restart.
- Alerts are factual/position-based only — no predictive "buy/sell" language.
- Close orders are reduce-only and pass through ORG.
- Guardian never opens (except optional single arm-entry) and never re-enters.
