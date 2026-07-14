# Position Guardian — Design Spec

**Date:** 2026-07-14
**Status:** Approved for implementation (full scope, not MVP)

## Goal

A tool that manages **risk and attention** on positions the user opens themselves.
It does NOT predict direction or generate entry signals. It does three things the
user (and every human) struggles to do consistently:

1. **Enforce a stop-loss** — mechanically, without hesitation.
2. **Trail the stop up** as the trade moves in favour (lock profit, let winners run).
3. **Watch and alert** — surface facts and position events so the user reacts fast.

The honest positioning: *a risk + discipline + monitoring cockpit for your own
trades.* The edge is the user's; the Guardian keeps them alive and disciplined.

## Non-Goals (honest boundaries)

- No direction prediction, no buy/sell signals, no "AI that makes money."
- No promise of profit. If the user's direction calls are 50/50, the Guardian
  keeps them disciplined and un-blown-up, but direction P&L stays ~0.
- Not a backtested-edge strategy. It is a protective/monitoring layer.

## Architecture

The Guardian is implemented as a **`strategy.Strategy`** so it drops into the
existing live engine, EngineManager, OMS, order-risk gateway (ORG), broker, and
WS price feed with no new execution plumbing.

```
User (Guardian UI) ──arm──▶ Guardian engine (strategy type "guardian")
                                │  reads position via ctx.Portfolio.Position()
   each bar/tick:              ▼
     update trailing stop ─▶ stop/TP hit? ─▶ ctx.ClosePosition() ─▶ ORG ─▶ broker
     evaluate alert rules ─▶ notify.Notifier (Telegram / email)
```

### Reuse map (verified in code 2026-07-14)

| Capability | Reused from |
| --- | --- |
| Tick/bar loop, session recovery | `internal/live/engine.go` |
| Order submit + idempotency + ORG risk gate | `ctx.PlaceOrder` → `internal/orgateway` → broker |
| Close position | `ctx.ClosePosition(symbol)` |
| Position read | `ctx.Portfolio.Position(symbol) (qty, avg, ok)` |
| Trailing-stop math (activation/ratchet/pullback) | ported from `aistrat/hedge_tier.go: manageGridTrail` |
| Alert delivery | `internal/notify` (`telegram.go`, `email.go`) |
| Status for dashboards | `strategy.StatusReporter.Status()` |
| Web UI framework + Positions/Orders pages | `web/src/pages/` |

### New code

- `internal/guardian/` — protection state, trailing math, ATR, alert engine, the
  Guardian strategy.
- Engine/strategy factory registration for type `"guardian"`.
- API endpoints for CRUD + live status of guardians.
- Manual **open-order** endpoint (arm-with-entry; today only close-position exists).
- `web/src/pages/Guardian.tsx` + nav + API client.

## Components (`internal/guardian/`)

### `atr.go`
Rolling ATR over the price/bar stream (Wilder-ish mean of true range). Used for
volatility-adaptive stop distance. Pure function, table-tested.

### `protection.go`
`Protection` config + `protectionState`:
- **Initial stop mode**: `price` (absolute), `pct` (e.g. 3%), or `atr` (k×ATR).
- **Trailing**: `enabled`, `activateR` (activate once P&L ≥ this in R), `trailR` or
  `trailAtrK` (ratchet distance), ratchet only in favour.
- **Take-profit** (optional): `price`, `pct`, or `rMultiple`.
- **State**: side, entry, qty, initial risk R, current stop, activated, peak R.
- Methods: `NewProtection(...)`, `updateStop(price, atr)`, `stopHit(price)`,
  `tpHit(price)`, `pnlR(price)`. All pure/unit-testable, no I/O.

### `alerts.go`
`AlertRule` interface + concrete rules, an `AlertEngine` that evaluates rules each
bar/tick, dedups via per-rule cooldown, and dispatches via an injected
`Dispatcher` (Telegram/email).

**Position-class rules** (about the open trade):
- `ProfitMilestoneRule` — fire at +1R, +2R, … (suggest trailing).
- `StopProximityRule` — price within X of the stop.
- `StagnationRule` — held N bars with |P&L| < threshold.
- `VolSpikeRule` — ATR jumped ≥ k× its recent average (stop may be too tight).

**Market-fact rules** (statements, never predictions):
- `LevelCrossRule` — price crossed a user level.
- `MAStateRule` — price crossed above/below its N-period MA (stated as fact).
- `VolRegimeRule` — volatility regime changed bucket.

Each rule returns `(fire bool, message string)`. The engine adds a cooldown so a
condition straddling a threshold doesn't spam.

### `guardian.go`
`Guardian` implements `strategy.Strategy` + `TickReceiver` + `StatusReporter`:
- Config: symbol, `Protection`, `[]AlertRule`, optional `EntryOrder` (arm-with-entry),
  `adoptExisting bool` (protect a pre-existing account position).
- `OnBar`: update ATR, update trailing stop, evaluate market-fact + position alerts,
  check stop/TP → `ctx.ClosePosition`.
- `OnTick`: precise stop/TP check + proximity alerts between bars.
- `OnFill`: if the protective close filled → alert "stopped out / took profit @ px",
  mark done. If arm-with-entry filled → initialise `Protection` from fill price.
- `Status`: `{state, side, entry, qty, stop, pnl_r, trail_active, peak_r, ...}` for UI.
- Never opens except the optional single arm-entry; never re-enters after exit.

## Feature 1 — Stop + trailing stop

- Arming: user supplies symbol, side, qty (or "adopt current position"), and stop/
  trail/TP config. If arm-with-entry, Guardian places the entry, then initialises
  protection from the fill.
- Initial risk `R` = |entry − initialStop| (per unit); all P&L reported in R.
- Trailing: once `pnlR ≥ activateR`, track peak; ratchet stop so it never loosens;
  trigger close on pullback ≥ `trailR` from peak **or** when price ≤ trailed stop.
- ATR mode makes both the initial stop and the trail distance adapt to volatility
  (mitigates whipsaw-vs-giveback). Sensible defaults set by us, user-tunable.
- Every close routes through ORG → broker as a **reduce-only** order; fill triggers
  a Telegram/email confirmation.

## Feature 2 — Alerts / monitoring

- User composes rules per guardian (or global watch rules with no position).
- Delivery via `internal/notify`: Telegram (primary) and email (both wired).
- Cooldown per rule to avoid spam; all messages are **facts or position events**,
  never "buy/sell now."

## Integration

- **Strategy factory**: register `"guardian"` so an engine can run it like any
  strategy; params parsed from JSON (protection + alert-rule config).
- **Position adoption**: when `adoptExisting`, read the live account position via
  `ctx.Portfolio.Position` (backed by PositionSyncer in `ctx.Extra`) at startup and
  protect it. Handles the "I already opened on the exchange, now guard it" case.
- **Persistence & recovery**: guardian config persisted with the engine session so a
  restart re-adopts the position and re-arms protection (reuse existing session
  recovery). Trailing peak/stop state persisted so a restart does not reset the trail.

## API

- `POST   /api/guardians` — create (symbol, protection, alerts, optional entry).
- `GET    /api/guardians` — list with live status.
- `GET    /api/guardians/{id}` — one guardian's live status.
- `PATCH  /api/guardians/{id}` — adjust stop/trail/TP/alerts live.
- `DELETE /api/guardians/{id}` — disarm (optionally flatten).
- `POST   /api/orders` — **new** manual open-order endpoint (for arm-with-entry),
  routed through ORG. (Today only `close-position` exists.)

## Web UI — `Guardian.tsx`

- **Arm panel**: pick symbol/side/qty (or "adopt current position"); set initial
  stop (price/%/ATR), trailing (activate R, trail R/ATR), take-profit; pick alert
  rules with thresholds.
- **Live table**: per guardian → current price, current stop, distance-to-stop,
  P&L in R, trail state, alerts fired. Manual "disarm" / "flatten now".
- **Alerts log**: recent fired alerts.
- Nav entry + Zustand store slice + API client methods.

## Testing

- Unit (TDD): `atr`, `protection` (all stop modes, trailing activation/ratchet/
  pullback, TP), each `AlertRule`, cooldown/dedup, `Guardian` OnBar/OnTick/OnFill
  via a fake Context + fake broker (assert close placed exactly when stop/TP hit,
  never re-enters).
- Integration: run Guardian in the live engine against the paper/demo broker;
  verify a real reduce-only close fires and a Telegram alert is delivered.

## Rollout

1. Core package (atr, protection, trailing, guardian) — unit-green.
2. Alerts engine + notify dispatch — unit-green.
3. Factory registration + config + adoption + persistence/recovery.
4. API endpoints + manual open-order.
5. Web UI page + nav + client.
6. Paper/demo E2E on user's existing demo account; then document.
