# Grid "无量阴跌" Gap — Quantify-First Gauge Design

**Date**: 2026-07-31
**Status**: Design approved, pending implementation
**Positioning**: This is a **survival/risk-reduction** investigation, not an alpha search. Even if Phase 1 succeeds, the outcome is a smaller residual drawdown on the existing Grid+TED strategy, not a new return engine. See [[survival_vs_alpha_2026-07-07]] — TED/Pool/ORG are all survival-layer, and the Alpha layer remains the one open gap; this work does not attempt to close that gap.

## Background

[[volume_gate_grid_2026-07-06]] froze the Grid + Terminal Event Detector (TED) research on 2026-07-06 (`206a3e1`, doc `docs/research/grid-ted-v1.md`). TED is a volume-only signal (`score = 0.5·vol_hi + 0.5·vol_up`) driving a 3-mechanism state machine (hysteresis/cooldown/persistence, `internal/strategy/grid/volgate.go`). Validated result: on 15m/5m it cuts real-cost drawdown 17–130× (e.g. BTC 15m 17.1%→0.13%); on 1h+ it is dead weight because volume aggregation smooths out the spikes the score depends on.

The frozen memo notes one known blind spot in passing, never quantified: **"挡不住无量阴跌"** — a steady decline with no volume spike never crosses the exit threshold, so the gate never fires. The memo's own suggested next step, if this line is reopened, is tagged `Trend/Inventory/Market-State`.

Separately, [[survival_vs_alpha_2026-07-07]] (`794150a`, `scripts/grid_trend_switch.py`) already tested and killed a *different* idea in this neighborhood: switching capital between grid and an active SMA trend-following leg (by TED-regime or by Kaufman Efficiency Ratio regime), and running both legs in parallel. Both are closed — switching loses to the better single leg every regime (BTC 15m switch −18.8% vs grid-only +12.6% in chop); parallel only reduces variance (13/15 cells), average return ≤0 since both legs individually have E≤0.

**This design is scoped narrower than what was killed**: a trend/structure signal used purely as a second *pause-to-cash* gate input alongside TED's volume signal — the same defensive action `volgate.go` already takes (flatten inventory, re-center later) — never as a signal to deploy capital into a standalone trend-following position. No capital ever gets allocated to "trend" as an asset class here; the only decision being augmented is "should Grid be off right now."

## Goal

Determine whether the no-volume-grinding-decline blind spot is frequent/large enough in the already-validated dataset (BTC/ETH, 15m/5m) to be worth building a second gate signal for. **Quantify before designing a detector** — per [[feedback_verify_before_estimate]], don't build a solution to an unmeasured problem.

## Phase 0 — Quantify the gap (build this now)

New script: `scripts/grid_no_vol_decline_gauge.py`. Composes existing, already-validated pieces rather than reimplementing them:

- **Data fetch**: reuse `grid_trend_switch.py`'s `fetch_range` (Binance fapi, same source `breakout_score.py` already uses) — not `breakout_score.py`'s own `fetch_klines`, which caps at 1500 bars and is far too short for a 90-day 5m window (~25,920 bars); `fetch_range` paginates. Symbols/intervals: BTC + ETH, 15m and 5m — the exact set TED was validated on, so results are directly comparable to the frozen 17–130× numbers.
- **TED score**: reuse `breakout_score.py::compute_signals` (`vol_hi`/`vol_up` composite) — do not re-derive.
- **Gate on/off timeline**: reuse `grid_gate_backtest.py::run_grid`'s embedded 3-mechanism state machine (hysteresis/cooldown/persistence) to get the actual gate state per bar. This is the same logic `volgate.go` runs in Go; reusing the Python port that already exists avoids a second hand-transcription that could silently drift from production behavior.
- **New logic (the only new code in Phase 0)**: scan the "on" (gate-active) stretches. Within each stretch, find windows where cumulative price decline exceeds a threshold defined the same way [[volume_gate_grid_2026-07-06]]'s own de-biasing lesson requires: an **absolute** percentile-rank threshold on forward move size (reusing `label_indices`'s `abs_pct` convention — rank within the sample, not an ATR-relative threshold), not a hand-picked flat percentage. Default: top ~15% of forward absolute-decline magnitudes in the sample, tunable. Each qualifying window where the gate never fired throughout = one "no-volume grinding decline" episode.
- **Output**: per symbol/interval — episode count, magnitude distribution (median/mean/max peak-to-trough decline while gated "on"), and estimated contribution to the *residual* drawdown already reported for the gated strategy (0.13% BTC 15m / 0.57% ETH 15m / 0.51% BTC 5m per the frozen memo) — i.e., of what little drawdown survives TED today, how much of it is this specific failure mode vs. something else (gap risk, warmup, etc.)?

## Decision gate

- **If episodes are rare or their contribution to residual drawdown is small** (residual DD is already tiny — 0.13%–0.57% — so there may not be much room left to extract): stop here, write an honest negative-result memory, do not build Phase 1. This mirrors the project's existing culture of closing lines that don't pay for the engineering.
- **If episodes are frequent or materially explain the residual drawdown**: proceed to Phase 1 — gauge 2–3 candidate trend/structure signals (Kaufman ER, already implemented in `grid_trend_switch.py::efficiency_ratio`, reusable as-is; rolling regression slope/R² normalized by return volatility; ADX as a standard baseline) against the labeled episodes from Phase 0, OOS-split by symbol/window, same rigor as the original `vol_hi`/`vol_up` vs. `compression` elimination. Only signals that clear OOS separation get considered for a Go port. Phase 1's exact parameters (OOS split, which 2–3 candidates, separation metric) will be finalized after Phase 0's results are in — premature to lock those down against a problem whose size is still unknown.

## Testing

This is a throwaway research script in `scripts/`, consistent with `breakout_score.py` / `grid_gate_backtest.py` / `grid_trend_switch.py` — none of which carry a test suite in this repo. Phase 0's only new logic (episode scanning over the on/off timeline) gets a lightweight sanity check: feed a synthetic close/volume series with a hand-placed no-volume decline and a hand-placed volume-spike decline, confirm the scanner flags the first and not the second. No Go changes in this phase, so no TDD cycle applies yet — that only becomes relevant if Phase 1 produces a signal worth porting to `internal/strategy/grid/`.

## Non-goals

- Not building a trend-following leg or any capital allocation into trend direction (already killed, see Background).
- Not re-optimizing TED's existing volume score (frozen, [[volume_gate_grid_2026-07-06]] — "勿再优化").
- Not testing 1h+ timeframes (already proven dead for this whole gate mechanism family — volume aggregation removes the signal at that granularity, and this Phase 0 gap-quantification is downstream of the same gate).
