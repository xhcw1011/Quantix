#!/usr/bin/env python3
# Put the "solid" market-neutral momentum (reports/mn_momentum.py) through the SAME
# rigor battery the funding factor passed: cross-regime OOS, cost stress, param
# robustness, universe robustness, year-by-year decay. Data loaded once, swept in-mem.
# funding's bar: positive EVERY regime, Sharpe ~1.5, cost survives to 30bp, no decay.
import math
from collections import defaultdict, deque

CSV = "reports/daily_full.csv"
LAG, VOLWIN, VOLLOOK, VTWIN = 1, 30, 30, 10

px = defaultdict(dict); qv = defaultdict(dict); SYMS = set()
for line in open(CSV):
    p = line.strip().split(",")
    if len(p) != 4: continue
    d, s, c, v = p
    try: px[d][s] = float(c); qv[d][s] = float(v)
    except: continue
    SYMS.add(s)
ALLDATES = sorted(px)
SYMS = sorted(SYMS)
print(f"# loaded {len(ALLDATES)} dates {ALLDATES[0]}..{ALLDATES[-1]}, {len(SYMS)} symbols")


def sim(L, R, K, topN, targetVol, maxLev, cost, start="0000", end="9999"):
    dates = [d for d in ALLDATES if start <= d <= end]
    idx = {d: i for i, d in enumerate(dates)}

    def ret(i, s):
        di, dl = dates[i], dates[i - L]
        if s in px[di] and s in px[dl] and px[dl][s] > 0: return px[di][s] / px[dl][s] - 1
        return None

    def tvol(i, s):
        t = 0.0; n = 0
        for j in range(max(0, i - VOLWIN + 1), i + 1):
            if s in qv[dates[j]]: t += qv[dates[j]][s]; n += 1
        return t / n if n else 0.0

    def rvol(i, s):
        rs = []
        for j in range(max(1, i - VOLLOOK + 1), i + 1):
            a, b = dates[j], dates[j - 1]
            if s in px[a] and s in px[b] and px[b][s] > 0: rs.append(px[a][s] / px[b][s] - 1)
        if len(rs) < 5: return None
        m = sum(rs) / len(rs); return math.sqrt(sum((x - m) ** 2 for x in rs) / len(rs)) or None

    ppy = 365.0 / R
    tgt_period = targetVol / math.sqrt(ppy)
    equity = 1.0; actual_prev = defaultdict(float); rets = []; eq = [1.0]
    base_hist = deque(maxlen=VTWIN); levs = []
    i = L + LAG
    while i + R < len(dates):
        si = i - LAG
        cands = [s for s in SYMS if ret(si, s) is not None and rvol(si, s)]
        if topN > 0:
            cands.sort(key=lambda s: tvol(si, s), reverse=True); cands = cands[:topN]
        if len(cands) < 2 * K: i += R; continue
        sc = sorted(((s, ret(si, s)) for s in cands), key=lambda x: x[1], reverse=True)
        longs, shorts = [s for s, _ in sc[:K]], [s for s, _ in sc[-K:]]
        wraw = defaultdict(float)
        for leg, sign in ((longs, 1), (shorts, -1)):
            inv = {s: 1.0 / rvol(si, s) for s in leg}
            z = sum(inv.values())
            for s in leg: wraw[s] = sign * 0.5 * inv[s] / z
        base = 0.0
        for s in wraw:
            d0, d1 = dates[i], dates[i + R]
            if s in px[d0] and s in px[d1] and px[d0][s] > 0: base += wraw[s] * (px[d1][s] / px[d0][s] - 1)
        if len(base_hist) >= VTWIN // 2:
            m = sum(base_hist) / len(base_hist)
            rv = math.sqrt(sum((x - m) ** 2 for x in base_hist) / len(base_hist))
            lev = min(maxLev, tgt_period / rv) if rv > 0 else 1.0
        else:
            lev = 1.0
        levs.append(lev)
        actual = {s: lev * wraw[s] for s in wraw}
        turn = sum(abs(actual.get(s, 0.0) - actual_prev.get(s, 0.0)) for s in set(actual) | set(actual_prev))
        net = max(lev * base - turn * cost, -1.0)
        equity *= (1 + net); rets.append(net); eq.append(equity)
        base_hist.append(base); actual_prev = actual
        if equity <= 1e-9: break
        i += R
    if not rets: return None
    n = len(rets); mean = sum(rets) / n; sd = math.sqrt(sum((x - mean) ** 2 for x in rets) / n)
    sharpe = mean / sd * math.sqrt(ppy) if sd > 0 else 0
    pos = sum(x for x in rets if x > 0); neg = -sum(x for x in rets if x < 0)
    pf = pos / neg if neg > 0 else float('inf')
    peak = -1e9; mdd = 0
    for e in eq:
        peak = max(peak, e); mdd = max(mdd, (peak - e) / peak)
    return dict(ret=(equity - 1) * 100, sharpe=sharpe, pf=pf, mdd=mdd * 100,
                avgLev=sum(levs) / len(levs), n=n)


def show(tag, r):
    if r is None: print(f"  {tag:<26} no trades"); return
    print(f"  {tag:<26} ret {r['ret']:7.1f}%  Sharpe {r['sharpe']:5.2f}  PF {r['pf']:4.2f}  "
          f"maxDD {r['mdd']:4.1f}%  avgLev {r['avgLev']:.2f}  n {r['n']}")


# baseline = the "solid" config
BL = dict(L=60, R=7, K=5, topN=50, targetVol=0.15, maxLev=3, cost=0.0010)
print(f"\n### 基线(L60 R7 K5 top50 tgt15% maxL3 费10bp)")
show("full 2023-26", sim(**BL))

print("\n### ① 跨 regime OOS  (funding 过关=每段都正)")
REGIMES = [("2023", "2023-01-01", "2023-12-31"), ("2024", "2024-01-01", "2024-12-31"),
           ("牛 24Q4-25Q1", "2024-11-01", "2025-02-15"), ("25中", "2025-02-15", "2025-10-01"),
           ("26跌", "2025-10-01", "2026-12-31")]
for name, a, z in REGIMES:
    show(name, sim(**BL, start=a, end=z))

print("\n### ② 逐年衰减  (funding 过关=不衰减)")
for y in ("2023", "2024", "2025", "2026"):
    show(y, sim(**{**BL}, start=f"{y}-01-01", end=f"{y}-12-31"))

print("\n### ③ 成本压力  (funding 过关=扛到 30bp)")
for c in (0.0005, 0.0010, 0.0020, 0.0030, 0.0050):
    show(f"cost {c*1e4:.0f}bp", sim(**{**BL, "cost": c}))

print("\n### ④ 参数稳健 L(动量窗口)")
for L in (30, 45, 60, 90, 120):
    show(f"L{L}", sim(**{**BL, "L": L}))
print("### 参数稳健 K(每边仓数)")
for K in (3, 5, 8):
    show(f"K{K}", sim(**{**BL, "K": K}))

print("\n### ⑤ universe 稳健 topN")
for t in (15, 30, 50, 100, 0):
    show(f"top{t if t else 'ALL'}", sim(**{**BL, "topN": t}))
