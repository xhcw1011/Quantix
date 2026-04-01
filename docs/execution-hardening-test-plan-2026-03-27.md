# Quantix Execution Hardening Test Plan — 2026-03-27

## Purpose

This document defines the next validation stage required before Quantix can credibly move from:

- serious beta / controlled live-test system

toward:

- production-ready Binance quantitative trading platform

The focus here is **execution hardening**, not feature expansion.

This means testing how the system behaves under:
- network faults
- exchange response ambiguity
- restart/recovery conditions
- order lifecycle edge cases
- state divergence risks
- protective-order failure scenarios

---

## Scope

Primary scope:
- Binance Spot execution path
- Quantix live engine
- OMS
- order persistence
- restart/recovery behavior
- operator safety behavior

Secondary scope:
- Binance Futures execution path where implemented
- engine manager auto-restart behavior
- DB/exchange consistency checks

---

## Acceptance Principle

Quantix should not be considered execution-hardened until it demonstrates:

1. **No silent loss of critical trading state**
2. **No uncontrolled duplicate order creation**
3. **No hidden orphaned positions/orders after failures**
4. **Recoverable operator workflows after restart/fault conditions**
5. **Clear alerting when the system cannot self-heal safely**

---

## Test Priority Legend

- **T0** = mandatory before meaningful live deployment
- **T1** = strongly recommended before scaling usage
- **T2** = maturity validation / long-term hardening

---

# T0 — Mandatory Execution Hardening Tests

## T0-1. Submit timeout / ambiguous order result test

### Goal
Validate behavior when an order submit attempt times out or the connection breaks during submission.

### Why it matters
This is one of the most dangerous live-trading edge cases.
The exchange may have accepted the order even if the client did not receive confirmation.

### Validate
- same `clientOrderID` is reused on retry where intended
- no duplicate position is opened
- internal OMS state remains reconcilable
- order history and exchange state can be matched afterward

### Pass criteria
- no duplicate live order is created
- final order state can be reconciled from exchange + DB
- operator can identify exactly what happened

---

## T0-2. WebSocket interruption while engine is running

### Goal
Validate live-engine behavior when market-data streaming is interrupted.

### Validate
- engine does not panic
- engine does not silently keep trading on stale assumptions
- reconnect behavior is visible and recoverable
- kline flow resumes correctly after reconnect

### Pass criteria
- engine remains operational or fails clearly
- no hidden state corruption
- no uncontrolled order behavior after reconnect

---

## T0-3. Process crash / forced restart with active orders

### Goal
Validate that a crash or hard stop with active/open orders can be recovered safely.

### Validate
- session persistence
- auto-restart behavior
- OMS recovery from DB
- exchange reconciliation logic
- correct treatment of active orders after restart

### Pass criteria
- no ghost engine state
- no duplicate order replay
- no hidden open exchange orders left unmanaged
- recovered state matches exchange truth closely enough for safe continuation or safe halt

---

## T0-4. Shutdown with open orders / protective orders

### Goal
Validate safe shutdown behavior when stop-loss / take-profit / pending orders exist.

### Validate
- `CancelAllPendingOrders(...)` behavior
- retry on transient cancel failure
- alert path if cancellation fails
- OMS state after shutdown attempt

### Pass criteria
- open exchange orders are cancelled when possible
- failures are clearly surfaced to operator
- no silent orphaning of protective orders

---

## T0-5. Partial fill handling test

### Goal
Validate that partial fills do not corrupt OMS, positions, or accounting.

### Validate
- incremental fill handling
- repeated polling does not double-count fills
- final position size matches exchange truth
- partial-to-full transitions are correct

### Pass criteria
- no duplicate fill accounting
- no incorrect position quantity
- OMS status transitions are correct

---

## T0-6. DB state vs exchange state divergence test

### Goal
Validate behavior when persisted order state and exchange order state disagree.

### Examples
- DB says OPEN, exchange says FILLED
- DB says OPEN, exchange says CANCELLED
- DB says PENDING with no exchange ID
- DB says active, exchange query fails repeatedly

### Pass criteria
- recovery path is deterministic
- system chooses a safe outcome
- operator can understand what was done and why

---

# T1 — High-Value Hardening Tests

## T1-1. Duplicate-order suppression stress test

### Goal
Validate the `FindPending(...)` soft idempotency path under repeated submissions.

### Validate
- repeated same-side submissions during pending state
- retries under slow exchange confirmation
- no double-position creation

### Pass criteria
- duplicate order attempts are blocked or safely reconciled

---

## T1-2. Long-duration live soak test

### Goal
Validate engine stability over extended runtime.

### Suggested durations
- minimum: 4 hours
- preferred: 24 hours
- ideal: several trading sessions or longer

### Validate
- reconnections
- memory growth
- CPU behavior
- kline continuity
- fill pipeline stability
- alert noise level

### Pass criteria
- no leak-like instability
- no unexplained engine stoppage
- no silent data/feed degradation

---

## T1-3. Protective-order failure handling test

### Goal
Validate operator safety when stop-loss / take-profit placement fails.

### Validate
- critical logging
- notification path
- visibility of unprotected state
- manual intervention workflow

### Pass criteria
- operator receives unambiguous warning
- failed-protection state is not silent
- incident can be triaged quickly

---

## T1-4. Engine manager lifecycle test

### Goal
Validate start/stop/list/status/autorestart behavior as an operator would actually use it.

### Validate
- repeated start/stop cycles
- engine already running cases
- stopped-session cleanup
- restart after persisted active session
- admin force-stop behavior

### Pass criteria
- lifecycle is deterministic and understandable
- no zombie sessions remain

---

## T1-5. Backfill + live handoff test

### Goal
Validate the transition from REST backfill to live websocket bar stream.

### Why it matters
This handoff is a classic source of:
- duplicated bars
- missing bars
- strategy double-triggering
- desynchronized live state

### Pass criteria
- no missing or duplicated actionable bars
- no duplicate trading trigger caused by handoff

---

# T2 — Maturity Tests

## T2-1. Spot capability boundary test

### Goal
Document and verify the exact production-safe feature boundary for Binance Spot.

### Validate
- what is supported
- what is intentionally unsupported
- what the operator must not assume

### Expected outcome
A precise support matrix for:
- market entries
- exits
- protection behavior
- restart expectations

---

## T2-2. Futures-specific hedge-mode and leverage validation

### Goal
Validate Binance Futures edge cases where supported.

### Validate
- leverage setting
- hedge mode / one-way expectations
- stop / take-profit handling
- margin-ratio monitoring

---

## T2-3. Alerting quality review

### Goal
Evaluate whether current alerting is sufficient for real operators.

### Validate
- too noisy vs too quiet
- critical alerts actually actionable
- failure messages understandable to operator

---

## T2-4. Operational runbook rehearsal

### Goal
Simulate real incident response.

### Scenarios
- exchange timeout during order
- restart with active order
- protective-order failure
- open order remains on exchange after shutdown

### Pass criteria
- operator can recover without guessing
- documented steps are sufficient

---

# Test Matrix Format

Each hardening test should be recorded using this structure:

## Test record template

- **Test ID:**
- **Priority:** T0 / T1 / T2
- **Scenario:**
- **Setup:**
- **Trigger:**
- **Expected behavior:**
- **Observed behavior:**
- **Result:** PASS / FAIL / PARTIAL
- **Risk if unresolved:**
- **Follow-up action:**

---

# Minimum Go-Live Gate

Before calling Quantix execution-hardened for Binance live trading, the following should be completed at minimum:

## Required minimum
- T0-1 submit timeout / ambiguous result
- T0-2 websocket interruption
- T0-3 crash/restart with active orders
- T0-4 shutdown with open/protective orders
- T0-5 partial fill handling
- T0-6 DB/exchange divergence
- T1-2 long-duration soak test

If these are not completed, the system should remain labeled:

> controlled live-test system

not

> production-ready trading system

---

# Current Status

At the time of writing, these tests are mostly **planned**, not fully executed.

What is already covered by current smoke tests:
- Binance auth
- direct order placement
- roundtrip order execution
- internal OMS/fill/position propagation

What is **not yet covered enough**:
- fault-tolerance under stress
- restart correctness under messy conditions
- partial-fill correctness in real exchange behavior
- prolonged operational stability

---

# Next Recommended Deliverable

After this plan, the next high-value step is to create:

## `docs/execution-hardening-test-results-2026-03-27.md`

This should record actual outcomes test-by-test, rather than only the plan.

---

## Document Status

Saved on: 2026-03-27
Location: `docs/execution-hardening-test-plan-2026-03-27.md`
