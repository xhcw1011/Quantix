# Grid No-Volume-Decline Gauge (Phase 0) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and run a gauge script that quantifies how often `internal/strategy/grid`'s TED volume gate stays "on" through a large, no-volume-spike price decline — the blind spot named but never measured in [[volume_gate_grid_2026-07-06]] — and use the result to decide whether Phase 1 (a trend/structure detector) is worth building.

**Architecture:** A new Python research script, `scripts/grid_no_vol_decline_gauge.py`, composes three existing pieces rather than reimplementing them: `breakout_score.py` (TED score, klines), `grid_gate_backtest.py` (the 3-mechanism gate state machine, refactored into a standalone reusable function), and `grid_trend_switch.py` (paginated multi-day kline fetch). The only new logic is an episode scanner: find on-stretches whose peak-to-trough decline ranks in the top N% of all on-stretch declines in the sample.

**Tech Stack:** Python 3 stdlib only — no `requests`, `numpy`, or `pytest` are installed in this environment (verified during design). All existing sibling scripts in `scripts/` are stdlib-only for the same reason; this plan does not introduce a new dependency.

## Global Constraints

- Stdlib-only Python (`urllib.request`, `argparse`, `math`, `datetime`) — no numpy/requests/pytest available or to be added.
- Reuse, don't reimplement: TED score comes from `breakout_score.compute_signals`; the gate state machine comes from `grid_gate_backtest.gate_timeline` (new, extracted in Task 1); paginated fetch comes from `grid_trend_switch.fetch_range`/`to_ms`.
- Episode thresholds are percentile-rank on absolute price-decline magnitude, never ATR-relative — this is the specific de-biasing lesson from [[volume_gate_grid_2026-07-06]] (the old "compression" signal looked good under ATR-relative labels and died under absolute ones).
- Survival/risk-reduction framing only (see spec's Non-goals) — this script never allocates capital to a trend-following position; it only measures how the existing grid+TED strategy's own drawdown behaves.
- No pytest convention exists for `scripts/` in this repo (confirmed: `breakout_score.py`, `grid_gate_backtest.py`, `grid_trend_switch.py` all ship without test files). Verification here is direct function calls with synthetic data (Task 2) plus a real end-to-end run (Task 3), matching the existing precedent — not a new pytest suite.
- Spec: `docs/superpowers/specs/2026-07-31-grid-no-vol-decline-gauge-design.md`.

---

### Task 1: Extract `gate_timeline()` from `grid_gate_backtest.py`

**Files:**
- Modify: `scripts/grid_gate_backtest.py:28-86`

**Interfaces:**
- Produces: `gate_timeline(score: list[float|None], exit_thresh=0.5, enter_thresh=0.5, cooldown=0, persistence=1) -> list[bool]` — per-bar gate on/off timeline, causal (lag-1), same 3-mechanism state machine `run_grid` already embeds. `True` = grid active that bar.
- `run_grid`'s existing signature and return shape (`{pnl, ret, maxdd, trades, fees, on_frac}`) are unchanged — this is a pure refactor.

This is a behavior-preserving extraction: pull the gate state machine (currently inlined in `run_grid`'s loop) into its own function so Task 2's gauge script can get the on/off timeline without hand-copying the state machine a second time (which risks drifting from `internal/strategy/grid/volgate.go`'s real Go behavior).

- [ ] **Step 1: Capture baseline behavior before touching anything**

Run this from the repo root:

```bash
cd scripts && python3 -c "
from grid_gate_backtest import run_grid
import random
random.seed(42)
closes = [100.0]
for _ in range(199):
    closes.append(closes[-1] * (1 + random.uniform(-0.01, 0.01)))
score = [random.random() for _ in range(200)]
r = run_grid(closes, score, 0.70, 0.40, 3, 3, spacing=0.01, fee=0.0005, max_inv=10)
print(r)
"
```

Expected output (exact — this is the golden master for Step 4):
```
{'pnl': 0.36056682478384977, 'ret': 0.00036056682478384977, 'maxdd': 0.0024182428401539756, 'trades': 8, 'fees': 0.3805362229941192, 'on_frac': 0.045}
```

- [ ] **Step 2: Replace lines 28-86 of `scripts/grid_gate_backtest.py`**

Replace the current `run_grid` function (from `def run_grid(closes, gate=None, ...` through the line just before `else:` / `on_bars += 1` at line 87) with:

```python
def gate_timeline(score, exit_thresh=0.5, enter_thresh=0.5, cooldown=0, persistence=1):
    """逐 bar 的开关闸状态(三机制,因果、滞后1根,无前视),从 TED 分数序列算出:
      ① 迟滞 Hysteresis:量分 ≥ exit_thresh 才退出;<enter_thresh 才考虑回来(exit>enter=死区)。
      ② 冷却 Cooldown:退出后至少等 cooldown 根才允许回来。
      ③ 持续 Persistence:要连续 persistence 根 Low(<enter_thresh)才真回来。
    单阈退化:exit=enter, cooldown=0, persistence=1。
    返回 list[bool],长度=len(score),True=该 bar 网格开。独立于 run_grid,
    是 grid_no_vol_decline_gauge.py 复用的那部分,避免重新照抄一份、和 Go 版(volgate.go)
    的行为漂移。"""
    n = len(score)
    state_on = True     # 闸门状态:True=开网,False=收网中
    since_exit = 0      # 退出后过了几根
    low_streak = 0      # 连续 Low 计数
    on = [True] * n
    for i in range(n):
        s = score[i - 1] if i - 1 >= 0 else None
        if state_on:
            if s is None or s >= exit_thresh:      # ① 迟滞:高于退出阈 → 收网
                state_on = False
                since_exit = 0
                low_streak = 0
        else:
            since_exit += 1
            low_streak = low_streak + 1 if (s is not None and s < enter_thresh) else 0
            # ②冷却够 + ③连续 Low 够 + 迟滞(低于回入阈,已含在 low_streak 里)→ 回来
            if since_exit >= cooldown and low_streak >= persistence:
                state_on = True
        on[i] = state_on
    return on


def run_grid(closes, gate=None, exit_thresh=0.5, enter_thresh=0.5,
             cooldown=0, persistence=1, spacing=0.01, fee=0.0005, max_inv=10):
    """逐 close 跑网格。gate=None 即裸网格(永远开)。开关闸状态用 gate_timeline()。
    返回 dict: pnl, ret(相对 max_inv*p0 资本), maxdd, trades, on_frac。"""
    n = len(closes)
    p0 = closes[0]
    step = math.log(1 + spacing)

    def level_of(p):
        return math.floor(math.log(p / p0) / step)

    pos = 0.0        # 库存(+多 -空),单位:档(qty=1/档)
    cash = 0.0       # 纯成交现金
    fees = 0.0       # 累计手续费
    trades = 0
    last_level = level_of(p0)
    capital = max_inv * p0
    peak = 0.0
    maxdd = 0.0
    on_bars = 0
    on = gate_timeline(gate, exit_thresh, enter_thresh, cooldown, persistence) if gate is not None else [True] * n

    def fill(price, dqty):
        nonlocal pos, cash, fees, trades
        cash -= dqty * price          # 买(dqty>0)花钱,卖(dqty<0)收钱
        fees += abs(dqty) * price * fee
        pos += dqty
        trades += 1

    for i in range(n):
        p = closes[i]
        active = on[i]

        if not active:
```

(The `if not active:` block and everything after it — inventory flatten, grid fill loop, equity/maxdd bookkeeping, the final `pnl`/return — is untouched; only the lines above it change.)

- [ ] **Step 3: Re-run the same baseline command from Step 1**

```bash
cd scripts && python3 -c "
from grid_gate_backtest import run_grid
import random
random.seed(42)
closes = [100.0]
for _ in range(199):
    closes.append(closes[-1] * (1 + random.uniform(-0.01, 0.01)))
score = [random.random() for _ in range(200)]
r = run_grid(closes, score, 0.70, 0.40, 3, 3, spacing=0.01, fee=0.0005, max_inv=10)
expected = {'pnl': 0.36056682478384977, 'ret': 0.00036056682478384977, 'maxdd': 0.0024182428401539756, 'trades': 8, 'fees': 0.3805362229941192, 'on_frac': 0.045}
assert r == expected, f'MISMATCH: {r} vs {expected}'
print('REGRESSION CHECK PASSED: refactor preserves exact behavior')
"
```

Expected output: `REGRESSION CHECK PASSED: refactor preserves exact behavior`

If this doesn't match, the refactor changed behavior — stop and diff against Step 2's exact replacement before proceeding.

- [ ] **Step 4: Confirm the existing CLI still runs (network required)**

```bash
cd scripts && python3 grid_gate_backtest.py --symbol BTCUSDT --interval 1h
```

Expected: the usual 三机制 vs 裸网格 vs 单阈闸门 comparison table prints with no traceback (exact numbers will differ run to run since it's live market data — the point of this step is "no crash, output shape unchanged", not matching fixed numbers).

- [ ] **Step 5: Commit**

```bash
git add scripts/grid_gate_backtest.py
git commit -m "refactor(research): extract gate_timeline() from grid_gate_backtest.run_grid

Behavior-preserving — pure extraction, verified against a fixed-seed
synthetic baseline before/after. Lets grid_no_vol_decline_gauge.py (next
commit) get the gate's on/off timeline without a second hand-copy of the
3-mechanism state machine."
```

---

### Task 2: Create `scripts/grid_no_vol_decline_gauge.py`

**Files:**
- Create: `scripts/grid_no_vol_decline_gauge.py`

**Interfaces:**
- Consumes: `gate_timeline(score, exit_thresh, enter_thresh, cooldown, persistence) -> list[bool]` and `run_grid(closes, gate, ...) -> dict` from Task 1's `grid_gate_backtest.py`; `compute_signals(kl, oi_pairs, fund_pairs, W, bars_per_day) -> dict` and `interval_hours(iv) -> float` from `breakout_score.py`; `fetch_range(sym, interval, start_ms, end_ms) -> list[dict]` and `to_ms(d) -> int` from `grid_trend_switch.py`.
- Produces: `find_no_volume_declines(closes, on_timeline, abs_pct=15.0, min_run_len=5) -> list[dict]` (each dict: `start`, `end`, `decline_pct`, `bars`) — this is what Task 3 reads to decide whether to proceed to Phase 1.

- [ ] **Step 1: Write the scanner + a standalone sanity check (no network needed)**

Create `scripts/grid_no_vol_decline_gauge.py` with this content:

```python
#!/usr/bin/env python3
"""grid_no_vol_decline_gauge — Phase 0 of the "Trend" gate research line.

TED (internal/strategy/grid/volgate.go, scripts/breakout_score.py's vol_hi+vol_up)
only sees volume. volume_gate_grid_2026-07-06 noted in passing, never measured,
that it "挡不住无量阴跌" — a steady decline with no volume spike never crosses the
exit threshold, so the gate stays on and rides the decline. This script quantifies
that blind spot before anyone builds a second (trend/structure) gate signal for it:
how often does the gate stay "on" through a large decline, and how large are those
declines relative to the strategy's already-tiny residual drawdown.

Note on units: episode magnitude is measured in raw price % (peak-to-trough close
decline while the gate stayed on). The strategy's reported maxdd is an EQUITY %
(scaled by how much of the ±max-inv grid inventory was actually filled at that
point). The two are not the same unit — an episode's price-decline % is an upper
bound on its equity impact (equality only if inventory happened to be fully built
before the decline started), not a literal "this episode caused N% of the drawdown"
figure. Treat the comparison as orientation, not a precise attribution.

See docs/superpowers/specs/2026-07-31-grid-no-vol-decline-gauge-design.md.

Usage:
  python3 scripts/grid_no_vol_decline_gauge.py --symbol BTCUSDT --interval 15m
  python3 scripts/grid_no_vol_decline_gauge.py --all   # BTC+ETH x 15m+5m, the TED-validated set
  python3 scripts/grid_no_vol_decline_gauge.py --all --start 2026-05-01 --end 2026-07-30
"""
import argparse
from datetime import datetime, timedelta, timezone

from breakout_score import compute_signals, interval_hours
from grid_gate_backtest import gate_timeline, run_grid
from grid_trend_switch import fetch_range, to_ms


def on_runs(on_timeline):
    """Maximal (start, end) inclusive index runs of consecutive True values."""
    runs = []
    start = None
    for i, v in enumerate(on_timeline):
        if v and start is None:
            start = i
        elif not v and start is not None:
            runs.append((start, i - 1))
            start = None
    if start is not None:
        runs.append((start, len(on_timeline) - 1))
    return runs


def max_decline_in_run(closes, start, end):
    """Worst peak-to-trough decline (positive fraction) within closes[start..end]."""
    peak = closes[start]
    worst = 0.0
    for i in range(start, end + 1):
        peak = max(peak, closes[i])
        worst = max(worst, (peak - closes[i]) / peak)
    return worst


def find_no_volume_declines(closes, on_timeline, abs_pct=15.0, min_run_len=5):
    """On-stretches whose worst peak-to-trough decline ranks in the top abs_pct%
    of all on-stretch declines in this sample — a percentile-rank threshold on
    absolute move size, same spirit as breakout_score.label_indices' abs_pct
    (rank within the sample, not a fixed % or ATR-relative cutoff, per the
    volume_gate_grid_2026-07-06 de-biasing lesson). min_run_len drops short
    runs so the percentile pool isn't dominated by degenerate noise.

    Returns episodes sorted by decline_pct descending: [{start, end, decline_pct, bars}].
    """
    runs = [(s, e) for s, e in on_runs(on_timeline) if e - s + 1 >= min_run_len]
    if not runs:
        return []
    declines = [(s, e, max_decline_in_run(closes, s, e)) for s, e in runs]
    magnitudes = sorted((d for _, _, d in declines), reverse=True)
    cutoff_idx = max(0, int(len(magnitudes) * abs_pct / 100) - 1)
    thr = magnitudes[cutoff_idx]
    episodes = [
        {"start": s, "end": e, "decline_pct": d * 100, "bars": e - s + 1}
        for s, e, d in declines if d >= thr
    ]
    episodes.sort(key=lambda x: -x["decline_pct"])
    return episodes


def gauge(symbol, interval, start_ms, end_ms, window=100, abs_pct=15.0, min_run_len=5,
          exit_thresh=0.70, enter_thresh=0.40, cooldown=3, persistence=3,
          spacing=0.01, fee=0.0005, max_inv=10):
    kl = fetch_range(symbol, interval, start_ms, end_ms)
    if len(kl) < 300:
        print(f"# {symbol} {interval}: only {len(kl)} bars in range, skipping (need >=300)")
        return None
    closes = [k["c"] for k in kl]
    ih = interval_hours(interval)
    score = compute_signals(kl, [], [], window, 24 / ih)["score"]
    on = gate_timeline(score, exit_thresh, enter_thresh, cooldown, persistence)

    gated = run_grid(closes, score, exit_thresh, enter_thresh, cooldown, persistence,
                      spacing=spacing, fee=fee, max_inv=max_inv)
    episodes = find_no_volume_declines(closes, on, abs_pct, min_run_len)

    days = len(closes) * ih / 24
    residual_dd = gated["maxdd"] * 100
    worst_episode = episodes[0]["decline_pct"] if episodes else 0.0
    print(f"# {symbol} {interval}  {len(closes)}根 ~{days:.0f}天  "
          f"gated maxdd={residual_dd:.2f}%  on_frac={gated['on_frac']*100:.0f}%")
    print(f"  no-volume decline episodes (top {abs_pct:.0f}% of on-stretch declines, "
          f">= {min_run_len} bars): {len(episodes)}")
    if episodes:
        mags = sorted(e["decline_pct"] for e in episodes)
        med = mags[len(mags) // 2]
        print(f"  price-decline magnitude (upper bound on equity impact, see docstring): "
              f"worst={worst_episode:.2f}%  median={med:.2f}%  vs strategy residual maxdd={residual_dd:.2f}%")
        for e in episodes[:5]:
            print(f"    bars {e['start']}-{e['end']} ({e['bars']} bars): -{e['decline_pct']:.2f}%")
    return {"symbol": symbol, "interval": interval, "residual_dd": residual_dd,
            "episodes": episodes, "worst_episode": worst_episode}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--symbol", default="BTCUSDT")
    ap.add_argument("--interval", default="15m")
    ap.add_argument("--all", action="store_true",
                     help="run BTC+ETH x 15m+5m, the TED-validated set")
    ap.add_argument("--start", default=None, help="YYYY-MM-DD, default 90 days before --end")
    ap.add_argument("--end", default=None, help="YYYY-MM-DD, default today (UTC)")
    ap.add_argument("--window", type=int, default=100)
    ap.add_argument("--abs-pct", type=float, default=15.0,
                     help="top N%% of on-stretch declines counted as an episode")
    ap.add_argument("--min-run-len", type=int, default=5)
    ap.add_argument("--exit-thresh", type=float, default=0.70)
    ap.add_argument("--enter-thresh", type=float, default=0.40)
    ap.add_argument("--cooldown", type=int, default=3)
    ap.add_argument("--persistence", type=int, default=3)
    ap.add_argument("--spacing", type=float, default=0.01)
    ap.add_argument("--fee", type=float, default=0.0005)
    ap.add_argument("--max-inv", type=int, default=10)
    args = ap.parse_args()

    end_dt = datetime.strptime(args.end, "%Y-%m-%d").replace(tzinfo=timezone.utc) \
        if args.end else datetime.now(timezone.utc)
    start_dt = datetime.strptime(args.start, "%Y-%m-%d").replace(tzinfo=timezone.utc) \
        if args.start else end_dt - timedelta(days=90)
    start_ms, end_ms = int(start_dt.timestamp() * 1000), int(end_dt.timestamp() * 1000)

    targets = [("BTCUSDT", "15m"), ("ETHUSDT", "15m"), ("BTCUSDT", "5m"), ("ETHUSDT", "5m")] \
        if args.all else [(args.symbol, args.interval)]
    for sym, iv in targets:
        gauge(sym, iv, start_ms, end_ms, args.window, args.abs_pct, args.min_run_len,
              args.exit_thresh, args.enter_thresh, args.cooldown, args.persistence,
              args.spacing, args.fee, args.max_inv)


if __name__ == "__main__":
    main()
```

- [ ] **Step 2: Run the sanity check (no network — tests the scanner directly against a hand-built timeline)**

```bash
cd scripts && python3 -c "
from grid_no_vol_decline_gauge import find_no_volume_declines

closes = [100.0] * 10                                    # bars 0-9: flat, on
closes += [100.0 - 0.55 * i for i in range(1, 16)]        # bars 10-24: grind down to 91.75 (on)
closes += [70.0] * 5                                      # bars 25-29: crash while gate is OFF
closes += [70.0] * 10                                     # bars 30-39: flat, on
on_timeline = [True] * 25 + [False] * 5 + [True] * 10
assert len(closes) == len(on_timeline) == 40

episodes = find_no_volume_declines(closes, on_timeline, abs_pct=15.0, min_run_len=3)
print('episodes:', episodes)
assert len(episodes) == 1, f'expected exactly 1 episode, got {len(episodes)}'
ep = episodes[0]
assert ep['start'] == 0 and ep['end'] == 24, f\"expected the on-run (0,24) flagged, got ({ep['start']},{ep['end']})\"
assert 8.0 < ep['decline_pct'] < 8.5, f\"expected ~8.25% decline, got {ep['decline_pct']}\"
print('OK: the 8.25% grind while gated on is flagged; the 30%+ crash during the off-gate stretch (bars 25-29) never enters the on-stretch pool at all.')
"
```

Expected output:
```
episodes: [{'start': 0, 'end': 24, 'decline_pct': 8.25, 'bars': 25}]
OK: the 8.25% grind while gated on is flagged; the 30%+ crash during the off-gate stretch (bars 25-29) never enters the on-stretch pool at all.
```

This demonstrates both required behaviors from the spec's Testing section: a real no-volume-style decline (large, contained entirely within an on-stretch) gets flagged; a much bigger move that happened while the gate was correctly off (TED already caught it — the volume-spike-decline analog) is invisible to the scanner because it's outside any on-stretch, not merely filtered out by magnitude.

- [ ] **Step 3: Confirm a single real symbol/interval runs end to end (network required)**

```bash
cd scripts && python3 grid_no_vol_decline_gauge.py --symbol BTCUSDT --interval 15m --start 2026-05-01 --end 2026-07-30
```

Expected: no traceback, output shaped like:
```
# BTCUSDT 15m  8640根 ~90天  gated maxdd=0.47%  on_frac=27%
  no-volume decline episodes (top 15% of on-stretch declines, >= 5 bars): 28
  price-decline magnitude (upper bound on equity impact, see docstring): worst=2.06%  median=0.78%  vs strategy residual maxdd=0.47%
    bars 3324-3335 (12 bars): -2.06%
    ...
```
(Exact numbers will differ slightly by run date since Binance's returned candle range shifts — the point is the shape and rough magnitude, not exact match.)

- [ ] **Step 4: Commit**

```bash
git add scripts/grid_no_vol_decline_gauge.py
git commit -m "research(grid): add Phase 0 no-volume-decline gauge script

Quantifies the blind spot volume_gate_grid_2026-07-06 named but never
measured — episodes where TED's gate stays on through a decline with no
volume spike. Composes existing breakout_score/grid_gate_backtest/
grid_trend_switch pieces; the only new logic is the episode scanner.
See docs/superpowers/specs/2026-07-31-grid-no-vol-decline-gauge-design.md."
```

---

### Task 3: Run Phase 0 across the TED-validated set and record the decision

**Files:**
- Create: a new memory entry (path depends on outcome — see below) documenting the Phase 0 result and the decision.

**Interfaces:**
- Consumes: `scripts/grid_no_vol_decline_gauge.py --all` output from Task 2.

This task is where Phase 0 actually answers its question. There's no code deliverable — the deliverable is the decision, made against the criteria the spec already fixed, and an honest write-up either way (per this project's established culture of recording negative results, not just positive ones — see [[oss_idea_shelved_2026-07-13]], [[directional_intraday_dead_2026-07-14]], [[funding_momentum_2026-07-13]] for the pattern).

- [ ] **Step 1: Run the full TED-validated sweep**

```bash
cd scripts && python3 grid_no_vol_decline_gauge.py --all --start 2026-05-01 --end 2026-07-30
```

This prints one block per symbol/interval (BTCUSDT/ETHUSDT × 15m/5m): episode count, worst/median decline magnitude, and the strategy's own residual maxdd for context.

- [ ] **Step 2: Apply the decision gate from the spec**

Per `docs/superpowers/specs/2026-07-31-grid-no-vol-decline-gauge-design.md`'s Decision gate section:
- If episodes are rare across all four symbol/interval combos, OR their magnitude is small relative to the strategy's own residual drawdown (i.e., the "worst" episode isn't meaningfully bigger than `residual maxdd`) → **stop, this is a negative result.**
- If episodes are frequent (double digits over a 90-day window, matching what showed up during this plan's own dry runs) AND their worst-case magnitude is a multiple of the residual maxdd → **the gap is real, proceed to Phase 1** (candidate trend signal gauge — not scoped in this plan; comes back to brainstorming to scope Phase 1's exact candidates/OOS design once this result is in hand, per the spec's explicit deferral).

- [ ] **Step 3a (if negative result): write the memory and stop here**

Create `~/.claude/projects/-Users-apexis-backdesk-project-go-workspace-Quantix/memory/grid_no_vol_decline_gauge_2026-07-31.md` (adjust date if run later) with frontmatter:
```yaml
---
name: grid-no-vol-decline-gauge-2026-07-31
description: "Phase 0 of the Trend gate line — quantified the '无量阴跌' blind spot, [rare/small — closed] not worth a Phase 1 detector"
metadata:
  type: project
---
```
Body: the actual numbers from Step 1 (episode counts, worst/median magnitude, residual maxdd per symbol/interval), the conclusion that the gap doesn't justify Phase 1, and a link back to `[[volume_gate_grid_2026-07-06]]`. Add a one-line pointer to `MEMORY.md`'s index.

- [ ] **Step 3b (if positive result): write the memory and hand off to Phase 1 scoping**

Same file path/frontmatter pattern as 3a but describing the confirmed gap (numbers from Step 1) and that Phase 1 is next. Do not start Phase 1 implementation in this task — per the spec, Phase 1's exact candidate signals and OOS design get scoped through brainstorming once Phase 0's real numbers are in hand, not pre-committed now.

- [ ] **Step 4: Commit the memory file and MEMORY.md index update**

```bash
git -C ~/.claude/projects/-Users-apexis-backdesk-project-go-workspace-Quantix/memory add grid_no_vol_decline_gauge_2026-07-31.md MEMORY.md
git -C ~/.claude/projects/-Users-apexis-backdesk-project-go-workspace-Quantix/memory commit -m "Phase 0 result: grid no-volume-decline gap gauge"
```
(Only if that memory directory is itself a git repo — if not, this step is just saving the file; skip the git commands.)

---

## Self-Review Notes

- **Spec coverage**: Phase 0's data fetch (✓ Task 2, via `fetch_range`), TED score reuse (✓, `compute_signals`), gate timeline reuse (✓ Task 1's extraction), absolute-percentile episode threshold (✓ `find_no_volume_declines`), residual-drawdown context (✓ `gauge()`'s output, with the unit-mismatch caveat made explicit — this refines the spec's wording slightly, see below), decision gate (✓ Task 3), lightweight sanity check (✓ Task 2 Step 2), non-goals respected (no trend-following leg anywhere in this plan).
- **Deviation from spec worth flagging**: the spec's Phase 0 description implies episode magnitude can be directly read as "contribution to residual drawdown." Building and dry-running the script (during this planning session) surfaced that price-decline % and equity maxdd % are different units (equity impact depends on how much of the grid's inventory was filled), so `gauge()`'s output labels this explicitly as an upper bound / orientation figure rather than a literal attribution. This doesn't change the decision gate's substance, only how precisely the number can be interpreted.
- **Placeholder scan**: none — every step has real, copy-pasteable code or commands with real expected output captured by actually running the code during planning.
- **Type consistency**: `find_no_volume_declines` returns `list[dict]` with keys `start`/`end`/`decline_pct`/`bars` — used consistently in `gauge()`'s printing and in Task 3's decision-gate description.
