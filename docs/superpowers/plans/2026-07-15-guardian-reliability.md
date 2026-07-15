# Guardian Reliability Upgrade — Implementation Plan

> Execute task-by-task with TDD. Steps use `- [ ]` checkboxes.

**Goal:** Make the Guardian's protection reliable enough to trust unattended:
① an exchange-native resting stop that survives bot/server death, ② trail state
that survives restart, ③ correct behaviour when the position changes or is closed
externally.

**Key insight:** The live broker already supports resting STOP_MARKET orders
(`broker.go` handles `OrderStopMarket`; `broker_protective.go` persists protective
orders to DB and recovers them on restart). The Guardian just needs to USE this via
the portable Context interface (`ctx.PlaceOrder` + `ctx.CancelOrder`), not reinvent it.

**Design shift:** Today the Guardian is the trigger ("watch price, place close when
hit"). After this, the *exchange* is the trigger: the Guardian places a resting
STOP_MARKET and its job becomes *advancing* that order as the trail ratchets. If the
bot dies, the exchange still executes the stop. In environments where a resting stop
can't be placed (returns no id), it falls back to today's tick-based close.

**Branch:** `feat/guardian-reliability` (off main).

---

## Task 1: Resting stop on arm (reliability backstop)

**Files:** `internal/guardian/guardian.go`, `guardian_reliability_test.go`

- [ ] Test (fake broker records orders + cancels): when armed, Guardian places a
  `OrderStopMarket` reduce-only order at `prot.Stop`, side = close side (long→SELL,
  short→BUY), qty = position qty. Track the returned id in `g.stopOrderID`.
- [ ] Test: if a resting stop is active, a stop-hit tick does NOT place a second
  close (exchange owns the trigger); OnFill of the stop marks `done`.
- [ ] Test: if PlaceOrder returns "" (resting unsupported), fall back to tick-based
  close (today's behaviour) — no regression.
- [ ] Implement: `placeRestingStop(ctx)`; set `g.restingMode = (id != "")`; guard
  the tick/bar close path on `!g.restingMode`.

## Task 2: Advance the resting stop as the trail ratchets

**Files:** `internal/guardian/guardian.go`, test

- [ ] Test: price runs up so the trailed stop rises by ≥ the replace epsilon →
  Guardian cancels the old stop id and places a new STOP_MARKET at the higher price;
  `g.stopOrderID` updates; a tiny rise below epsilon does NOT churn a replace.
- [ ] Test: the resting stop is never moved against the position (never lowered for
  a long / raised for a short).
- [ ] Implement: after `prot.UpdateStop`, if `restingMode` and stop advanced ≥ eps,
  `ctx.CancelOrder(old)` + `placeRestingStop`.

## Task 3: Restart recovery (trail state survives restart)

**Files:** `internal/guardian/state.go` (+ store wiring), test

- [ ] Test: a Guardian restored from persisted state (stop, peak, activated) keeps
  the trailed stop — does NOT reset to the initial protective level.
- [ ] Implement a small `GuardianState{Stop, PeakR, Activated, StopOrderID}` persisted
  per engine (JSON blob keyed by engineID), saved whenever the stop advances and
  loaded on construction. On restore, seed `prot.Stop/PeakR/activated` and re-adopt
  the existing resting stop id (cancel+replace to re-assert if needed).
- [ ] Wire save/load through the engine (reuse the session-persistence path).

## Task 4: Position re-sync / external-change detection

**Files:** `internal/guardian/guardian.go`, test

- [ ] Test: position goes to 0 (user closed manually / liquidated) → Guardian cancels
  its resting stop and marks `done`; places no close on a flat account.
- [ ] Test: position qty changes (user added/reduced) → Guardian cancels + replaces
  the resting stop with the new qty; R and entry are NOT silently corrupted.
- [ ] Implement: each bar re-read `ctx.Portfolio.Position(symbol)`; branch on
  qty==0 (retire) vs qty changed (resize stop) vs unchanged (normal).

## Task 5: Notify + build/vet + merge

- [ ] Alert (via existing dispatcher) on: resting stop placed, stop advanced, stop
  filled, position retired. (Transparency — ties to the "notify every action" idea.)
- [ ] `go build ./...` + `go test ./internal/guardian/` + `go vet` green.
- [ ] Merge `feat/guardian-reliability` → main.

## Self-review checklist
- Resting stop is reduce-only; never flips the position.
- Exactly one close path active at a time (resting OR tick fallback) — no double-close.
- Trail only ratchets in favour, on the exchange too.
- Restart restores the trailed stop, never loosens it.
- Flat/changed position handled without erroring or orphaning a stop order.
