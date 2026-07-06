# Capital Architecture Layer — Design

**Status: DESIGN (not implemented) · 2026-07-06**

The system's problem is not strategy quality (the baseline showed every strategy at
PF ≈ 0.7) — it is that **capital has no structure**. Trend churns 100%-underwater
with 30–36% drawdown tails while grid/dca sit calm at 4% DD; run from one
undifferentiated capital blob, trend's drawdown drags the whole account and grid's
caution is wasted. The Capital Layer gives capital a **fractal (pooled) structure**
so behaviors are isolated and risk is measured where it is coherent — the pool.

This is **Layer 3** of the risk structure. Layer 1 = ORG (per-order safety,
built). Layer 2 = strategy engines (each manages its own positions, built). Layer 3
= this. Capital *allocation* (weights, regime-adjustment) is a later step; this spec
defines **structure + pool-level risk only**.

---

## 1. Core abstraction: the Pool

A **Pool** is the minimal unit of **capital + risk**:

```
Pool {
  Name         string          // Growth | Yield | Cash
  Members      []StrategyID    // engines assigned to this pool
  NotionalCap  float64         // capital allotted (virtual for now)
  MaxDrawdown  float64         // pool-level DD limit → halt
  MaxLongExp   float64         // directional exposure caps (fraction of pool equity)
  MaxShortExp  float64
  Status       ACTIVE | HALTED
}
```

**Risk is read at the pool, never the strategy.** Strategy-level risk is already
overfit and conflicting (baseline: uniformly weak PF, wildly different behavior), so
it must rise to the pool.

**Virtual-first, upgradeable.** A pool's capital source is an interface. Today it is
a *notional slice of the one shared exchange account* (accounting construct). The
abstraction is designed so it can later back onto a *real exchange sub-account*
without changing strategy or enforcement code.

> **Honest limit of virtual pools:** they isolate **accounting and risk decisions**,
> not margin. On the shared account a genuine trend blow-up still consumes the
> account's margin and can cross into other pools. True margin isolation arrives only
> with the sub-account upgrade. This is a known, accepted v1 limitation.

## 2. Pool membership (behavioral families, not strategy groups)

Mapping is justified by the **baseline behavior evidence** (behavior families), not
by strategy names:

| Pool | Members | Behavior family |
|---|---|---|
| **Growth** | macross, spottrend | high churn, high DD, low PF — trend |
| **Yield** | grid, dca, dipdca, spotgrid | long-horizon, low churn, stable equity |
| **Cash** | (buffer, no strategies) | absorbs/supplies margin |
| **float** | meanreversion | regime-dependent hybrid |

**MeanReversion is a float.** It *trades* like trend (frequent) but its *return
structure* is mean-reverting like grid. For v1 it defaults to **Growth** but is
tagged migratable; dynamic pool migration is deferred (future work), not built now.

Membership is config-driven: each engine/strategy carries a `pool` tag.

## 3. Pool accounting (how virtual isolation is realized)

Each pool gets a notional capital `C_pool`. Every fill is attributed to its
strategy's pool. Engines already track their own realized/unrealized PnL and
positions, so this is **aggregation above the engines** — engine internals unchanged.

```
pool_equity   = C_pool + Σ(member realized + unrealized PnL)
pool_DD       = (peak(pool_equity) − pool_equity) / peak(pool_equity)
pool_long_exp  = Σ(member LONG  notional) / pool_equity
pool_short_exp = Σ(member SHORT notional) / pool_equity
```

**Directional, never net.** Long and short exposure are tracked and capped
*separately*. Netting is forbidden: grid-long + trend-short can net to "balanced"
while stacking real risk. `pool_long_exp` and `pool_short_exp` each have their own
cap.

This attribution is what isolates behavior: trend's unrealized loss lands only in
Growth's `pool_equity`/DD and never depresses Yield's.

## 4. Pool-level risk (two rules)

Both operate on pool state; neither ever blocks a *closing* order (a position can
always be exited).

- **MaxDrawdown → HALT the pool.** When `pool_DD ≥ MaxDrawdown`, the pool goes
  `HALTED`: its members stop *opening* new positions; other pools are unaffected.
  Purpose: stop a tail regime from spreading. This is genuine risk control.
- **MaxExposure → reject over-cap opens** (per direction). An opening order that
  would push `pool_long_exp` (or `pool_short_exp`) past its cap is denied. Purpose:
  a liquidity / leverage throttle (not "risk" in the tail sense).

`MAX_POSITION_PCT` conceptually lives **here** — as a *pool* exposure cap, not a
per-strategy one (the earlier mistake that killed every single-symbol strategy).

## 5. Execution architecture — the critical decision

**PoolManager decides; ORG enforces. Exactly one gate.**

```
Strategy → PoolManager (decision) → ORG (enforcement) → Execution
```

- **PoolManager (Layer 3, decision).** Groups engines by pool tag, continuously
  computes each pool's equity / DD / directional exposure, decides pool status, and
  **publishes** a read-only snapshot. It **never blocks an order.**

  ```
  POOL_STATUS = {
    Growth: { status: ACTIVE|HALTED, longExp, shortExp, maxLong, maxShort },
    Yield:  { ... },
    Cash:   { status: ACTIVE },
  }
  ```

- **ORG (Layer 1, sole enforcement point).** Its StateProvider gains the order's
  pool + that pool's published status/exposure/caps. A thin **pool-gate** rule (Layer
  1 mechanism, fed by Layer 3 policy) denies an *opening* order when its pool is
  `HALTED`, or when it would breach the pool's directional exposure cap. ORG's
  existing order-safety rules are unchanged. ORG computes deterministic per-order
  arithmetic against *published* state — it holds no pool logic.

**Why not the alternatives (explicitly rejected):**
- ORG owning pool control → ORG stops being a stateless-ish per-order safety gate.
- PoolManager blocking orders directly → **two parallel gates** (ORG + PoolManager) =
  debug hell, unexplainable behavior, fragmented deny logic. We already lived the
  deny-all / layer-misplacement disaster; a dual-gate would repeat it. Single
  enforcement point is non-negotiable.

## 6. How it plugs into the existing system

- New package `internal/pool` — `PoolManager` + `Pool` + `PoolStatus`. Sits above
  the engines (the existing `internal/api` EngineManager already owns the engines);
  PoolManager reads engine equity/positions to aggregate, and publishes `PoolStatus`.
- ORG's `StateProvider` (live) already reads broker/syncer state; it gains a pool
  reference so `OrderState` carries `Pool` + the published `PoolStatus` for that pool.
- One new ORG rule: `PoolGateRule` — reads pool status/exposure from `OrderState`,
  denies opens on HALTED or over-cap (directional). Reason codes `POOL_HALTED`,
  `POOL_EXPOSURE`.
- Rollout mirrors ORG: **Shadow first** (PoolManager publishes, the pool-gate logs
  would-deny but never blocks), observe, then Enforce.

## 7. Error handling & edge cases

- Missing pool tag → strategy defaults to a `default` pool (never unpooled).
- `pool_equity ≤ 0` (pool wiped) → HALTED; no divide-by-zero (guard exposure calc).
- PoolManager stale/unavailable → ORG treats pool as ACTIVE with no exposure cap
  (fail-open for availability; the order-safety rules still apply). Logged.
- Closing orders and cancels always pass, regardless of pool status.

## 8. Testing

- `PoolManager` aggregation + DD/exposure math: pure unit tests (feed member
  equity/positions, assert pool_equity / pool_DD / directional exposure and status).
- Direction-aware exposure: assert long and short cap independently; a long + an
  offsetting short both count against their own caps (no netting).
- `PoolGateRule`: HALTED pool denies opens / allows closes; over-cap directional open
  denied; ACTIVE within cap allowed. (Same TDD style as the existing ORG rules.)
- Integration: a Growth-pool DD breach halts macross/spottrend opens while Yield
  keeps trading.

## 9. Explicitly deferred (structure first)

- Capital **allocation** weights (e.g. Growth 40 / Yield 30 / DCA 20 / Cash 10) and
  regime-based pool adjustment (trend regime → Growth↑). Static placeholder weights
  for now.
- MeanReversion dynamic pool **migration**.
- Real **sub-account** backing (the upgrade the interface is designed for).
- Cross-pool cash transfer from the Cash buffer.

## 10. Success criteria

A Growth-pool drawdown halts only Growth's opening orders (Yield unaffected);
pool equity/DD/directional-exposure are correctly attributed and published; ORG is
the only place an order is ever denied; zero behavior change while in Shadow.
