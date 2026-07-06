#!/usr/bin/env python3
"""breakout_score — Q1:规则版"区间突破概率"打分(数据管道 + 打分,第一块砖).

思路(用户框架):不判断"现在是不是震荡",而是估计"未来容易不容易突破"。
5 个**领先**信号,全部用 **rolling percentile**(不是固定阈值——抗过拟合:
每个值只看它在自己近期分布里的位置):

  1. ATR 压缩       — 波动收缩 → coil → 突破  (低 ATR 分位 = 高突破分)
  2. OI 增加        — 持仓量上升 = 有人在建仓/蓄势
  3. Funding 极端   — 一边太挤 → 挤压/趋势风险
  4. 量价背离       — 量涨价平 = 换手/吸筹 → 突破
  5. 区间时长       — 窄区间盘越久越容易破(反直觉但对)

高分 → 别开 Grid(趋势腿准备);低分 → 区间平静,Grid 可开。
这是**打分管道**;"这个分数是否真的预示突破"要下一步回测验证。
只用 Binance 公开 fapi,无需 key。

  python3 scripts/breakout_score.py --symbol BTCUSDT --interval 1h
"""
import argparse
import json
import time
import urllib.request
from datetime import datetime, timezone

FAPI = "https://fapi.binance.com"

# 五个信号在总分里的权重(可调;先等权)。高分=突破更可能=别 grid。
WEIGHTS = {"atr": 0.25, "oi": 0.20, "funding": 0.15, "voldiv": 0.20, "duration": 0.20}


def get(url, retries=3):
    last = None
    for a in range(retries):
        try:
            with urllib.request.urlopen(url, timeout=20) as r:
                return json.loads(r.read())
        except Exception as e:  # 瞬时网络中断(IncompleteRead 等)重试
            last = e
            time.sleep(1.5 * (a + 1))
    raise last


def fetch_klines(sym, interval, limit):
    d = get(f"{FAPI}/fapi/v1/klines?symbol={sym}&interval={interval}&limit={limit}")
    return [{"t": k[0], "o": float(k[1]), "h": float(k[2]), "l": float(k[3]),
             "c": float(k[4]), "v": float(k[7])} for k in d]  # v = quote volume


def fetch_oi(sym, period, limit):
    try:
        d = get(f"{FAPI}/futures/data/openInterestHist?symbol={sym}&period={period}&limit={limit}")
        return {int(x["timestamp"]): float(x["sumOpenInterest"]) for x in d}
    except Exception:
        return {}


def fetch_funding(sym, limit=200):
    try:
        d = get(f"{FAPI}/fapi/v1/fundingRate?symbol={sym}&limit={limit}")
        return sorted(((int(x["fundingTime"]), float(x["fundingRate"])) for x in d))
    except Exception:
        return []


def atr_series(kl, n=14):
    """Wilder-ish ATR:每根的 true range 的 n 期简单均值。"""
    tr = [kl[0]["h"] - kl[0]["l"]]
    for i in range(1, len(kl)):
        pc = kl[i - 1]["c"]
        tr.append(max(kl[i]["h"] - kl[i]["l"], abs(kl[i]["h"] - pc), abs(kl[i]["l"] - pc)))
    out = [None] * len(kl)
    for i in range(len(kl)):
        if i >= n - 1:
            out[i] = sum(tr[i - n + 1:i + 1]) / n
    return out


def pctile(value, sample):
    """value 在 sample 里的分位(<=value 的占比),0..1。"""
    s = [x for x in sample if x is not None]
    if not s:
        return 0.5
    return sum(1 for x in s if x <= value) / len(s)


def latest_scores(sym, interval, oi_period, window):
    kl = fetch_klines(sym, interval, max(window + 60, 200))
    if len(kl) < window + 20:
        raise SystemExit(f"K 线不足({len(kl)})")
    oi_map = fetch_oi(sym, oi_period, 500)
    fund = fetch_funding(sym)

    n = len(kl)
    atr = atr_series(kl, 14)
    closes = [k["c"] for k in kl]
    vols = [k["v"] for k in kl]
    W = window

    def win(series, i):
        return series[max(0, i - W + 1):i + 1]

    i = n - 1  # 最新一根
    price = closes[i]

    # ① ATR 压缩:当前 ATR 在近 W 期的分位越低 → 越压缩 → 突破分越高
    atr_p = pctile(atr[i], win(atr, i))
    s_atr = 1 - atr_p

    # ② OI:近 20 根 OI 变化率的分位越高(涨得猛) → 突破分越高
    ts_sorted = sorted(oi_map)
    def oi_at(t):  # 最近 <= t 的 OI
        c = [x for x in ts_sorted if x <= t]
        return oi_map[c[-1]] if c else None
    oi_now = oi_at(kl[i]["t"])
    oi_chg = None
    oi_chg_series = [None] * n
    if oi_now:
        for j in range(20, n):
            a = oi_at(kl[j - 20]["t"]); b = oi_at(kl[j]["t"])
            if a and b and a > 0:
                oi_chg_series[j] = b / a - 1
        oi_chg = oi_chg_series[i]
    s_oi = pctile(oi_chg, win(oi_chg_series, i)) if oi_chg is not None else 0.5

    # ③ Funding 极端:|funding| 的分位越高 → 越极端 → 突破分越高
    fr = fund[-1][1] if fund else 0.0
    fabs = [abs(r) for _, r in fund] if fund else [0.0]
    s_fund = pctile(abs(fr), fabs)

    # ④ 量价背离:量分位高 且 近K根价幅分位低 → 换手/吸筹 → 突破分高
    K = 8
    rng_series = [None] * n
    for j in range(K, n):
        w = closes[j - K:j + 1]
        rng_series[j] = (max(w) - min(w)) / (sum(w) / len(w))
    vol_p = pctile(vols[i], win(vols, i))
    rng_p = pctile(rng_series[i], win(rng_series, i)) if rng_series[i] is not None else 0.5
    s_voldiv = vol_p * (1 - rng_p)

    # ⑤ 区间时长:连续多少根"K 根价幅分位 < 0.35"(即持续压缩),越久 → 突破分越高
    age = 0
    for j in range(i, K, -1):
        rp = pctile(rng_series[j], win(rng_series, j)) if rng_series[j] is not None else 1.0
        if rp < 0.35:
            age += 1
        else:
            break
    bars_per_day = 24 / interval_hours(interval)
    s_dur = min(age / (2 * bars_per_day), 1.0)  # 满 2 天封顶

    subs = {"atr": s_atr, "oi": s_oi, "funding": s_fund, "voldiv": s_voldiv, "duration": s_dur}
    score = sum(WEIGHTS[k] * subs[k] for k in WEIGHTS)
    ctx = {"price": price, "atr_pctile": atr_p, "oi_chg": oi_chg, "funding": fr,
           "vol_pctile": vol_p, "range_pctile": rng_p, "range_age_bars": age,
           "oi_available": bool(oi_map)}
    return score, subs, ctx


def interval_hours(iv):
    unit = iv[-1]; num = int(iv[:-1])
    return num * {"m": 1 / 60, "h": 1, "d": 24}[unit]


def bar(x, width=20):
    n = int(round(max(0.0, min(1.0, x)) * width))
    return "█" * n + "·" * (width - n)


def main():
    ap = argparse.ArgumentParser(description="Q1 区间突破概率打分(规则版)")
    ap.add_argument("--symbol", default="BTCUSDT")
    ap.add_argument("--interval", default="1h", help="K线/OI 周期,如 15m/1h/4h")
    ap.add_argument("--oi-period", default=None, help="OI 周期(默认同 --interval)")
    ap.add_argument("--window", type=int, default=100, help="percentile 滚动窗口(根)")
    args = ap.parse_args()
    oi_period = args.oi_period or args.interval

    score, subs, ctx = latest_scores(args.symbol, args.interval, oi_period, args.window)

    print(f"# {args.symbol} {args.interval}  {datetime.now(timezone.utc):%Y-%m-%d %H:%M UTC}  "
          f"price {ctx['price']:.2f}  (percentile 窗口 {args.window} 根)")
    if not ctx["oi_available"]:
        print("# ⚠ OI 历史拿不到(可能新币/超出30天),OI 信号用中性 0.5")
    print("-" * 66)
    labels = {"atr": "① ATR 压缩", "oi": "② OI 增加", "funding": "③ Funding 极端",
              "voldiv": "④ 量价背离", "duration": "⑤ 区间时长"}
    for k in ["atr", "oi", "funding", "voldiv", "duration"]:
        print(f"  {labels[k]:<14} {bar(subs[k])} {subs[k]:.2f}  (w={WEIGHTS[k]})")
    print("-" * 66)
    print(f"  突破概率分  {bar(score)} {score:.2f}")
    if score >= 0.6:
        verdict = "🔴 高 → 别开 Grid、趋势腿准备(容易突破)"
    elif score <= 0.4:
        verdict = "🟢 低 → 区间平静,Grid 可开(带硬止损)"
    else:
        verdict = "🟡 中 → 谨慎,倾向 Grid 降仓"
    print(f"  判定       {verdict}")
    print("-" * 66)
    print(f"  参考: ATR分位 {ctx['atr_pctile']:.2f}  "
          f"OI20变化 {('%+.1f%%' % (ctx['oi_chg']*100)) if ctx['oi_chg'] is not None else 'n/a'}  "
          f"funding {ctx['funding']*100:+.4f}%  量分位 {ctx['vol_pctile']:.2f}  "
          f"价幅分位 {ctx['range_pctile']:.2f}  压缩已持续 {ctx['range_age_bars']} 根")


if __name__ == "__main__":
    main()
