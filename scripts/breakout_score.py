#!/usr/bin/env python3
"""breakout_score — Q1:区间突破概率(规则版打分 + 预测力验证).

思路(用户框架):不判断"现在是不是震荡",而是估计"未来容易不容易突破"。
5 个**领先**信号,全部 rolling percentile(抗过拟合):
  ① ATR 压缩  ② OI 增加  ③ Funding 极端  ④ 量价背离  ⑤ 区间时长
加权 → 0..1 突破概率分。高分 → 别 Grid(趋势准备);低分 → 区间平静,Grid 可开。

两种模式:
  # 当前打分:
  python3 scripts/breakout_score.py --symbol BTCUSDT --interval 1h
  # 验证预测力(标注历史突破,看分数高时是不是真的更容易突破):
  python3 scripts/breakout_score.py --symbol BTCUSDT --interval 1h --validate

打分是**因果**的(percentile 只用过去);突破标签用未来 H 根(仅作验证目标)。
只用 Binance 公开 fapi,无需 key。
"""
import argparse
import json
import time
import urllib.request
from datetime import datetime, timezone

FAPI = "https://fapi.binance.com"
# 权重:OOS 验证后锁定——只有 区间时长 + 量价背离 在两个币、样本内外全部保持;
# ATR(BTC 上过拟合)、OI/Funding(没撑住/数据缺)剔除(权重 0,仍算子分供诊断)。
WEIGHTS = {"atr": 0.0, "oi": 0.0, "funding": 0.0, "voldiv": 0.5, "duration": 0.5}


def get(url, retries=3):
    last = None
    for a in range(retries):
        try:
            with urllib.request.urlopen(url, timeout=20) as r:
                return json.loads(r.read())
        except Exception as e:
            last = e
            time.sleep(1.5 * (a + 1))
    raise last


def fetch_klines(sym, interval, limit):
    d = get(f"{FAPI}/fapi/v1/klines?symbol={sym}&interval={interval}&limit={limit}")
    return [{"t": k[0], "h": float(k[2]), "l": float(k[3]), "c": float(k[4]), "v": float(k[7])} for k in d]


def fetch_oi(sym, period, limit=500):
    try:
        d = get(f"{FAPI}/futures/data/openInterestHist?symbol={sym}&period={period}&limit={limit}")
        return sorted((int(x["timestamp"]), float(x["sumOpenInterest"])) for x in d)
    except Exception:
        return []


def fetch_funding(sym, limit=1000):
    try:
        d = get(f"{FAPI}/fapi/v1/fundingRate?symbol={sym}&limit={limit}")
        return sorted((int(x["fundingTime"]), float(x["fundingRate"])) for x in d)
    except Exception:
        return []


def interval_hours(iv):
    return int(iv[:-1]) * {"m": 1 / 60, "h": 1, "d": 24}[iv[-1]]


def atr_series(kl, n=14):
    tr = [kl[0]["h"] - kl[0]["l"]]
    for i in range(1, len(kl)):
        pc = kl[i - 1]["c"]
        tr.append(max(kl[i]["h"] - kl[i]["l"], abs(kl[i]["h"] - pc), abs(kl[i]["l"] - pc)))
    return [None if i < n - 1 else sum(tr[i - n + 1:i + 1]) / n for i in range(len(kl))]


def align(sorted_pairs, kl):
    """两指针:每根 K 线取最近一个 ts<=bar 的值(前向填充)。O(n+m)。"""
    out = [None] * len(kl)
    j, last = 0, None
    for i, k in enumerate(kl):
        while j < len(sorted_pairs) and sorted_pairs[j][0] <= k["t"]:
            last = sorted_pairs[j][1]
            j += 1
        out[i] = last
    return out


def pctile(value, sample):
    s = [x for x in sample if x is not None]
    if value is None or not s:
        return 0.5
    return sum(1 for x in s if x <= value) / len(s)


def compute_signals(kl, oi_pairs, fund_pairs, W, bars_per_day):
    """逐根因果计算 5 个子分 + 总分。返回并行数组。"""
    n = len(kl)
    close = [k["c"] for k in kl]
    vol = [k["v"] for k in kl]
    atr = atr_series(kl, 14)
    oi = align(oi_pairs, kl) if oi_pairs else [None] * n
    fabs = [abs(x) if x is not None else None for x in align(fund_pairs, kl)] if fund_pairs else [None] * n

    oi_chg = [None] * n
    for i in range(20, n):
        a, b = oi[i - 20], oi[i]
        if a and b and a > 0:
            oi_chg[i] = b / a - 1

    K = 8
    rng = [None] * n
    for i in range(K, n):
        w = close[i - K:i + 1]
        rng[i] = (max(w) - min(w)) / (sum(w) / len(w))

    def w_of(series, i):
        return series[max(0, i - W + 1):i + 1]

    rng_p = [pctile(rng[i], w_of(rng, i)) if rng[i] is not None else None for i in range(n)]

    subs = {k: [None] * n for k in WEIGHTS}
    score = [None] * n
    for i in range(n):
        if atr[i] is None or i < W // 2:
            continue
        subs["atr"][i] = 1 - pctile(atr[i], w_of(atr, i))
        subs["oi"][i] = pctile(oi_chg[i], w_of(oi_chg, i)) if oi_chg[i] is not None else 0.5
        subs["funding"][i] = pctile(fabs[i], w_of(fabs, i)) if fabs[i] is not None else 0.5
        vp = pctile(vol[i], w_of(vol, i))
        rp = rng_p[i] if rng_p[i] is not None else 0.5
        subs["voldiv"][i] = vp * (1 - rp)
        age = 0
        for j in range(i, K, -1):
            if rng_p[j] is not None and rng_p[j] < 0.35:
                age += 1
            else:
                break
        subs["duration"][i] = min(age / (2 * bars_per_day), 1.0)
        score[i] = sum(WEIGHTS[k] * subs[k][i] for k in WEIGHTS)
    return {"score": score, "subs": subs, "atr": atr, "close": close,
            "ctx": {"oi": bool(oi_pairs), "fund": bool(fund_pairs)}}


def buckets(pairs, nb=5):
    """pairs=[(x,label)]. 按 x 排序分 nb 档,返回每档 (x区间, 样本数, 突破率)。"""
    pairs = sorted(pairs)
    m = len(pairs)
    out = []
    for b in range(nb):
        seg = pairs[b * m // nb:(b + 1) * m // nb]
        if seg:
            rate = sum(l for _, l in seg) / len(seg)
            out.append((seg[0][0], seg[-1][0], len(seg), rate))
    return out


def do_validate(sym, interval, oi_period, W, horizon_h, atr_mult):
    ih = interval_hours(interval)
    bpd = 24 / ih
    limit = 1500
    kl = fetch_klines(sym, interval, limit)
    sig = compute_signals(kl, fetch_oi(sym, oi_period), fetch_funding(sym), W, bpd)
    n = len(kl)
    score, atr, close = sig["score"], sig["atr"], sig["close"]
    H = max(1, round(horizon_h / ih))

    # 标注突破:未来 H 根内,|收盘位移| > atr_mult × ATR(i) 即算一次突破
    pairs = []
    persig = {k: [] for k in WEIGHTS}
    for i in range(n):
        if score[i] is None or atr[i] is None or i + H >= n:
            continue
        move = max(abs(close[j] - close[i]) for j in range(i + 1, i + H + 1))
        lab = 1 if move > atr_mult * atr[i] else 0
        pairs.append((score[i], lab))
        for k in WEIGHTS:
            persig[k].append((sig["subs"][k][i], lab))

    if not pairs:
        raise SystemExit("样本不足(数据太少或 horizon 太长)")
    base = sum(l for _, l in pairs) / len(pairs)

    print(f"# {sym} {interval}  验证突破预测力  样本 {len(pairs)} 根  "
          f"覆盖 ~{len(pairs)*ih/24:.0f} 天  ({datetime.now(timezone.utc):%Y-%m-%d %H:%M UTC})")
    print(f"# 突破定义: 未来 {horizon_h}h 内位移 > {atr_mult}×ATR   基准突破率 {base*100:.1f}%")
    miss = [n for n, ok in [("OI", sig["ctx"]["oi"]), ("Funding", sig["ctx"]["fund"])] if not ok]
    if miss:
        print(f"# ⚠ {'/'.join(miss)} 数据拿不到 → 这些信号退成常量,本次验证对它们无效(标 N/A)")
    print("=" * 70)
    print("  总分分档(低→高)  样本  该档后续突破率   vs 基准")
    for lo, hi, cnt, rate in buckets(pairs):
        lift = rate - base
        arrow = "↑" if lift > 0.02 else ("↓" if lift < -0.02 else "·")
        print(f"    [{lo:.2f},{hi:.2f}]   {cnt:>4}   {rate*100:>5.1f}%   {lift*100:>+5.1f}pp {arrow}")
    top = buckets(pairs)[-1][3]
    bot = buckets(pairs)[0][3]
    print("-" * 70)
    print(f"  顶档 vs 底档突破率: {top*100:.1f}% vs {bot*100:.1f}%  "
          f"→ lift {(top-bot)*100:+.1f}pp  {'有预测力 ✓' if top-bot > 0.05 else '预测力弱/无 ✗'}")
    print("=" * 70)
    print("  逐信号预测力(各自 顶档 vs 底档突破率,看谁真有用):")
    for k in WEIGHTS:
        ps = [p for p in persig[k] if p[0] is not None]
        vals = [x for x, _ in ps]
        if len(ps) < 20:
            continue
        if max(vals) - min(vals) < 1e-6:  # 常量=数据缺失/退化,分档是时间巧合,不算数
            print(f"    {k:<9} 常量({vals[0]:.2f}) → N/A(数据缺失,无法判断)")
            continue
        bk = buckets(ps)
        t, b = bk[-1][3], bk[0][3]
        useful = "有用 ✓" if t - b > 0.05 else ("反向!" if t - b < -0.05 else "无用 ✗")
        print(f"    {k:<9} 顶 {t*100:>5.1f}% / 底 {b*100:>5.1f}%  lift {(t-b)*100:>+5.1f}pp  {useful}")


def label_indices(sig, horizon_h, atr_mult, ih):
    """返回 [(bar_index, breakout_label)]:未来 H 根内位移 > atr_mult×ATR 即突破。"""
    score, atr, close = sig["score"], sig["atr"], sig["close"]
    n = len(close)
    H = max(1, round(horizon_h / ih))
    out = []
    for i in range(n):
        if score[i] is None or atr[i] is None or i + H >= n:
            continue
        move = max(abs(close[j] - close[i]) for j in range(i + 1, i + H + 1))
        out.append((i, 1 if move > atr_mult * atr[i] else 0))
    return out


def _seg_lift(sig, seg, name):
    """某信号在一段 bar 上的 顶档-底档 突破率差;常量/数据不足 → None。"""
    ps = [(sig["subs"][name][i], lab) for i, lab in seg if sig["subs"][name][i] is not None]
    vals = [x for x, _ in ps]
    if len(ps) < 20 or (vals and max(vals) - min(vals) < 1e-6):
        return None
    bk = buckets(ps)
    return bk[-1][3] - bk[0][3]


def do_oos(sym, interval, oi_period, W, horizon_h, atr_mult, train_frac):
    ih = interval_hours(interval)
    kl = fetch_klines(sym, interval, 1500)
    sig = compute_signals(kl, fetch_oi(sym, oi_period), fetch_funding(sym), W, 24 / ih)
    labeled = label_indices(sig, horizon_h, atr_mult, ih)
    if len(labeled) < 200:
        raise SystemExit("样本不足")
    cut = int(len(labeled) * train_frac)
    train, test = labeled[:cut], labeled[cut:]

    print(f"# {sym} {interval}  OOS 验证  train {len(train)} / test {len(test)} 根  "
          f"(突破: 未来{horizon_h}h > {atr_mult}×ATR,base {sum(l for _,l in labeled)/len(labeled)*100:.0f}%)")
    print("# 权重已锁定(区间时长+量价背离);逐信号 train/test lift 供诊断,composite 看 TEST 是否保持")
    print("=" * 66)
    print("  逐信号 lift:      train      test      稳健?")
    for k in WEIGHTS:
        lt, lv = _seg_lift(sig, train, k), _seg_lift(sig, test, k)
        ts = f"{lt*100:+.1f}pp" if lt is not None else "N/A"
        vs = f"{lv*100:+.1f}pp" if lv is not None else "N/A"
        both = lt is not None and lv is not None and lt > 0.03 and lv > 0.03
        robust = "稳 ✓" if both else ("过拟合? ✗" if (lt is not None and lt > 0.03) else "—")
        mark = "  ←锁定" if WEIGHTS[k] > 0 else ""
        print(f"    {k:<9}     {ts:>8}   {vs:>8}    {robust}{mark}")
    print("-" * 66)
    locked = [k for k in WEIGHTS if WEIGHTS[k] > 0]

    def comp(i):
        return sum(WEIGHTS[k] * sig["subs"][k][i] for k in locked if sig["subs"][k][i] is not None)

    def clift(seg):
        bk = buckets([(comp(i), lab) for i, lab in seg])
        return bk[-1][3] - bk[0][3], bk[-1][3], bk[0][3]

    tl, tt, tb = clift(train)
    vl, vt, vb = clift(test)
    print(f"  锁定权重: {', '.join(f'{k}={WEIGHTS[k]}' for k in locked)}")
    print(f"  composite    train {tl*100:+.1f}pp(顶{tt*100:.0f}/底{tb*100:.0f})"
          f"   test {vl*100:+.1f}pp(顶{vt*100:.0f}/底{vb*100:.0f})")
    print("=" * 66)
    if vl > 0.05:
        print(f"  ✅ OOS 保持:test 顶/底仍拉开 {vl*100:+.1f}pp → 这段 edge 不是过拟合")
    elif vl > 0.02:
        print(f"  🟡 OOS 减弱:test {vl*100:+.1f}pp,还在但变小 → 谨慎,要更多数据")
    else:
        print(f"  ❌ OOS 崩:test {vl*100:+.1f}pp → train 的 edge 没撑过样本外")


def bar(x, width=20):
    n = int(round(max(0.0, min(1.0, x)) * width))
    return "█" * n + "·" * (width - n)


def do_latest(sym, interval, oi_period, W):
    bpd = 24 / interval_hours(interval)
    kl = fetch_klines(sym, interval, max(W + 60, 200))
    sig = compute_signals(kl, fetch_oi(sym, oi_period), fetch_funding(sym), W, bpd)
    i = len(kl) - 1
    while i >= 0 and sig["score"][i] is None:
        i -= 1
    subs = {k: sig["subs"][k][i] for k in WEIGHTS}
    score = sig["score"][i]
    print(f"# {sym} {interval}  {datetime.now(timezone.utc):%Y-%m-%d %H:%M UTC}  "
          f"price {sig['close'][i]:.4f}  (percentile 窗口 {W} 根)")
    if not sig["ctx"]["oi"]:
        print("# ⚠ OI 历史拿不到,② 用中性 0.5")
    print("-" * 60)
    lab = {"atr": "① ATR 压缩", "oi": "② OI 增加", "funding": "③ Funding 极端",
           "voldiv": "④ 量价背离", "duration": "⑤ 区间时长"}
    for k in WEIGHTS:
        print(f"  {lab[k]:<14} {bar(subs[k])} {subs[k]:.2f}  (w={WEIGHTS[k]})")
    print("-" * 60)
    print(f"  突破概率分  {bar(score)} {score:.2f}")
    v = ("🔴 高 → 别开 Grid、趋势准备" if score >= 0.6 else
         "🟢 低 → 区间平静,Grid 可开(带硬止损)" if score <= 0.4 else "🟡 中 → 谨慎降仓")
    print(f"  判定       {v}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--symbol", default="BTCUSDT")
    ap.add_argument("--interval", default="1h")
    ap.add_argument("--oi-period", default=None)
    ap.add_argument("--window", type=int, default=100, help="percentile 滚动窗口(根)")
    ap.add_argument("--validate", action="store_true", help="验证模式:标注历史突破测预测力")
    ap.add_argument("--oos", action="store_true", help="样本外:train 定权重、test 验证是否保持")
    ap.add_argument("--train-frac", type=float, default=0.65, help="train 占比(前 X 时序)")
    ap.add_argument("--horizon", type=float, default=4.0, help="突破前瞻小时数(验证用)")
    ap.add_argument("--atr-mult", type=float, default=2.0, help="位移 > 此倍数×ATR 算突破")
    args = ap.parse_args()
    oi_period = args.oi_period or args.interval
    if args.oos:
        do_oos(args.symbol, args.interval, oi_period, args.window, args.horizon, args.atr_mult, args.train_frac)
    elif args.validate:
        do_validate(args.symbol, args.interval, oi_period, args.window, args.horizon, args.atr_mult)
    else:
        do_latest(args.symbol, args.interval, oi_period, args.window)


if __name__ == "__main__":
    main()
