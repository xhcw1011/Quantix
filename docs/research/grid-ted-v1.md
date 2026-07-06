# Research: Grid Terminal Event Detector (TED) — V1.0

**Status: VALIDATED · FROZEN (do not reopen for optimization)**
Date: 2026-07-06 · Owner: xhcw1011

Formerly "Grid Volume Gate". Renamed to **Terminal Event Detector (TED)** because
the mechanism does not predict *direction* — it predicts whether the current bar
is a **terminal event** (a move a mean-reversion grid will *not* recover from) vs
ordinary, recoverable noise.

> **Event → Grid exits. Not an event → Grid continues.**

---

## 1. Original question (the hypothesis)

> Is there a mechanism that stops a Grid from dying in extreme conditions?

**Answer: Yes.** A causal volume-percentile detector, run as a hysteresis state
machine, flattens the grid before terminal moves while leaving recoverable
drawdowns alone. On fine timeframes it converts the grid's failure mode
(load up into a trend, blow up) into "sit out the event, re-center after".

## 2. The detector

Per bar, causal, no look-ahead:

```
score = 0.5 * vol_hi + 0.5 * vol_up            (both percentiles in [0,1])
  vol_hi = pctile(volume, last Window bars)               "volume is high"
  vol_up = pctile(volume / mean(prev RatioBars), window)  "volume is rising"
```

3-mechanism state machine (kills flip-flop churn):
- **Hysteresis** — exit when `score ≥ 0.70`, only re-enter once `score < 0.40`.
- **Cooldown** — after an exit, wait ≥ N bars before re-entry is allowed.
- **Persistence** — require N consecutive low bars to actually re-enter.

On exit: flatten inventory + re-center the grid at the current price.

## 3. Why volume, and why this is a Terminal Event Detector

The signal was *derived*, not assumed. Original breakout research locked
`duration + voldiv` (compression) with +20pp OOS lift. An **absolute-threshold
de-bias** showed that edge was mostly a measurement artifact (compression → low
ATR → low relative threshold): under an absolute-move label the composite flipped
negative (BTC −8.2pp, ETH −3.3pp). Decomposing the black box isolated the real,
absolute-robust carrier — **volume** (vol_hi +38/+37pp, vol_up +14/+17pp), while
compression was *inverse*. Re-locked to volume: 6/6 OOS (BTC/ETH/SOL/BNB 1h +
BTC/ETH 4h); a skip-bars test confirmed persistence (not coincidence).

Volume is a proxy for "a real event is happening". That is the whole product:
distinguishing a **terminal** drawdown from a **recoverable** one — which is
exactly what a grid needs and what a price threshold cannot do (see §5).

## 4. Validation — four independent layers

| Layer | Result |
|---|---|
| **Python OOS de-bias** | Volume survives absolute-threshold label 6/6 coins/TFs; compression was an artifact. |
| **Go unit tests** | `internal/strategy/grid/volgate.go` — pctile, hysteresis, cooldown, persistence, spike-flatten all covered. |
| **Real backtester, cross-coin** | `cmd/backtest`, real fills/fee/slippage, DB data — see below. |
| **Control experiment (price stop)** | 2×2 refutes the complementary-stop hypothesis — see §5. |

Real backtest (15m/5m, 108d, grid 20 levels × 0.5%, 5bp fee):

| Case | gate off | gate on |
|---|---|---|
| ETH 15m | −8.3% / **DD 21.1%** | +0.7% / **DD 0.57%** |
| BTC 15m | −1.7% / **DD 17.1%** | +0.1% / **DD 0.13%** |
| BTC 5m | −1.7% / DD 16.8% | −0.4% / DD 0.51% |

Drawdown cut 17–130×, confirmed on both symbols.

## 5. Scope, limits, and what was REFUTED

**Timeframe boundary (hard limit).** TED works on **≤ 15m only**. Same BTC, same
Feb–Mar window: 15m DD 9.2%→0.5%, but **1h DD 6.6%→6.6% (identical)**. On coarse
bars, volume aggregation smooths away the spikes the detector keys on, so it never
fires. → **Deploy on 15m or finer.**

**It is risk reduction, not alpha.** It always costs participation/return; in calm
markets it nearly turns the grid off. It measures magnitude, not direction.

**REFUTED: catastrophic price stop.** Hypothesis was a price stop would cover the
low-volume grinds TED misses. 2×2 (none/gate/stop/both) disproved it:
- ETH 15m: stop −10.3%/DD14 (worse than none −8.3%); `both` = `gate` (TED fires on
  the volume spike before price reaches the stop).
- BTC 1h: stop is unstable / path-dependent — 5%:+4.6, 8%:−5.6, 10%+:never fires.
  Non-monotonic ⇒ overfit noise, not protection.
- **Structural reason:** a grid is mean-reversion; a price stop converts
  *recoverable* unrealized drawdown into *locked* realized loss, and cannot tell
  recoverable from terminal (it fires on any dip). TED can — a big volume move ≈ a
  terminal event. This is the core of the whole line: **the distinction that
  matters is recoverable-vs-terminal, not up-vs-down.**

Kept as opt-in (`StopLossPct`, default 0 = off) for reproducibility; **not recommended**.

## 6. Verdict & disposition

- **TED V1.0 = the grid's independent risk module.** Enable on grids ≤ 15m; leave
  price stops off.
- **Frozen.** Do not keep optimizing VolGate — marginal return has fallen off.
- **Code:** `internal/strategy/grid/volgate.go` + `grid.go`. Params: `VolGateWindow`
  (>0 enables), `VolGateExit/Enter/Cooldown/Persistence`. Research tools:
  `scripts/breakout_score.py`, `scripts/grid_gate_backtest.py`.
- **Commits:** `206a3e1` (port), `c64b24b` (backfill), `3d195bf` (stop + verdict).
- Code retains the name `VolGate`; "TED" is the official conceptual name. A rename
  is available on request but is deliberately not done under the freeze.

## 7. Next research lines (pick one; do NOT reopen this one)

Trend · Inventory management · Market-state classification.
