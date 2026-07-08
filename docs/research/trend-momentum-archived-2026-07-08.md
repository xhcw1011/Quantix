# Trend / Momentum — ARCHIVED (no deployable edge) · 2026-07-08

**Status: CLOSED.** After full funding-grade rigor, neither trend-following nor
momentum is a deployable return engine. The cross-sectional **funding factor** stands
alone. This archives the trend/momentum line so effort concentrates on the funding
rebalancer.

## What was tested (same battery the funding factor passed)

The bar (funding cleared all of these): **positive every regime · Sharpe ~1.5 · cost
survives to 30bp · no year-by-year decay · param/universe robust.**

| Thesis | Tool | Verdict |
|---|---|---|
| **Grid (mean-reversion)** | `internal/strategy/grid`, faithful backtest | No raw edge (PF ~0.97 zero-cost, loses at real cost). The volume gate (TED) is a **drawdown shield / survival**, not alpha. |
| **TSMOM / trend-following** | `reports/xstsmom.py` + `reports/xstsmom_rigor.py` | **DEAD.** Sharpe 0.28 (lo) / 0.50 (ls), maxDD 53%. Positive **only in 2023** (+35%); loses in bull (−8), 25-mid (−1.6), 26-down (−9.5). Monotone decay: 2023 (Sh1.06) → 2024 +3% → **2025 −22.9% → 2026 −12.7%**. Negative ~18 months. |
| **Cross-sectional momentum** | `reports/mn_momentum.py` + `reports/mn_momentum_rigor.py` | **Decaying.** Sharpe 0.64; 牛+30 but 25-mid −0.4 / 26-down −0.0; 2024 peak (Sh1.37) → 2025 −8.4% → weak 2026. |

Commits: `66948a5` (TSMOM rigor), `a1521dc` (XS-momentum rigor), plus the momentum
research series (`01eec4b` "time-series momentum also fails; edge has decayed since
2023", `d28cd0b`, `11fb3d9`, `341e5bc`).

## Why it fails where funding passes

- **Trend/momentum edge was a 2023 phenomenon** (crypto's post-bear recovery trend) and
  has fully decayed — flat-to-negative since early 2025.
- **Grid has no expectancy** — the gate only reduces variance (survival, not alpha).
- **Combining them is worse, not better**: E(grid)≤0 ∧ E(trend)≤0 ⇒ E(combo)≤0.
  Variance reduction ≠ expectancy creation. The grid⇄trend switch was already killed;
  "funding+momentum" was refuted (momentum is dead weight, not a diversifier).

## Conclusion

Of grid (no edge) / TSMOM (dead) / XS-momentum (decaying), **only the cross-sectional
funding factor passes full rigor** (positive every regime, Sharpe 1.5, no decay, cost
robust to 30bp, executable). It is the sole return engine. See
`docs/research/funding-factor-validated-2026-07-07.md` and the `internal/xsfunding` +
`internal/rebalancer` implementation. Trend/momentum: closed.
