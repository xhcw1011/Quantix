# Baseline Behavior Snapshot — 2026-07-06

**Step 1.5 · 观察期产物 · 不是优化,是记录系统自然状态**

Purpose: capture each strategy's *natural* trading behavior with **no portfolio
intervention**, before designing any Layer-3 allocator. Observation only — no
allocations are proposed here (Portfolio Engine is deliberately deferred).

Method: `cmd/backtest -out-json` → `scripts/baseline_report.py` (pure post-process,
no engine change). ETHUSDT 15m, 2026-01-21→05-09 (108d), $10k, one param set each.

## Snapshot

| strat | ret% | PF | 胜率 | 交易 | 笔/天 | 持仓中位h | 均敞口 | 在场% | 在回撤% | DD中位 | DDp90 |
|---|---|---|---|---|---|---|---|---|---|---|---|
| spottrend | −38.1 | 0.71 | 27% | 216 | 2.0 | 4.2 | 49% | 49% | **100%** | 29.7% | **36.3%** |
| macross | −30.2 | 0.73 | 29% | 165 | 1.5 | 5.5 | 43% | 43% | **100%** | 24.4% | 30.3% |
| meanreversion | −17.3 | 0.72 | 56% | 77 | 0.7 | 4.0 | 13% | 13% | 96% | 17.0% | 19.2% |
| grid | −8.3 | 0.22 | 59% | 64 | 0.6 | 35.8 | 47% | 98% | 92% | 13.9% | 17.2% |
| spotgrid | +0.1 | 1.14 | 35% | 26 | 0.2 | 43.2 | 2% | 91% | **4%** | 0.6% | 0.6% |
| rebalance | −10.4 | 0.00 | 0% | 2 | 0.0 | 2591 | 49% | 100% | 99% | 14.8% | 19.0% |
| dca | +6.0 | — | — | 1 | 0.0 | 2591 | 54% | 100% | 91% | 4.7% | 7.8% |
| dipdca | +6.3 | — | — | 1 | 0.0 | 2591 | 56% | 100% | 91% | 5.0% | 8.2% |

(dca/dipdca/rebalance = accumulate-and-hold: "1–2 trades" is the single position
closed at the final bar, so PF/胜率 are degenerate.)

## Observed personas (behavior, not judgement)

- **Trend family (spottrend, macross)** — churniest (1.5–2 trades/day), short holds
  (4–5h), **underwater 100% of the time**, fat drawdown tail (p90 30–36%), losing.
  In this ranging/down ETH regime they never get out of drawdown.
- **meanreversion** — 56% win rate yet −17%; low, disciplined exposure (~13%); 96%
  of time in some drawdown.
- **grid** — many small wins / few large losses (PF 0.22), near-always in market
  (98%), long holds (36h median), moderate DD (p90 17%).
- **spotgrid** — the calm outlier: PF 1.14, tiny exposure (2%), only **4% of time in
  drawdown**. Fixed small $/order keeps it safe.
- **dca / dipdca** — accumulate-and-hold; ~55% exposure, thin DD (p90 ~8%), slightly
  positive; a single position carried to the end.

## The one observation that matters

The strategies split into two behavioral families:
1. **Churny + chronically underwater, fat DD tails** — trend & mean-reversion.
2. **Calm, near-flat, thin DD tails** — spot-grid & accumulation.
Exposure ranges 2%→56%. This is the raw material a future exposure allocator would
work with — but that design is **deferred until live-shadow behavior is confirmed**.

## Caveats & next

- One symbol (ETH), **one regime** (Jan–May 2026, down/range), one param set each.
  A single snapshot, not the full behavior space — trend strategies would look very
  different in a trending regime.
- Complementary track: the same behavior on **live ORG shadow (3–7 days)** once
  deployment is authorized. Only after natural-state behavior is confirmed live +
  offline should the Layer-3 Portfolio (exposure allocator) be designed.
