#!/usr/bin/env python3
# Cross-sectional momentum backtester (stdlib only).
# Rank a basket by lookback return, long top-K / short bottom-K, rebalance every R days.
# 1-day execution lag (signal from close[i-lag]), costs on turnover. No lookahead.
#
# usage: xsmom.py <csv> <lookbackDays> <rebalanceDays> <K> <ls|lo> <costPerSide> [startDate] [endDate]
import sys, math
from collections import defaultdict

csv, L, R, K = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4])
mode = sys.argv[5]                       # ls = long-short, lo = long-only
cost = float(sys.argv[6])                # per-side fraction, e.g. 0.0005
start = sys.argv[7] if len(sys.argv) > 7 else "0000"
end   = sys.argv[8] if len(sys.argv) > 8 else "9999"
LAG = 1                                  # signal from close[i-LAG], enter at close[i]

# load
px = defaultdict(dict)                    # px[date][sym] = close
syms = set()
for line in open(csv):
    p = line.strip().split(",")
    if len(p) != 3: continue
    d, s, c = p
    px[d][s] = float(c); syms.add(s)
dates = sorted(d for d in px if start <= d <= end)
syms = sorted(syms)

def mom(i, s):
    di, dl = dates[i], dates[i-L]
    if s in px[di] and s in px[dl] and px[dl][s] > 0:
        return px[di][s]/px[dl][s] - 1
    return None

equity = 1.0
prev_w = defaultdict(float)
rets = []
eq_curve = [1.0]
i = L + LAG
while i + R < len(dates):
    # signal at close[i-LAG]; rank
    si = i - LAG
    scored = [(s, mom(si, s)) for s in syms]
    scored = [(s, m) for s, m in scored if m is not None]
    if len(scored) < 2*K if mode == "ls" else len(scored) < K:
        i += R; continue
    scored.sort(key=lambda x: x[1], reverse=True)
    longs = [s for s, _ in scored[:K]]
    shorts = [s for s, _ in scored[-K:]] if mode == "ls" else []
    w = defaultdict(float)
    for s in longs:  w[s] += 1.0/K
    for s in shorts: w[s] -= 1.0/K
    # holding return close[i] -> close[i+R]
    pr = 0.0
    for s in set(longs) | set(shorts):
        d0, d1 = dates[i], dates[i+R]
        if s in px[d0] and s in px[d1] and px[d0][s] > 0:
            pr += w[s] * (px[d1][s]/px[d0][s] - 1)
    # turnover cost
    turn = sum(abs(w[s] - prev_w.get(s, 0.0)) for s in set(w) | set(prev_w))
    net = pr - turn*cost
    equity *= (1 + net)
    rets.append(net); eq_curve.append(equity)
    prev_w = w
    i += R

if not rets:
    print("no trades"); sys.exit()
n = len(rets)
ppy = 365.0/R
mean = sum(rets)/n
var = sum((x-mean)**2 for x in rets)/n
sd = math.sqrt(var)
sharpe = (mean/sd*math.sqrt(ppy)) if sd > 0 else 0
pos = sum(x for x in rets if x > 0); neg = -sum(x for x in rets if x < 0)
pf = pos/neg if neg > 0 else float('inf')
wr = sum(1 for x in rets if x > 0)/n*100
peak = -1e9; mdd = 0
for e in eq_curve:
    peak = max(peak, e); mdd = max(mdd, (peak-e)/peak)
yrs = (dates[-1] > dates[0]) and ((len(dates))/365.0) or 1
cagr = (equity**(1/max(yrs,0.1)) - 1)*100
print(f"  L{L} R{R} K{K} {mode} cost{cost} {dates[0]}..{dates[-1]} | "
      f"ret {(equity-1)*100:6.1f}% CAGR {cagr:5.1f}% Sharpe {sharpe:5.2f} "
      f"PF {pf:4.2f} WR {wr:4.1f}% maxDD {mdd*100:4.1f}% rebals {n}")
