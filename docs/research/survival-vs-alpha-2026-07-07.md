# Architecture: Survival Layer vs Alpha Layer — 2026-07-07

## The realization

Everything built and validated so far is a **survival layer** (risk / variance
reduction), **not a return engine** (alpha). Positive expectancy can only come from
an **Alpha Layer we do not yet have**. This reframes the whole stack.

```
                     Alpha Layer   ← 收益来源 (the only place return can come from)
                          |
             +------------+-------------+
             |                          |
        Trend Engine               Yield Engine
        (待验证 TBV)                (待验证 TBV)
        —————————————————————————————————————————
                    Capital Layer     ← survival (built + validated AS survival)
                          |
                     PoolManager
                          |
                        ORG
                          |
                     Execution
```

## Every layer built so far is survival, not alpha

| Layer | Real role | Validated as | Is NOT |
|---|---|---|---|
| **VolGate / TED** | grid event-exit | drawdown shield (17–130× on fine TF) | a money machine |
| **Pool / Capital** | portfolio risk isolation | DD reduction (13/15 cells, ~half) | a money machine |
| **ORG** | order-safety gate | execution boundary (31-ETH class) | a money machine |

All three are **survival layer**: they reduce variance / cap disaster / bound
execution. None creates edge.

## Two verdicts from the grid⇄trend research (recorded precisely, not "parallel failed")

### 1. Switch (TED regime-switch) — DEAD, closed

Judged dead, do not invest further. Cause is structural, not tunable:
**regime-detection latency + switching cost + wrong switches + whipsaw** → the
switch never captures the best leg. It was worse than the better single leg in
*every* regime and catastrophic in ranges (BTC 15m: switch −18.8% where grid alone
made +12.6%). Both natural switch signals (TED = volatility, ER = trend/range) lost.

### 2. Grid + Trend parallel — structurally valid, but NOT alpha

Evidence (15 cells = BTC/ETH/SOL × 5 regime windows, real fees):
- **Drawdown falls in 13/15 cells** (often ~half either leg) → the two legs have
  **low return correlation** → this is the genuine value of a Portfolio Layer.
- **But return averages ≤ 0** (≈ −4.3% / window; only BTC positive; ETH/SOL badly
  negative, e.g. ETH 2025-01 −36.8%). ETH 2025-05: asset **+40%** yet grid −14% +
  trend −17% → parallel −15% (legs genuinely lack edge).

**The math that settles it:** if `E(grid) ≤ 0` and `E(trend) ≤ 0` then
`E(grid+trend) ≤ 0` — combining two weak edges only **lowers variance**, it does not
create expectancy. (The earlier "parallel +19.6%" was a single-coin/BTC artifact;
multi-coin refuted it — the recurring single-sample trap.)

## The gap: Return Engine (Alpha Layer)

Nothing built has positive expectancy. The full negative ledger: aistrat faithful
−89.6% / PF 0.50, x-sectional momentum survivorship-killed, funding carry compressed,
macross thin/regime-dependent, breakout prediction dead under de-bias, grid / trend /
switch / parallel all ≤ 0 across coins+windows.

**The sole unsolved problem is the Alpha Layer.** Everything below it (the survival
stack) is done and correct *as survival*. Return must come from a validated
Trend Engine and/or Yield Engine — neither exists yet (both `待验证`).

## Disposition

- **Survival stack** (ORG / Pool / TED): correct, keep — deploy/observe as risk
  infrastructure. Do NOT sell any of them as a return product.
- **Switch**: dead, closed.
- **Parallel / pooling**: the Capital Layer's structure (survival); not a return
  product on its own.
- **Next**: the Alpha Layer — does any return engine survive multi-coin / multi-window
  positive expectancy after fees? That is the real, still-open question, and our
  track record on finding alpha is a string of honest negatives. Search it clear-eyed.

Tools: `scripts/grid_trend_switch.py` (grid/trend/switch/parallel, multi-regime).
