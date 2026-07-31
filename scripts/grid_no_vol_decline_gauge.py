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
