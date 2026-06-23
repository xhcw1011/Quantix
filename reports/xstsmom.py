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
volscale = int(sys.argv[7]) if len(sys.argv) > 7 else 0   # 1 = inverse-vol weighting
start = sys.argv[8] if len(sys.argv) > 8 else "0000"
end   = sys.argv[9] if len(sys.argv) > 9 else "9999"
LAG, VOLWIN, VOLLOOK = 1, 30, 30

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

def ret_vol(i, s):                 # stdev of daily returns over last VOLLOOK days
    rs = []
    for j in range(max(1, i-VOLLOOK+1), i+1):
        a, b = dates[j], dates[j-1]
        if s in px[a] and s in px[b] and px[b][s] > 0:
            rs.append(px[a][s]/px[b][s]-1)
    if len(rs) < 5: return None
    m = sum(rs)/len(rs)
    return math.sqrt(sum((x-m)**2 for x in rs)/len(rs)) or None

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
    sig = {}
    for s in cands:
        m = ret(si, s)
        if mode in ("lo", "ls") and m > 0:   sig[s] = 1
        elif mode in ("ls", "so") and m < 0: sig[s] = -1
    w = defaultdict(float)
    gross = len(sig)/N                       # preserve breadth-scaling (de-risk in bear)
    if volscale:                             # distribute gross inverse-to-volatility
        raw = {s: 1.0/ret_vol(si, s) for s in sig if ret_vol(si, s)}
        denom = sum(raw.values())
        if denom > 0:
            for s in raw: w[s] = sig[s]*gross*raw[s]/denom
    else:                                    # equal weight
        for s in sig: w[s] = sig[s]*(1.0/N)
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
