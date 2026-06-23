#!/usr/bin/env python3
# Market-neutral momentum, "made solid" (stdlib only):
#   - point-in-time tradeable universe (top-N by trailing 30d $vol, only listed coins)
#   - cross-sectional rank by L-day return -> long top-K, short bottom-K
#   - DOLLAR-NEUTRAL + RISK-PARITY: each leg inverse-vol weighted, long gross = short
#     gross = 0.5 (net ~0, gross 1 before leverage)
#   - PORTFOLIO VOL-TARGETING: lever the whole book to a constant target vol using
#     trailing realized vol of the un-levered strategy (no lookahead), capped at maxLev
#   - turnover costs on the actual (levered) weight changes; liquidation floor
#
# usage: mn_momentum.py <csv> <L> <R> <K> <topN> <targetVolAnn> <maxLev> <cost> [start] [end]
import sys, math
from collections import defaultdict, deque

csv = sys.argv[1]
L, R, K, topN = int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4]), int(sys.argv[5])
targetVol, maxLev, cost = float(sys.argv[6]), float(sys.argv[7]), float(sys.argv[8])
start = sys.argv[9] if len(sys.argv) > 9 else "0000"
end   = sys.argv[10] if len(sys.argv) > 10 else "9999"
LAG, VOLWIN, VOLLOOK, VTWIN = 1, 30, 30, 10   # VTWIN = lookback (periods) for vol-target

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
    if s in px[di] and s in px[dl] and px[dl][s] > 0: return px[di][s]/px[dl][s]-1
    return None
def tvol(i, s):
    t = 0.0; n = 0
    for j in range(max(0, i-VOLWIN+1), i+1):
        if s in qv[dates[j]]: t += qv[dates[j]][s]; n += 1
    return t/n if n else 0.0
def rvol(i, s):
    rs = []
    for j in range(max(1, i-VOLLOOK+1), i+1):
        a, b = dates[j], dates[j-1]
        if s in px[a] and s in px[b] and px[b][s] > 0: rs.append(px[a][s]/px[b][s]-1)
    if len(rs) < 5: return None
    m = sum(rs)/len(rs); return math.sqrt(sum((x-m)**2 for x in rs)/len(rs)) or None

ppy = 365.0/R
tgt_period = targetVol/math.sqrt(ppy)
equity = 1.0; actual_prev = defaultdict(float); rets = []; eq = [1.0]
base_hist = deque(maxlen=VTWIN); levs = []
i = L + LAG
while i + R < len(dates):
    si = i - LAG
    cands = [s for s in syms if ret(si, s) is not None and rvol(si, s)]
    if topN > 0:
        cands.sort(key=lambda s: tvol(si, s), reverse=True); cands = cands[:topN]
    if len(cands) < 2*K: i += R; continue
    sc = sorted(((s, ret(si, s)) for s in cands), key=lambda x: x[1], reverse=True)
    longs, shorts = [s for s, _ in sc[:K]], [s for s, _ in sc[-K:]]
    # risk-parity within each leg, dollar-neutral (each leg gross 0.5)
    wraw = defaultdict(float)
    for leg, sign in ((longs, 1), (shorts, -1)):
        inv = {s: 1.0/rvol(si, s) for s in leg}
        z = sum(inv.values())
        for s in leg: wraw[s] = sign*0.5*inv[s]/z
    # un-levered portfolio return this period
    base = 0.0
    for s in wraw:
        d0, d1 = dates[i], dates[i+R]
        if s in px[d0] and s in px[d1] and px[d0][s] > 0: base += wraw[s]*(px[d1][s]/px[d0][s]-1)
    # vol-target leverage from trailing un-levered vol (no lookahead)
    if len(base_hist) >= VTWIN//2:
        m = sum(base_hist)/len(base_hist)
        rv = math.sqrt(sum((x-m)**2 for x in base_hist)/len(base_hist))
        lev = min(maxLev, tgt_period/rv) if rv > 0 else 1.0
    else:
        lev = 1.0
    levs.append(lev)
    actual = {s: lev*wraw[s] for s in wraw}
    turn = sum(abs(actual.get(s, 0.0)-actual_prev.get(s, 0.0)) for s in set(actual)|set(actual_prev))
    net = max(lev*base - turn*cost, -1.0)
    equity *= (1+net); rets.append(net); eq.append(equity)
    base_hist.append(base); actual_prev = actual
    if equity <= 1e-9: break
    i += R

if not rets: print("  no trades"); sys.exit()
n = len(rets); mean = sum(rets)/n; sd = math.sqrt(sum((x-mean)**2 for x in rets)/n)
sharpe = mean/sd*math.sqrt(ppy) if sd > 0 else 0
pos = sum(x for x in rets if x > 0); neg = -sum(x for x in rets if x < 0)
pf = pos/neg if neg > 0 else float('inf')
wr = sum(1 for x in rets if x > 0)/n*100
peak = -1e9; mdd = 0
for e in eq: peak = max(peak, e); mdd = max(mdd, (peak-e)/peak)
print(f"  L{L} R{R} K{K} top{topN} tgt{targetVol} maxL{maxLev} {dates[0]}..{dates[-1]} | "
      f"ret {(equity-1)*100:7.1f}% Sharpe {sharpe:5.2f} PF {pf:4.2f} WR {wr:4.1f}% "
      f"maxDD {mdd*100:4.1f}% avgLev {sum(levs)/len(levs):.2f} rebals {n}")
