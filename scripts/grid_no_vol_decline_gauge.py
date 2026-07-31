#!/usr/bin/env python3
"""grid_no_vol_decline_gauge — Phase 0 of the "Trend" gate research line.

TED (internal/strategy/grid/volgate.go, scripts/breakout_score.py's vol_hi+vol_up)
only sees volume. volume_gate_grid_2026-07-06 noted in passing, never measured,
that it "挡不住无量阴跌" — a steady decline with no volume spike never crosses the
exit threshold, so the gate stays on and rides the decline. This script quantifies
that blind spot before anyone builds a second (trend/structure) gate signal for it:
how often does the gate stay "on" through a large decline, and how large are those
declines relative to the strategy's already-tiny residual drawdown.

Note on units (bug found + fixed 2026-07-31, during final review, before this line
moved on): an early draft of this script compared each episode's raw PRICE decline
% (peak-to-trough close move) directly against the strategy's EQUITY maxdd % —
different units, and since maxdd is the GLOBAL running-peak drawdown, no single
sub-window's contribution can ever exceed it, so any reported ratio above 1.0x was
not just imprecise but logically impossible. Fixed: episode_equity_dd() computes
each episode's own EQUITY drawdown (peak reset to the window's start, same capital
base run_grid uses: max_inv * closes[0]) and compares that to the strategy's global
equity maxdd — bounded <= 1.0x by construction (see episode_equity_dd's docstring
for the proof sketch). Raw price decline_pct is still reported, but only as
market-move context (and against the max_inv inventory bound, see
max_inv_bound_pct), never as an equity proxy.

Note on episode counts: find_no_volume_declines always selects the top abs_pct% of
on-stretch declines by construction — so its episode COUNT is tautological (~abs_pct%
of on-stretch count, true regardless of whether anything in the data is actually big).
gauge() additionally reports a count of on-stretches clearing an ABSOLUTE, falsifiable
bar (equity-dd >= ABS_DD_FRAC of the strategy's own gated maxdd) that can legitimately
come out at zero — see ABS_DD_FRAC below for why 20% was chosen.

Note on warmup: gate_timeline treats "no lagged score yet" (bar 0, or any bar whose
score[i-1] is None because compute_signals hasn't warmed up) as gate-OFF. The Go
production gate (internal/strategy/grid/volgate.go's volGate.update) does the
opposite during warmup — insufficient history keeps the gate ON. Same 3-mechanism
state machine (hysteresis/cooldown/persistence) once both are warmed up, but this is
not a byte-for-byte equivalent implementation — the warmup edge case differs.
Immaterial to this gauge's conclusion (warmup is ~50-100 bars out of 8640+ per run)
but don't cite this script as proof the two never diverge.

See docs/superpowers/specs/2026-07-31-grid-no-vol-decline-gauge-design.md.

Usage:
  python3 scripts/grid_no_vol_decline_gauge.py --symbol BTCUSDT --interval 15m
  python3 scripts/grid_no_vol_decline_gauge.py --all   # BTC+ETH x 15m+5m, the TED-validated set
  python3 scripts/grid_no_vol_decline_gauge.py --all --start 2026-05-01 --end 2026-07-30

Note on the sanity check (see repo's implementation-plan Task 2 Step 2 for the
copy-pasteable command): it exercises find_no_volume_declines() directly against a
hand-built on_timeline, not the full compute_signals -> gate_timeline pipeline —
it's a scanner-logic check, not an end-to-end signal test. Only gauge()'s real
network run exercises the full pipeline.
"""
import argparse
import math
from datetime import datetime, timedelta, timezone

from breakout_score import compute_signals, interval_hours
from grid_gate_backtest import gate_timeline, run_grid
from grid_trend_switch import fetch_range

# Falsifiable episode bar: an on-stretch "counts" only if its own equity drawdown is
# at least this fraction of the strategy's GLOBAL gated maxdd. 20% is a materiality
# bar, not a significance test: it means a single episode alone explains at least a
# fifth of the strategy's entire sample-period worst-case pain — clearly non-trivial
# — while still being far below 100%, so it doesn't require an episode to literally
# BE the global worst moment to count. Below this, an episode's equity contribution
# is noise-level relative to the number that actually matters to a trader (the
# realized worst drawdown). Unlike the percentile-ranked list below (which always
# returns ~abs_pct% of on-stretches by construction, true or not), this count can
# legitimately come out as zero if the data doesn't support it.
ABS_DD_FRAC = 0.20


def median(vals):
    """Proper averaged median — mags[len(mags)//2] alone is an upper-median and is
    wrong for even-length lists (e.g. [1,2,3,4] -> mags[2]=3, not the true median 2.5)."""
    s = sorted(vals)
    n = len(s)
    if n == 0:
        return 0.0
    mid = n // 2
    if n % 2 == 0:
        return (s[mid - 1] + s[mid]) / 2
    return s[mid]


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


def on_stretches(on_timeline, min_run_len=5):
    """All on-stretches (start, end) inclusive with length >= min_run_len."""
    return [(s, e) for s, e in on_runs(on_timeline) if e - s + 1 >= min_run_len]


def max_decline_in_run(closes, start, end):
    """Worst peak-to-trough PRICE decline (positive fraction) within closes[start..end].
    Market-move context only — NOT directly comparable to equity maxdd, see module
    docstring and episode_equity_dd()."""
    peak = closes[start]
    worst = 0.0
    for i in range(start, end + 1):
        peak = max(peak, closes[i])
        worst = max(worst, (peak - closes[i]) / peak)
    return worst


def episode_equity_dd(equity_curve, start, end, capital):
    """Worst peak-to-trough EQUITY drawdown within [start, end], as a fraction of the
    same capital base run_grid uses (max_inv * closes[0]).

    The peak resets to equity_curve[start] (window-local), not the all-time running
    peak run_grid's global maxdd uses — and that's exactly why this is directly
    comparable to (and always <= ) the global maxdd: at any bar i inside the window,
    the GLOBAL running peak (which already incorporates all history up to i, both
    before and during the window) is >= this window-local peak, so
    global_dd(i) = global_peak(i) - equity(i) >= window_peak(i) - equity(i) = window_dd(i)
    for every i in the window. Hence max(window_dd) <= max(global_dd over the window)
    <= max(global_dd over all bars) = the strategy's reported maxdd. This is what
    replaces the old, unit-mismatched "price-decline % vs equity maxdd %" comparison.
    """
    if capital <= 0:
        return 0.0
    peak = equity_curve[start]
    worst = 0.0
    for i in range(start, end + 1):
        peak = max(peak, equity_curve[i])
        worst = max(worst, peak - equity_curve[i])
    return worst / capital


def episode_pinned(pos_curve, start, end, max_inv):
    """True if inventory ever hit the +-max_inv bound during [start, end] — i.e. the
    grid genuinely ran out of room during this episode, not just moved through it."""
    return any(abs(pos_curve[i]) >= max_inv for i in range(start, end + 1))


def max_inv_bound_pct(spacing, max_inv):
    """SUFFICIENT (not exact) price decline (%) that guarantees walking the grid down
    max_inv levels and exhausting long-side inventory, using the same log-spaced level
    math run_grid/gate_timeline's caller uses (level_of(p) = floor(log(p/p0)/step),
    step = log(1+spacing)).

    This is NOT a single fixed bound: level_of floors, so wherever an on-stretch's
    reference price sits within its own level (not necessarily at the boundary)
    changes how far price actually has to move to cross max_inv more level
    boundaries. The true requirement is a RANGE — see max_inv_bound_range_pct() for
    both ends. This function returns only the sufficient (upper, worst-alignment)
    end, ~9.47% at defaults (spacing=0.01, max_inv=10), for callers that just want a
    single conservative number; do not cite it as an exact threshold."""
    return (1 - (1 + spacing) ** -max_inv) * 100


def max_inv_bound_range_pct(spacing, max_inv):
    """(necessary_pct, sufficient_pct) price-decline range needed to reach
    pos==max_inv, per max_inv_bound_pct's docstring:
      - necessary (best case: reference price already at the bottom of its level,
        so only max_inv-1 more boundary crossings are needed):
        1 - (1+spacing)^-(max_inv-1)  (~8.57% at defaults)
      - sufficient (worst case: reference price at the top of its level, a full
        max_inv boundary crossings needed): 1 - (1+spacing)^-max_inv (~9.47%)
    The actual bound for any given on-stretch falls somewhere in this range
    depending on where its reference price landed within its level; this range is
    the honest bracket, not a point estimate."""
    necessary = (1 - (1 + spacing) ** -(max_inv - 1)) * 100
    sufficient = (1 - (1 + spacing) ** -max_inv) * 100
    return necessary, sufficient


def find_no_volume_declines(closes, on_timeline, abs_pct=15.0, min_run_len=5):
    """On-stretches whose worst peak-to-trough PRICE decline ranks in the top
    abs_pct% of all on-stretch declines in this sample — a percentile-rank
    threshold on absolute move size, same spirit as breakout_score.label_indices'
    abs_pct (rank within the sample, not a fixed % or ATR-relative cutoff, per the
    volume_gate_grid_2026-07-06 de-biasing lesson). min_run_len drops short runs so
    the percentile pool isn't dominated by degenerate noise.

    This list's episode COUNT is tautological by construction (~abs_pct% of the
    on-stretch count, always) — see gauge()'s separate ABS_DD_FRAC-based count for a
    falsifiable alternative. This percentile list is still useful as "here are the
    worst stretches in this sample" context, just not as evidence of frequency.

    Returns episodes sorted by decline_pct descending: [{start, end, decline_pct, bars}].
    """
    runs = on_stretches(on_timeline, min_run_len)
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
    capital = max_inv * closes[0]           # same capital base run_grid uses
    gated_maxdd = gated["maxdd"]            # fraction of capital (global running-peak dd)

    n = len(closes)
    all_runs = on_stretches(on, min_run_len)
    stats_by_key = {}
    for s, e in all_runs:
        # Extend the equity-dd window to include the flatten bar (end+1, clamped to
        # n-1): when the gate flips off, run_grid closes the position with
        # fill(p, -pos) on the OFF bar itself (grid_gate_backtest.py's
        # `if not active: fill(p, -pos)`), i.e. one bar past this on-stretch's `end`.
        # That's where the stretch's damage is actually realized in cash, so
        # excluding it would understate the episode's true equity impact — for
        # BTC/ETH 5m the strategy's actual global-worst equity point lands exactly
        # on this bar. [start, end] alone is also a defensible boundary; this is a
        # deliberate, more-conservative choice for a risk-quantification script, not
        # an oversight.
        dd_end = min(e + 1, n - 1)
        dd = episode_equity_dd(gated["equity_curve"], s, dd_end, capital)
        stats_by_key[(s, e)] = {
            "equity_dd_pct": dd * 100,
            "equity_dd_ratio": (dd / gated_maxdd) if gated_maxdd > 0 else float("nan"),
            "pinned": episode_pinned(gated["pos_curve"], s, e, max_inv),
        }
    bound_necessary_pct, bound_sufficient_pct = max_inv_bound_range_pct(spacing, max_inv)

    # Price-decline-ranked subset (top abs_pct% by raw price move) — kept for its own
    # purpose (showing the biggest raw market moves as context) but is a DIFFERENT,
    # smaller population than stats_by_key/all_runs below; see the print labels.
    episodes = find_no_volume_declines(closes, on, abs_pct, min_run_len)
    for ep in episodes:
        ep.update(stats_by_key[(ep["start"], ep["end"])])
        ep["bound_frac_pct"] = (ep["decline_pct"] / bound_sufficient_pct * 100) \
            if bound_sufficient_pct > 0 else float("nan")

    # Everything below this line is computed over stats_by_key / all_runs — ALL
    # candidate on-stretches, not just the price-decline top-abs_pct% subset above.
    abs_count = sum(1 for st in stats_by_key.values()
                     if not math.isnan(st["equity_dd_ratio"]) and st["equity_dd_ratio"] >= ABS_DD_FRAC)
    pinned_count = sum(1 for st in stats_by_key.values() if st["pinned"])
    all_ratios = [st["equity_dd_ratio"] for st in stats_by_key.values()
                  if not math.isnan(st["equity_dd_ratio"])]
    stretch_lengths = [e - s + 1 for s, e in all_runs]
    median_stretch_len = median(stretch_lengths) if stretch_lengths else 0.0
    # Global max |pos| reached ANYWHERE in the run (on-stretch or off), not just
    # within on-stretches — gives "how much of the max_inv budget did the grid ever
    # actually use" independent of the gate.
    global_max_pos = max((abs(p) for p in gated["pos_curve"]), default=0.0)

    days = len(closes) * ih / 24
    residual_dd = gated_maxdd * 100
    print(f"# {symbol} {interval}  {len(closes)}根 ~{days:.0f}天  "
          f"gated maxdd={residual_dd:.2f}%  on_frac={gated['on_frac']*100:.0f}%  "
          f"on-stretches(>={min_run_len}b)={len(all_runs)}")
    print(f"  [population: ALL {len(all_runs)} on-stretches >= {min_run_len} bars]  "
          f"falsifiable count (equity-dd >= {ABS_DD_FRAC*100:.0f}% of gated maxdd): {abs_count}   "
          f"median on-stretch length: {median_stretch_len:.1f} bars")
    if all_ratios:
        print(f"  [population: ALL {len(all_runs)} on-stretches, same population as above — "
              f"NOT the price-decline-ranked list printed below]  "
              f"equity-dd ratio vs global gated maxdd (the real comparison, always <= 1.0x): "
              f"worst={max(all_ratios):.3f}x  median={median(all_ratios):.3f}x")
    print(f"  max_inv inventory bound: {bound_necessary_pct:.2f}%-{bound_sufficient_pct:.2f}% price move "
          f"needed to pin pos==max_inv depending on level alignment (sufficient/conservative bound: "
          f"{bound_sufficient_pct:.2f}%; spacing={spacing*100:.1f}%, max_inv={max_inv})")
    print(f"  [population: ALL {len(all_runs)} on-stretches]  {pinned_count}/{len(all_runs)} actually "
          f"pinned there  |  global max |pos| reached anywhere in the run (on-stretch or off, "
          f"i.e. even less selective a population than 'all on-stretches'): {global_max_pos:.0f}/{max_inv} "
          f"({global_max_pos / max_inv * 100:.0f}% of inventory budget) — read together with the median "
          f"on-stretch length above: short stretches have little power to ever reach the bound regardless "
          f"of how small the underlying risk is")
    print(f"  [population: top {abs_pct:.0f}% BY PRICE DECLINE only — {len(episodes)} of the "
          f"{len(all_runs)} on-stretches above, a DIFFERENT/smaller subset than the ratios/counts "
          f"printed above, shown below purely as 'here are the biggest raw price moves' context; "
          f"count is tautological by construction (~{abs_pct:.0f}% of on-stretch count, always) and this "
          f"subset need NOT contain the worst equity-dd stretch found above]")
    if episodes:
        price_mags = [e["decline_pct"] for e in episodes]
        print(f"  price-decline (market-move context, NOT comparable to equity maxdd — see docstring): "
              f"worst={price_mags[0]:.2f}%  median={median(price_mags):.2f}%")
        for e in episodes[:5]:
            print(f"    bars {e['start']}-{e['end']} ({e['bars']} bars): price -{e['decline_pct']:.2f}% "
                  f"(={e['bound_frac_pct']:.0f}% of max_inv sufficient bound, pinned={e['pinned']})  "
                  f"equity-dd {e['equity_dd_pct']:.3f}% ({e['equity_dd_ratio']:.3f}x gated maxdd)")
    return {"symbol": symbol, "interval": interval, "residual_dd": residual_dd,
            "episodes": episodes, "abs_count": abs_count, "on_stretch_count": len(all_runs),
            "pinned_count": pinned_count, "bound_pct": bound_sufficient_pct,
            "bound_necessary_pct": bound_necessary_pct, "bound_sufficient_pct": bound_sufficient_pct,
            "worst_ratio": max(all_ratios) if all_ratios else float("nan"),
            "median_ratio": median(all_ratios) if all_ratios else float("nan"),
            "median_stretch_len": median_stretch_len, "global_max_pos": global_max_pos}


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
