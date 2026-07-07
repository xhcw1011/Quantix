#!/usr/bin/env python3
"""grid⇄trend 切换 thesis 验证:switch vs grid-only vs trend-only,扣真实费,按 regime 分段。

模型:TED(成交量分,来自 breakout_score 的 vol_hi+vol_up)判 regime。
  平静(分低)→ 跑 grid(吃震荡);事件/放量(分≥退出阈)→ 平掉 grid、切趋势跟随骑那波;
  再平静(迟滞+持续+冷却)→ 平趋势、回 grid。
问:切换是否**同时打败**两条单腿(grid-only / trend-only)?只有同时赢,切换才是真 edge。

用法:
  python3 scripts/grid_trend_switch.py --symbol BTCUSDT --start 2024-11-01 --end 2024-12-20
"""
import argparse
import math
from datetime import datetime, timezone

from breakout_score import get, FAPI, interval_hours, compute_signals


def to_ms(d):
    return int(datetime.strptime(d, "%Y-%m-%d").replace(tzinfo=timezone.utc).timestamp() * 1000)


def fetch_range(sym, interval, start_ms, end_ms):
    """分页拉取一段完整历史 K 线(Binance fapi,1500/次)。"""
    out, cur = [], start_ms
    while cur < end_ms:
        d = get(f"{FAPI}/fapi/v1/klines?symbol={sym}&interval={interval}&startTime={cur}&endTime={end_ms}&limit=1500")
        if not d:
            break
        out += d
        if len(d) < 1500:
            break
        nxt = d[-1][0] + 1
        if nxt <= cur:
            break
        cur = nxt
    return [{"t": k[0], "h": float(k[2]), "l": float(k[3]), "c": float(k[4]), "v": float(k[7])}
            for k in out if start_ms <= k[0] < end_ms]


def sma(vals, n):
    out, s = [None] * len(vals), 0.0
    for i, v in enumerate(vals):
        s += v
        if i >= n:
            s -= vals[i - n]
        if i >= n - 1:
            out[i] = s / n
    return out


def efficiency_ratio(closes, i, n):
    """Kaufman ER: |净移动| / Σ|逐根移动|,近 n 根。1=干净趋势,0=纯噪声。"""
    if i < n:
        return 0.0
    net = abs(closes[i] - closes[i - n])
    noise = sum(abs(closes[j] - closes[j - 1]) for j in range(i - n + 1, i + 1))
    return net / noise if noise > 0 else 0.0


def simulate(closes, score, mode, fee, spacing, max_inv, exit_th, enter_th, cooldown, persistence,
             fast, slow, er_n, er_min, switch_by="ted", er_enter=0.35, er_exit=0.20):
    """mode: grid | trend | switch. 网格用可重居中的几何格(长窗/趋势里也不失效);
    趋势腿用 SMA fast/slow 方向(合约、双向)。所有模式共用同一套机制,公平对比。"""
    n = len(closes)
    step = math.log(1 + spacing)
    smaf, smas = sma(closes, fast), sma(closes, slow)
    base = closes[0]
    last_level = 0

    def rel_level(p):
        return math.floor(math.log(p / base) / step)

    pos = 0.0        # 库存(单位:1 unit/coin)
    cash = 0.0
    fees = 0.0
    trades = 0
    cap = max_inv * closes[0]
    peak = maxdd = 0.0
    curve = []       # 每根 bar 的 equity/cap(供并行组合)

    state = "grid"   # switch 子状态
    cool = low_streak = 0
    trend_dir = 0    # 事件驱动:干净交叉才翻,之间持有(不每根 bar 调仓)

    def fill(price, dq):
        nonlocal pos, cash, fees, trades
        cash -= dq * price
        fees += abs(dq) * price * fee
        pos += dq
        trades += 1

    for i in range(n):
        p = closes[i]
        s = score[i - 1] if i > 0 and score[i - 1] is not None else None  # 因果、滞后 1 根

        if mode == "grid":
            sub = "grid"
        elif mode == "trend":
            sub = "trend"
        else:  # switch 状态机
            if switch_by == "er":   # 趋势/震荡分类器:ER 高→趋势、低→震荡(带迟滞)
                erv = efficiency_ratio(closes, i, er_n)
                if state == "grid":
                    if erv >= er_enter:            # 进入趋势 → 平网、切趋势
                        if pos != 0:
                            fill(p, -pos)
                        state, cool, low_streak = "trend", 0, 0
                else:
                    low_streak = low_streak + 1 if erv < er_exit else 0
                    cool += 1
                    if cool >= cooldown and low_streak >= persistence:  # 回震荡 → 平趋势、回网
                        if pos != 0:
                            fill(p, -pos)
                        state, base, last_level = "grid", p, 0
            else:                   # TED:波动率事件才切趋势
                if state == "grid":
                    if s is not None and s >= exit_th:
                        if pos != 0:
                            fill(p, -pos)
                        state, cool, low_streak = "trend", 0, 0
                else:
                    low_streak = low_streak + 1 if (s is not None and s < enter_th) else 0
                    cool += 1
                    if cool >= cooldown and low_streak >= persistence:
                        if pos != 0:
                            fill(p, -pos)
                        state, base, last_level = "grid", p, 0
            sub = state

        # 事件驱动更新趋势方向(所有模式共用):干净的金叉/死叉才翻,ER 过滤掉震荡里的假交叉
        if i > 0 and smaf[i] is not None and smas[i - 1] is not None:
            prev, cur = smaf[i - 1] - smas[i - 1], smaf[i] - smas[i]
            if prev <= 0 < cur and efficiency_ratio(closes, i, er_n) >= er_min:
                trend_dir = 1
            elif prev >= 0 > cur and efficiency_ratio(closes, i, er_n) >= er_min:
                trend_dir = -1

        if sub == "grid":
            if abs(rel_level(p)) >= max_inv:   # 价格离开网格带 → 重居中(库存留着,真实网格行为)
                base, last_level = p, 0
            lvl = rel_level(p)
            while lvl < last_level and pos < max_inv:   # 跌破下一档 → 买
                last_level -= 1
                fill(base * math.exp(step * last_level), 1.0)
            while lvl > last_level and pos > -max_inv:   # 涨破上一档 → 卖
                last_level += 1
                fill(base * math.exp(step * last_level), -1.0)
        else:  # 趋势:持有当前趋势方向的满仓(事件驱动,不 whipsaw)
            target = trend_dir * max_inv
            if pos != target:
                fill(p, target - pos)

        eq = cash + pos * p - fees
        peak = max(peak, eq)
        maxdd = max(maxdd, peak - eq)
        curve.append(eq / cap)

    pnl = cash + pos * closes[-1] - fees
    return {"ret": pnl / cap, "maxdd": maxdd / cap, "trades": trades, "fees": fees, "curve": curve}


def combine(curves, weights):
    """把多本独立账的 equity/cap 曲线按权重合成组合曲线,返回 (组合收益, 组合最大回撤)。"""
    n = len(curves[0])
    comb = [sum(w * c[i] for w, c in zip(weights, curves)) for i in range(n)]
    peak = mdd = 0.0
    for v in comb:
        peak = max(peak, v)
        mdd = max(mdd, peak - v)
    return comb[-1], mdd


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--symbol", default="BTCUSDT")
    ap.add_argument("--interval", default="15m")
    ap.add_argument("--start", required=True)
    ap.add_argument("--end", required=True)
    ap.add_argument("--spacing", type=float, default=0.005)
    ap.add_argument("--max-inv", type=int, default=10)
    ap.add_argument("--fee", type=float, default=0.0005)
    ap.add_argument("--fast", type=int, default=30)
    ap.add_argument("--slow", type=int, default=90)
    ap.add_argument("--er-n", type=int, default=10)
    ap.add_argument("--er-min", type=float, default=0.20)
    a = ap.parse_args()

    ih = interval_hours(a.interval)
    kl = fetch_range(a.symbol, a.interval, to_ms(a.start), to_ms(a.end))
    if len(kl) < 300:
        raise SystemExit(f"数据不足: {len(kl)} 根")
    closes = [k["c"] for k in kl]
    score = compute_signals(kl, [], [], 100, 24 / ih)["score"]  # TED 分 = vol_hi+vol_up
    bh = closes[-1] / closes[0] - 1
    days = len(closes) * ih / 24
    regime = "牛📈" if bh > 0.15 else ("熊📉" if bh < -0.15 else "震荡↔")

    common = dict(fee=a.fee, spacing=a.spacing, max_inv=a.max_inv,
                  exit_th=0.70, enter_th=0.40, cooldown=3, persistence=3,
                  fast=a.fast, slow=a.slow, er_n=a.er_n, er_min=a.er_min)
    g = simulate(closes, score, "grid", **common)
    t = simulate(closes, score, "trend", **common)
    s_ted = simulate(closes, score, "switch", switch_by="ted", **common)
    s_er = simulate(closes, score, "switch", switch_by="er", **common)

    print(f"# {a.symbol} {a.interval}  {a.start}→{a.end}  {len(closes)}根/~{days:.0f}天  "
          f"买入持有 {bh*100:+.0f}%  regime={regime}")
    par_ret, par_dd = combine([g["curve"], t["curve"]], [0.5, 0.5])   # 并行:两本独立账 50/50
    print("=" * 62)
    print(f"  {'模式':<18}{'收益%':>9}{'最大回撤%':>11}{'成交':>8}")
    for name, r in [("grid-only", g), ("trend-only", t), ("switch(TED)", s_ted), ("switch(ER)", s_er)]:
        print(f"  {name:<18}{r['ret']*100:>9.1f}{r['maxdd']*100:>11.1f}{r['trades']:>8}")
    print(f"  {'并行 grid+trend':<18}{par_ret*100:>9.1f}{par_dd*100:>11.1f}{'—':>8}")
    print("-" * 62)
    best_leg = max(g["ret"], t["ret"])
    print(f"  switch(TED) vs 更好单腿: {(s_ted['ret']-best_leg)*100:+.1f}pp   "
          f"switch(ER): {(s_er['ret']-best_leg)*100:+.1f}pp")
    print(f"  并行 vs 更好单腿: {(par_ret-best_leg)*100:+.1f}pp   "
          f"并行回撤 vs grid/trend: {(par_dd - min(g['maxdd'], t['maxdd']))*100:+.1f}pp(负=并行回撤更小)")


if __name__ == "__main__":
    main()
