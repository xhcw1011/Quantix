#!/usr/bin/env python3
"""
trade_diagnose.py — post-trade attribution for Quantix backtests.

Reads the trades CSV emitted by internal/backtest.WriteTradesCSV and answers the
question that matters more than any entry-signal tweak: *where do the losses
actually come from?* It buckets trades by exit reason and market regime, reports
per-bucket expectancy, and flags the one or two scenarios that carry most of the
damage — the ones worth filtering before touching an EMA length.

It also runs the anti-overfitting check: with --split-date it computes each
bucket's expectancy in-sample, then reports whether that same bucket stays
negative out-of-sample. A bucket that only loses in-sample is noise, not a rule.

Usage:
    python3 scripts/trade_diagnose.py reports/bt_ctx_trades.csv
    python3 scripts/trade_diagnose.py reports/*.csv --split-date 2026-05-01
    python3 scripts/trade_diagnose.py trades.csv --min-bucket 30

Works on old CSVs too (missing attribution columns degrade gracefully).
"""

import argparse
import csv
import glob
import json
import math
import sys
from collections import defaultdict


# ─────────────────────────── loading ────────────────────────────

def _f(row, key, default=0.0):
    v = row.get(key, "")
    if v is None or v == "":
        return default
    try:
        return float(v)
    except ValueError:
        return default


def load(paths):
    trades = []
    for pat in paths:
        for path in sorted(glob.glob(pat)) or [pat]:
            try:
                with open(path, newline="") as fh:
                    for row in csv.DictReader(fh):
                        t = {
                            "symbol": row.get("symbol", ""),
                            "side": row.get("side", ""),
                            "entry_time": row.get("entry_time", ""),
                            "net_pnl": _f(row, "net_pnl"),
                            "pnl_pct": _f(row, "pnl_pct"),
                            "exit_reason": row.get("exit_reason", "") or "(untagged)",
                            "mfe_pct": _f(row, "mfe_pct"),
                            "mae_pct": _f(row, "mae_pct"),
                            "mfe_r": _f(row, "mfe_r"),
                            "mae_r": _f(row, "mae_r"),
                            "regime": "(none)",
                            "source": path,
                        }
                        meta_raw = row.get("entry_meta", "")
                        if meta_raw:
                            try:
                                meta = json.loads(meta_raw)
                                t["meta"] = meta
                                if "regime" in meta:
                                    t["regime"] = _regime_label(meta["regime"])
                            except (ValueError, TypeError):
                                pass
                        trades.append(t)
            except FileNotFoundError:
                print(f"! file not found: {path}", file=sys.stderr)
    return trades


def _regime_label(v):
    # Numeric regime codes → readable labels; strings pass through. Codes are
    # strategy-defined. aistrat.regimeCode: 0=range 1=slow_trend 2=strong_trend
    # 3=expansion -1=unknown. (aistrat_v4.volRegime instead emits 0/1/2 = vol buckets.)
    labels = {0: "range", 1: "slow_trend", 2: "strong_trend", 3: "expansion", -1: "unknown"}
    try:
        return labels.get(int(v), f"regime_{int(v)}")
    except (ValueError, TypeError):
        return str(v)


# ─────────────────────────── stats ──────────────────────────────

def stats(trades):
    n = len(trades)
    if n == 0:
        return None
    wins = [t for t in trades if t["net_pnl"] >= 0]
    losses = [t for t in trades if t["net_pnl"] < 0]
    gross_win = sum(t["net_pnl"] for t in wins)
    gross_loss = -sum(t["net_pnl"] for t in losses)  # positive magnitude
    net = sum(t["net_pnl"] for t in trades)
    return {
        "n": n,
        "net": net,
        "expectancy": net / n,
        "win_rate": len(wins) / n * 100,
        "n_win": len(wins),
        "n_loss": len(losses),
        "avg_win": (gross_win / len(wins)) if wins else 0.0,
        "avg_loss": (gross_loss / len(losses)) if losses else 0.0,
        "gross_win": gross_win,
        "gross_loss": gross_loss,
        "profit_factor": (gross_win / gross_loss) if gross_loss > 0 else float("inf"),
    }


def bucketize(trades, keyfn):
    buckets = defaultdict(list)
    for t in trades:
        buckets[keyfn(t)].append(t)
    return buckets


def fmt_money(x):
    return f"{x:+,.2f}"


def bucket_table(title, buckets, total_gross_loss, min_bucket):
    print(f"\n{title}")
    print("-" * len(title))
    rows = []
    for key, ts in buckets.items():
        s = stats(ts)
        loss_share = (s["gross_loss"] / total_gross_loss * 100) if total_gross_loss > 0 else 0.0
        rows.append((key, s, loss_share))
    # Sort by total loss contribution (biggest bleeders first).
    rows.sort(key=lambda r: -r[2])
    hdr = f"{'bucket':<20}{'n':>6}{'win%':>7}{'expect':>10}{'net':>12}{'loss_share':>12}"
    print(hdr)
    print("-" * len(hdr))
    for key, s, loss_share in rows:
        flag = ""
        if s["expectancy"] < 0 and s["n"] >= min_bucket:
            flag = "  <-- NEGATIVE"
        elif s["n"] < min_bucket:
            flag = "  (thin)"
        print(f"{str(key):<20}{s['n']:>6}{s['win_rate']:>6.1f}%"
              f"{s['expectancy']:>10.2f}{fmt_money(s['net']):>12}"
              f"{loss_share:>11.1f}%{flag}")


# ───────────────────── MAE / MFE diagnostics ────────────────────

def excursion_report(trades):
    have = [t for t in trades if t["mfe_pct"] != 0 or t["mae_pct"] != 0]
    if not have:
        print("\nMAE/MFE: no excursion data in this CSV (pre-attribution run).")
        return
    wins = [t for t in have if t["net_pnl"] >= 0]
    losses = [t for t in have if t["net_pnl"] < 0]

    def mean(xs):
        return sum(xs) / len(xs) if xs else 0.0

    print("\nMAE / MFE diagnostics")
    print("---------------------")
    # Capture efficiency: how much of the peak favourable move winners kept.
    cap = [t["pnl_pct"] / t["mfe_pct"] for t in wins if t["mfe_pct"] > 0]
    if cap:
        print(f"  winners' capture efficiency (realised / peak):  {mean(cap)*100:5.1f}%")
        print(f"    -> handed back {100 - mean(cap)*100:.1f}% of peak profit on average"
              " (TP / trailing calibration)")
    # Stop calibration: how deep winners dig before recovering vs losers.
    if wins:
        print(f"  winners' avg MAE (deepest drawdown before win): {mean([t['mae_pct'] for t in wins]):6.2f}%")
    if losses:
        print(f"  losers'  avg MAE:                               {mean([t['mae_pct'] for t in losses]):6.2f}%")
        print(f"  losers'  avg MFE (peak profit before losing):   {mean([t['mfe_pct'] for t in losses]):6.2f}%")
    # Stopped-in-profit: losers that were once meaningfully green.
    if losses:
        gave_back = [t for t in losses if t["mfe_pct"] > 0.5]
        print(f"  losers that were ever > +0.5%:                  "
              f"{len(gave_back)}/{len(losses)} ({len(gave_back)/len(losses)*100:.0f}%)"
              "  <- were winning, then stopped for a loss")
    # R-multiple view when stops were recorded.
    r_have = [t for t in have if t["mfe_r"] != 0 or t["mae_r"] != 0]
    if r_have:
        rw = [t for t in r_have if t["net_pnl"] >= 0]
        rl = [t for t in r_have if t["net_pnl"] < 0]
        if rw:
            print(f"  winners' avg MFE in R:                          {mean([t['mfe_r'] for t in rw]):6.2f}R")
        if rl:
            print(f"  losers'  avg MAE in R:                          {mean([t['mae_r'] for t in rl]):6.2f}R")


# ───────────────────────── OOS split ────────────────────────────

def oos_check(trades, split_date, keyfn, label, min_bucket):
    ins = [t for t in trades if t["entry_time"][:10] < split_date]
    oos = [t for t in trades if t["entry_time"][:10] >= split_date]
    if not ins or not oos:
        print(f"\nOOS split at {split_date}: not enough data on one side "
              f"(in={len(ins)}, out={len(oos)}).")
        return
    print(f"\nOOS stability by {label}  (split {split_date}: "
          f"{len(ins)} in-sample / {len(oos)} out-of-sample)")
    print("  a rule is only real if a bucket stays negative on BOTH sides")
    bi = bucketize(ins, keyfn)
    bo = bucketize(oos, keyfn)
    hdr = f"  {'bucket':<20}{'IS n':>6}{'IS exp':>9}{'OOS n':>7}{'OOS exp':>9}   verdict"
    print(hdr)
    print("  " + "-" * (len(hdr) - 2))
    for key in sorted(set(bi) | set(bo)):
        si = stats(bi.get(key, []))
        so = stats(bo.get(key, []))
        ie = si["expectancy"] if si else float("nan")
        oe = so["expectancy"] if so else float("nan")
        ni = si["n"] if si else 0
        no = so["n"] if so else 0
        verdict = ""
        if si and so and ni >= min_bucket and no >= min_bucket:
            if ie < 0 and oe < 0:
                verdict = "STABLE NEGATIVE -> filter candidate"
            elif ie < 0 and oe >= 0:
                verdict = "in-sample only -> likely overfit"
            elif ie >= 0 and oe < 0:
                verdict = "flipped negative OOS -> unstable"
        else:
            verdict = "(thin)"
        ie_s = f"{ie:>9.2f}" if si else f"{'-':>9}"
        oe_s = f"{oe:>9.2f}" if so else f"{'-':>9}"
        print(f"  {str(key):<20}{ni:>6}{ie_s}{no:>7}{oe_s}   {verdict}")


# ─────────────────────────── main ───────────────────────────────

def main():
    ap = argparse.ArgumentParser(description="Post-trade attribution for Quantix backtests.")
    ap.add_argument("csv", nargs="+", help="trades CSV path(s) or glob(s)")
    ap.add_argument("--split-date", help="YYYY-MM-DD: in-sample/out-of-sample boundary for the stability check")
    ap.add_argument("--min-bucket", type=int, default=25, help="min trades for a bucket verdict (default 25)")
    args = ap.parse_args()

    trades = load(args.csv)
    if not trades:
        print("no trades loaded.", file=sys.stderr)
        sys.exit(1)

    overall = stats(trades)
    print("=" * 60)
    print(f"  OVERALL — {overall['n']} trades")
    print("=" * 60)
    print(f"  net pnl        {fmt_money(overall['net'])}")
    print(f"  expectancy     {overall['expectancy']:+.2f} per trade")
    print(f"  win rate       {overall['win_rate']:.1f}%  ({overall['n_win']}W / {overall['n_loss']}L)")
    print(f"  avg win        {overall['avg_win']:+.2f}")
    print(f"  avg loss       {-overall['avg_loss']:+.2f}")
    pf = overall["profit_factor"]
    print(f"  profit factor  {pf:.2f}" if math.isfinite(pf) else "  profit factor  inf")

    tgl = overall["gross_loss"]
    bucket_table("LOSS ATTRIBUTION by exit_reason", bucketize(trades, lambda t: t["exit_reason"]), tgl, args.min_bucket)

    has_regime = any(t["regime"] != "(none)" for t in trades)
    if has_regime:
        bucket_table("LOSS ATTRIBUTION by regime", bucketize(trades, lambda t: t["regime"]), tgl, args.min_bucket)
        bucket_table("LOSS ATTRIBUTION by regime x exit_reason",
                     bucketize(trades, lambda t: f"{t['regime']}/{t['exit_reason']}"), tgl, args.min_bucket)
    else:
        print("\n(no regime data yet — populate OrderRequest.Meta['regime'] at entry to unlock regime buckets)")

    excursion_report(trades)

    if args.split_date:
        oos_check(trades, args.split_date, lambda t: t["exit_reason"], "exit_reason", args.min_bucket)
        if has_regime:
            oos_check(trades, args.split_date, lambda t: f"{t['regime']}/{t['exit_reason']}",
                      "regime x exit_reason", args.min_bucket)

    print()


if __name__ == "__main__":
    main()
