#!/usr/bin/env python3
"""Baseline Behavior Snapshot — 各策略在"无 Portfolio 干预"下的自然行为画像。

这是观察,不是优化:读 cmd/backtest 的 -out-json 报告,提取每个策略的
胜率 / 持仓时长 / churn / 敞口形态 / 回撤分布——不改任何引擎结构。

用法: python3 scripts/baseline_report.py reports/baseline/*.json
"""
import glob
import json
import sys
from datetime import datetime


def parse(t):
    # RFC3339 with offset, e.g. 2026-01-22T12:59:59.999+08:00
    try:
        return datetime.fromisoformat(t)
    except ValueError:
        # trim fractional seconds if the runtime is picky
        head, _, tail = t.partition(".")
        off = tail[-6:] if tail and tail[-6] in "+-" else ""
        return datetime.fromisoformat(head + off)


def pctl(sorted_vals, q):
    if not sorted_vals:
        return 0.0
    return sorted_vals[min(len(sorted_vals) - 1, int(len(sorted_vals) * q))]


def analyze(d, label):
    r = {"name": label}
    r["ret"] = d["TotalReturn"]
    r["pf"] = d["ProfitFactor"]
    r["maxdd"] = d["MaxDrawdown"]
    r["trades"] = d["TotalTrades"]
    r["winrate"] = d["WinRate"]

    start, end = parse(d["StartTime"]), parse(d["EndTime"])
    days = max((end - start).total_seconds() / 86400, 1e-9)
    r["tr_day"] = r["trades"] / days

    # holding time (median hours) from closed trades
    holds = sorted(
        (parse(t["ExitTime"]) - parse(t["EntryTime"])).total_seconds() / 3600
        for t in d["Trades"] if t.get("ExitTime") and t.get("EntryTime")
    )
    r["hold_med_h"] = pctl(holds, 0.5)

    # exposure = invested fraction (Equity - Cash) / Equity, per equity point
    eq = d["EquityCurve"]
    exps = [(p["Equity"] - p["Cash"]) / p["Equity"] for p in eq if p["Equity"] > 0]
    r["exp_mean"] = sum(exps) / len(exps) if exps else 0.0
    r["in_mkt"] = sum(1 for e in exps if e > 0.01) / len(exps) if exps else 0.0

    # drawdown distribution from equity curve
    vals = [p["Equity"] for p in eq]
    peak, dds = vals[0] if vals else 0, []
    for v in vals:
        peak = max(peak, v)
        dds.append((peak - v) / peak if peak > 0 else 0.0)
    indd = sorted(x for x in dds if x > 0.005)
    r["in_dd"] = len(indd) / len(dds) if dds else 0.0
    r["dd_med"] = pctl(indd, 0.5)
    r["dd_p90"] = pctl(indd, 0.9)
    return r


def main():
    import os
    paths = sys.argv[1:] or sorted(glob.glob("reports/baseline/*.json"))
    rows = [analyze(json.load(open(p)), os.path.splitext(os.path.basename(p))[0]) for p in paths]
    rows.sort(key=lambda x: -x["tr_day"])  # churniest first

    print("# Baseline Behavior Snapshot — 自然交易形态(无 Portfolio 干预)")
    print(f"# {len(rows)} 策略 · ETHUSDT 15m · 108 天 · 本金 1万 · 仅观察")
    print("=" * 92)
    print(f"  {'strat':<14}{'ret%':>7}{'PF':>6}{'胜率':>6}{'交易':>6}{'笔/天':>7}"
          f"{'持仓中位h':>10}{'均敞口':>8}{'在场%':>7}{'在回撤%':>8}{'DD中位':>7}{'DDp90':>7}")
    print("-" * 92)
    for r in rows:
        print(f"  {r['name']:<14}{r['ret']:>7.1f}{r['pf']:>6.2f}{r['winrate']:>5.0f}%"
              f"{r['trades']:>6}{r['tr_day']:>7.1f}{r['hold_med_h']:>10.1f}"
              f"{r['exp_mean']*100:>7.0f}%{r['in_mkt']*100:>6.0f}%{r['in_dd']*100:>7.0f}%"
              f"{r['dd_med']*100:>6.1f}%{r['dd_p90']*100:>6.1f}%")
    print("=" * 92)
    print("  churn=笔/天 · 均敞口=已投入/权益 · 在场%=有仓时间占比 · 在回撤%=处于回撤的时间占比")


if __name__ == "__main__":
    main()
