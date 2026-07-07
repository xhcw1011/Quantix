#!/usr/bin/env python3
"""截面 Funding 因子验证:跨 regime OOS + 成本压力 + 参数稳健 + 衰减(严格 PIT,含 funding 损益)。

结论到目前:momentum 因子近乎死;combined 被 momentum 拖累;真货 = funding 因子单独
(多低/负费率、空高正费率,dollar-neutral)。这里把 funding 因子往死里测:换成本、换参数、
看近期是否衰减——最容易塌的先测。数据拉一次,内存里扫。
"""
import sys
sys.path.insert(0, "scripts")
import statistics
from collections import defaultdict
from datetime import datetime, timezone

from breakout_score import get, FAPI

UNIVERSE = ["BTCUSDT", "ETHUSDT", "SOLUSDT", "BNBUSDT", "XRPUSDT", "DOGEUSDT", "ADAUSDT",
            "AVAXUSDT", "LINKUSDT", "DOTUSDT", "TRXUSDT", "LTCUSDT", "BCHUSDT", "ATOMUSDT",
            "UNIUSDT", "ETCUSDT", "XLMUSDT", "NEARUSDT", "APTUSDT", "FILUSDT", "INJUSDT",
            "OPUSDT", "ARBUSDT", "SUIUSDT", "TIAUSDT", "SEIUSDT", "RUNEUSDT", "AAVEUSDT",
            "LDOUSDT", "WLDUSDT",
            # 扩到 ~50:更多流动中大盘(稳健性)
            "ALGOUSDT", "VETUSDT", "ICPUSDT", "HBARUSDT", "SANDUSDT", "MANAUSDT", "AXSUSDT",
            "GALAUSDT", "CHZUSDT", "EOSUSDT", "EGLDUSDT", "THETAUSDT", "GRTUSDT", "IMXUSDT",
            "STXUSDT", "ORDIUSDT", "PYTHUSDT", "JUPUSDT", "DYDXUSDT", "CRVUSDT"]
FUND_START = "2024-09-01"
REGIMES = [("牛", "2024-11-01", "2025-02-15"), ("25中", "2025-02-15", "2025-10-01"),
           ("26跌", "2025-10-01", "2026-12-31")]


def daykey(ms):
    return datetime.fromtimestamp(ms / 1000, tz=timezone.utc).strftime("%Y-%m-%d")


def to_ms(d):
    return int(datetime.strptime(d, "%Y-%m-%d").replace(tzinfo=timezone.utc).timestamp() * 1000)


def fetch_daily(sym):
    kl = get(f"{FAPI}/fapi/v1/klines?symbol={sym}&interval=1d&limit=1500")
    return ({daykey(k[0]): float(k[4]) for k in kl}, {daykey(k[0]): float(k[7]) for k in kl})


def fetch_funding_daily(sym):
    fd = defaultdict(float)
    cur = to_ms(FUND_START)
    for _ in range(8):
        fr = get(f"{FAPI}/fapi/v1/fundingRate?symbol={sym}&startTime={cur}&limit=1000")
        if not fr:
            break
        for x in fr:
            fd[daykey(x["fundingTime"])] += float(x["fundingRate"])
        if len(fr) < 1000:
            break
        cur = fr[-1]["fundingTime"] + 1
    return fd


def zscores(d):
    v = list(d.values())
    if len(v) < 2:
        return {k: 0.0 for k in d}
    m, s = statistics.mean(v), statistics.pstdev(v) or 1.0
    return {k: (x - m) / s for k, x in d.items()}


def simulate(px, qv, fund, dates, factor, L, W, K, REB, COST, univ=None, LAG=1):
    univ = univ or UNIVERSE

    def ret(i, s, n):
        if i - n < 0:
            return None
        di, dl = dates[i], dates[i - n]
        return px[s][di] / px[s][dl] - 1 if di in px.get(s, {}) and dl in px.get(s, {}) and px[s][dl] > 0 else None

    def tfund(i, s, n):
        return sum(fund.get(s, {}).get(dates[j], 0.0) for j in range(max(0, i - n + 1), i + 1))

    def ffund(i, s, n):
        return sum(fund.get(s, {}).get(dates[j], 0.0) for j in range(i + 1, min(i + n + 1, len(dates))))

    def tvol(i, s, n=30):
        vs = [qv[s][dates[j]] for j in range(max(0, i - n + 1), i + 1) if dates[j] in qv.get(s, {})]
        return sum(vs) / len(vs) if vs else 0.0

    def hasf(i, s):
        f = fund.get(s, {})
        return f and min(f) <= dates[max(0, i - W)]

    eq, step_dates, pcomp, fcomp, prev, i = [1.0], [], [], [], {}, L + LAG
    while i + REB < len(dates):
        si = i - LAG
        cands = [s for s in univ if ret(si, s, L) is not None and tvol(si, s) > 0 and hasf(si, s)]
        if len(cands) < 2 * K + 2:
            i += REB
            continue
        zm = zscores({s: ret(si, s, L) for s in cands})
        zf = zscores({s: tfund(si, s, W) for s in cands})
        sc = ({s: zm[s] for s in cands} if factor == "momentum"
              else {s: -zf[s] for s in cands} if factor == "funding"
              else {s: zm[s] - zf[s] for s in cands})
        ranked = sorted(cands, key=lambda s: sc[s], reverse=True)
        pos = {s: 1 for s in ranked[:K]}
        pos.update({s: -1 for s in ranked[-K:]})
        pc = sum(side * (ret(i + REB, s, REB) or 0.0) for s, side in pos.items()) / (2 * K)   # 价格(均值回归)
        fc = sum(-side * ffund(i, s, REB) for s, side in pos.items()) / (2 * K)                # carry(收/付 funding)
        cost = sum(abs(pos.get(s, 0) - prev.get(s, 0)) for s in set(pos) | set(prev)) / (2 * K) * COST
        eq.append(eq[-1] * (1 + pc + fc - cost))
        pcomp.append(pc)
        fcomp.append(fc)
        step_dates.append(dates[i])
        prev = pos
        i += REB
    return eq, step_dates, pcomp, fcomp


def perf(eq, step_dates, REB):
    if len(eq) < 3:
        return 0, 0, 0
    rets = [eq[j] / eq[j - 1] - 1 for j in range(1, len(eq))]
    yrs = (datetime.strptime(step_dates[-1], "%Y-%m-%d") - datetime.strptime(step_dates[0], "%Y-%m-%d")).days / 365
    ann = (eq[-1] ** (1 / yrs) - 1) if yrs > 0 and eq[-1] > 0 else 0
    sh = (statistics.mean(rets) / (statistics.pstdev(rets) or 1)) * ((365 / REB) ** 0.5)
    return eq[-1] - 1, ann, sh


def wret(eq, step_dates, a, b):
    idx = [j for j, d in enumerate(step_dates) if a <= d < b]
    return eq[idx[-1]] / eq[idx[0]] - 1 if len(idx) >= 2 else None


def main():
    px, qv, fund = {}, {}, {}
    for s in UNIVERSE:
        try:
            px[s], qv[s] = fetch_daily(s)
            fund[s] = fetch_funding_daily(s)
        except Exception as e:
            print(f"  skip {s}: {e}")
    dates = sorted({d for s in px for d in px[s]})

    print(f"### 基线 3 因子(全 {len(UNIVERSE)} 币,L14 W14 K5 REB3 费10bp)")
    for f in ("momentum", "funding", "combined"):
        eq, sd, _, _ = simulate(px, qv, fund, dates, f, 14, 14, 5, 3, 0.0010)
        cum, ann, sh = perf(eq, sd, 3)
        segs = "/".join(f"{(wret(eq,sd,a,z) or 0)*100:+.0f}" for _, a, z in REGIMES)
        print(f"  {f:<10} 累计{cum*100:>6.1f}% 年化{ann*100:>6.1f}% Sharpe{sh:>5.2f}  regime牛/中/跌 {segs}")

    # ① 拆解:funding 因子的收益里,carry(收/付 funding) vs 价格均值回归 各占多少(费前)
    print("\n### ① 拆解 funding 因子:carry vs 价格均值回归(费前,累计贡献 %)")
    eq, sd, pc, fc = simulate(px, qv, fund, dates, "funding", 14, 14, 5, 3, 0.0)
    def seg_sum(comp, a, b):
        return sum(c for c, d in zip(comp, sd) if a <= d < b) * 100
    print(f"  {'':<8}{'全期':>9}{'牛':>9}{'25中':>9}{'26跌':>9}")
    print(f"  {'carry':<8}{sum(fc)*100:>8.0f}%" + "".join(f"{seg_sum(fc,a,z):>8.0f}%" for _, a, z in REGIMES))
    print(f"  {'均值回归':<6}{sum(pc)*100:>8.0f}%" + "".join(f"{seg_sum(pc,a,z):>8.0f}%" for _, a, z in REGIMES))
    print("  (carry=结构性现金流、稳;均值回归=反拥挤,regime 依赖、可能被套利掉)")

    # ② universe 稳健:换 universe 大小 / 子集
    print("\n### ② universe 稳健(funding,W14 K5 REB3 费20bp)")
    for label, u in [("前15大", UNIVERSE[:15]), ("前30", UNIVERSE[:30]), (f"全{len(UNIVERSE)}", UNIVERSE),
                     ("剔前10(去BTC等)", UNIVERSE[10:])]:
        eq, sd, _, _ = simulate(px, qv, fund, dates, "funding", 14, W_default(u), 5, 3, 0.0020, univ=u)
        cum, ann, sh = perf(eq, sd, 3)
        print(f"  {label:<14} 年化{ann*100:>6.1f}%  Sharpe{sh:>5.2f}  累计{cum*100:>6.1f}%")

    print("\n### 成本压力(全 universe funding, W14 K5 REB3)")
    for c in (10, 20, 30, 50):
        eq, sd, _, _ = simulate(px, qv, fund, dates, "funding", 14, 14, 5, 3, c / 1e4)
        _, ann, sh = perf(eq, sd, 3)
        print(f"  费{c:>2}bp/边  年化{ann*100:>6.1f}%  Sharpe{sh:>5.2f}")


def W_default(u):
    return 14


if __name__ == "__main__":
    main()
