# Cross-Sectional Funding Factor — VALIDATED (first Return Engine) · 2026-07-07

The first edge all session to pass the full rigor battery. A market-neutral,
cross-sectional funding factor. This is the **Alpha Layer** the survival stack
(ORG / Pool) was missing.

## The factor

Over a broad mid-cap perp universe, each rebalance rank coins by trailing funding:
**long the K lowest-funding, short the K highest-funding** (equal weight,
dollar-neutral, hold REB days, collect/pay funding). Long-short → market-neutral.

Base config used: ~50-coin universe, L14 (unused — funding-only wins), W14 funding
window, K=5 each side, rebalance every 3 days, PIT universe (liquid + listed).
Tool: `scripts/xsmom_funding.py`.

## Validation results (2024-09 → 2026-07, ~22 months, PIT, funding P&L included)

**Base (50 coins, 10bp):** funding **+71.8% cum / 35.3% ann / Sharpe 1.51**;
per regime **牛 +21 / 25中 +14 / 26跌 +23** — positive in every regime.
(momentum alone Sharpe 0.36 ≈ dead; combined worse than funding — momentum is a drag,
so "funding+momentum hybrid" is refuted; the edge is funding alone.)

| Test | Result | Verdict |
|---|---|---|
| **Cross-regime OOS** | bull +21 / mid +14 / down +23 (all +) | ✅ survives the bull |
| **Cost stress** | 10bp 35% · 20bp 28% · 30bp 22% · 50bp 9% | ✅ to ~30bp |
| **Param robustness** | W7–30 × K3–8: ~11/12 positive; REB1–7 all + | ✅ broad, not a sweet spot |
| **Universe robustness** | top15 6% · top30 18% · all50 28% · drop-top10 26% | ✅ but needs breadth |
| **Decay** | +1→+5→+11→+23 across blocks | ✅ not crowding-compressed |
| **Executable** | short-book ≈ $827M/day | ✅ (mid-caps, perp shorts — no borrow) |

## Decomposition (carry vs mean-reversion, pre-cost cumulative)

| component | 全期 | 牛 | 25中 | 26跌 |
|---|---|---|---|---|
| **carry** (funding collected/paid) | +24% | +2 | +4 | +16 |
| **mean-reversion** (anti-crowding price) | +44% | +19 | +14 | +9 |

- **Mean-reversion is the bigger driver (~2/3)** — crowded (high-funding) coins
  underperform. This is the larger but more **crowdable / decayable** part.
- **Carry (~1/3, ~13%/yr pre-cost)** is the **durable structural floor** — a real
  cash flow, hard to arbitrage away; fattest in down markets (high funding).
- So durability = a robust carry floor + a larger-but-fragile mean-reversion upside.

## Where the edge lives (matters for the build)

The edge is in the **cross-sectional dispersion of mid-caps**, not the megacaps
(top-15 alone only 6%). It needs a **broad mid-cap universe (30–50+ coins)**. Cost:
mid-caps are less liquid → higher execution cost + tail risk. This is the real
operational risk, not the signal.

## Honest caveats

1. The larger component (mean-reversion) is more crowdable/decayable than carry.
2. Edge requires mid-cap breadth → execution/liquidity/tail risk in smaller coins.
3. One exchange (Binance), one factor construction; other-exchange robustness untested.
4. High turnover (rebalance ~3 days) → needs automation (infra exists).

## Disposition

**Validated. Move to build** (Go strategy → paper-forward → shadow → gradual live).
It is the Alpha Layer, market-neutral, sits on the survival stack (ORG/Pool). Treat
the carry floor as the durable base, mean-reversion as decayable upside; watch
execution cost and factor decay. Design: `docs/superpowers/specs/2026-07-07-xs-funding-strategy-design.md`.
