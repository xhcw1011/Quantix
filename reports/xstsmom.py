#!/usr/bin/env python3
# TIME-SERIES momentum (TSMOM), stdlib only. Each asset independently: long if its
# OWN L-day return > 0, else cash (lo) or short (ls). Equal weight 1/N_universe per
# held asset, so exposure scales with breadth -> auto de-risk in bear markets.
# Point-in-time liquid universe (top-N by trailing 30d $vol, only listed coins).
#
# usage: xstsmom.py <csv> <L> <R> <lo|ls> <cost> <topN> [start] [end]
#   csv columns: date,symbol,close,quote_volume
import sys, math
from collections import defaultdict

csv = sys.argv[1]
L, R = int(sys.argv[2]), int(sys.argv[3])
mode, cost, topN = sys.argv[4], float(sys.argv[5]), int(sys.argv[6])
start = sys.argv[7] if len(sys.argv) > 7 else "0000"
end   = sys.argv[8] if len(sys.argv) > 8 else "9999"
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

def trail_vol(i, s):
    tot, n = 0.0, 0
    for j in range(max(0, i-VOLWIN+1), i+1):
        if s in qv[dates[j]]: tot += qv[dates[j]][s]; n += 1
    return tot/n if n else 0.0

equity = 1.0; prev_w = defaultdict(float); rets = []; eq = [1.0]; expo = []
i = L + LAG
while i + R < len(dates):
    si = i - LAG
    cands = [s for s in syms if ret(si, s) is not None]
    if topN > 0:
        cands.sort(key=lambda s: trail_vol(si, s), reverse=True)
        cands = cands[:topN]
    N = len(cands)
    if N == 0: i += R; continue
    w = defaultdict(float)
    for s in cands:
        m = ret(si, s)
        if m > 0:   w[s] = 1.0/N            # long, weight scaled by breadth
        elif mode == "ls" and m < 0: w[s] = -1.0/N
    expo.append(sum(w.values()))            # net long exposure (0..1 for lo)
    pr = 0.0
    for s in w:
        d0, d1 = dates[i], dates[i+R]
        if s in px[d0] and s in px[d1] and px[d0][s] > 0:
            pr += w[s]*(px[d1][s]/px[d0][s]-1)
    turn = sum(abs(w[s]-prev_w.get(s, 0.0)) for s in set(w)|set(prev_w))
    net = max(pr - turn*cost, -1.0)
    equity *= (1+net); rets.append(net); eq.append(equity); prev_w = w
    if equity <= 1e-9: break
    i += R

if not rets: print("  no trades"); sys.exit()
n = len(rets); ppy = 365.0/R
mean = sum(rets)/n; sd = math.sqrt(sum((x-mean)**2 for x in rets)/n)
sharpe = mean/sd*math.sqrt(ppy) if sd > 0 else 0
pos = sum(x for x in rets if x > 0); neg = -sum(x for x in rets if x < 0)
pf = pos/neg if neg > 0 else float('inf')
wr = sum(1 for x in rets if x > 0)/n*100
peak = -1e9; mdd = 0
for e in eq: peak = max(peak, e); mdd = max(mdd, (peak-e)/peak)
print(f"  TSMOM L{L} R{R} top{topN} {mode} cost{cost} {dates[0]}..{dates[-1]} | "
      f"ret {(equity-1)*100:7.1f}% Sharpe {sharpe:5.2f} PF {pf:4.2f} WR {wr:4.1f}% "
      f"maxDD {mdd*100:4.1f}% avgExpo {sum(expo)/len(expo)*100:3.0f}% rebals {n}")
