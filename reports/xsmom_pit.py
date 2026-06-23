#!/usr/bin/env python3
# Cross-sectional momentum with POINT-IN-TIME tradeable universe (stdlib only).
# Each rebalance: universe = top-N coins by trailing 30d dollar volume that ALSO
# have L+ days of history (so newly-listed coins enter only once tradeable, and
# illiquid junk is excluded) -> rank THOSE by lookback return -> long top-K /
# short bottom-K. This removes the hand-picked-survivor bias of the 15-coin run.
#
# usage: xsmom_pit.py <csv> <L> <R> <K> <ls|lo> <cost> <topN> [start] [end]
#   csv columns: date,symbol,close,quote_volume   ;  topN=0 -> use all coins
import sys, math
from collections import defaultdict

csv = sys.argv[1]
L, R, K = int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4])
mode, cost, topN = sys.argv[5], float(sys.argv[6]), int(sys.argv[7])
start = sys.argv[8] if len(sys.argv) > 8 else "0000"
end   = sys.argv[9] if len(sys.argv) > 9 else "9999"
LAG, VOLWIN = 1, 30

px = defaultdict(dict); qv = defaultdict(dict); syms = set()
for line in open(csv):
    p = line.strip().split(",")
    if len(p) != 4: continue
    d, s, c, v = p
    try: px[d][s] = float(c); qv[d][s] = float(v)
    except: continue
    syms.add(s)
dates = sorted(d for d in px if start <= d <= end)
syms = sorted(syms)

def ret(i, s):
    di, dl = dates[i], dates[i-L]
    if s in px[di] and s in px[dl] and px[dl][s] > 0:
        return px[di][s]/px[dl][s] - 1
    return None

def trail_vol(i, s):           # avg dollar volume over last VOLWIN days ending at i
    tot, n = 0.0, 0
    for j in range(max(0, i-VOLWIN+1), i+1):
        if s in qv[dates[j]]:
            tot += qv[dates[j]][s]; n += 1
    return tot/n if n else 0.0

equity = 1.0; prev_w = defaultdict(float); rets = []; eq = [1.0]
i = L + LAG
while i + R < len(dates):
    si = i - LAG
    cands = [s for s in syms if ret(si, s) is not None]      # has momentum + listed
    if topN > 0:
        cands.sort(key=lambda s: trail_vol(si, s), reverse=True)
        cands = cands[:topN]                                  # point-in-time liquid universe
    need = 2*K if mode == "ls" else K
    if len(cands) < need:
        i += R; continue
    scored = sorted(((s, ret(si, s)) for s in cands), key=lambda x: x[1], reverse=True)
    longs = [s for s, _ in scored[:K]] if mode != "so" else []
    shorts = [s for s, _ in scored[-K:]] if mode in ("ls", "so") else []
    w = defaultdict(float)
    for s in longs:  w[s] += 1.0/K
    for s in shorts: w[s] -= 1.0/K
    pr = 0.0
    for s in set(longs) | set(shorts):
        d0, d1 = dates[i], dates[i+R]
        if s in px[d0] and s in px[d1] and px[d0][s] > 0:
            pr += w[s] * (px[d1][s]/px[d0][s] - 1)
    turn = sum(abs(w[s]-prev_w.get(s, 0.0)) for s in set(w) | set(prev_w))
    net = pr - turn*cost
    net = max(net, -1.0)            # can't lose more than 100% — you'd be liquidated first
    equity *= (1+net); rets.append(net); eq.append(equity); prev_w = w
    if equity <= 1e-9:             # blown up — account dead, stop
        eq.append(0.0); break
    i += R

if not rets:
    print("  no trades"); sys.exit()
n = len(rets); ppy = 365.0/R
mean = sum(rets)/n; sd = math.sqrt(sum((x-mean)**2 for x in rets)/n)
sharpe = mean/sd*math.sqrt(ppy) if sd > 0 else 0
pos = sum(x for x in rets if x > 0); neg = -sum(x for x in rets if x < 0)
pf = pos/neg if neg > 0 else float('inf')
wr = sum(1 for x in rets if x > 0)/n*100
peak = -1e9; mdd = 0
for e in eq:
    peak = max(peak, e); mdd = max(mdd, (peak-e)/peak)
print(f"  L{L} R{R} K{K} top{topN} {mode} cost{cost} {dates[0]}..{dates[-1]} | "
      f"ret {(equity-1)*100:7.1f}% Sharpe {sharpe:5.2f} PF {pf:4.2f} "
      f"WR {wr:4.1f}% maxDD {mdd*100:4.1f}% rebals {n}")
