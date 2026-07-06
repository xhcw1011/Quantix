#!/usr/bin/env python3
"""网格开关闸回测:把 breakout_score 里去伪后活下来的 成交量信号(vol_hi+vol_up)
当成 grid 的开关闸,对比 裸网格 vs 带闸网格。

闸门逻辑(因果、滞后 1 根,无前视):
  - 量分(vol_hi+vol_up 的分位合成,∈[0,1])< thresh → 平静 → 网格开(铺网吃震荡)
  - 量分 ≥ thresh → 要有大动作 → 网格关(平掉库存、暂停),等平静再回来

网格本身:几何等比档位,逐档限价成交,库存上限 ±max_inv 档(有界风险)。
带手续费(网格头号杀手)。裸/带闸用同一套费率,看差值。

用法:
  python3 grid_gate_backtest.py --symbol BTCUSDT --interval 1h --spacing 0.01 --gate-thresh 0.5
"""
import argparse
import math

from breakout_score import fetch_klines, compute_signals, interval_hours


def volume_gate(kl, interval, W):
    """返回每根 bar 的量分(vol_hi+vol_up 合成),∈[0,1],None=暖机不足。无需 OI/Funding。"""
    ih = interval_hours(interval)
    sig = compute_signals(kl, [], [], W, 24 / ih)  # 空 OI/Funding,它们权重为 0
    return sig["score"]


def run_grid(closes, gate=None, gate_thresh=0.5, spacing=0.01, fee=0.0005, max_inv=10, cooldown=0):
    """逐 close 跑网格。gate=None 即裸网格(永远开)。
    cooldown:检测到 volatile 后,要再连续平静 cooldown 根才重新开网(压 flip-flop churn)。
    返回 dict: pnl, ret(相对 max_inv*p0 资本), maxdd, trades, on_frac。"""
    n = len(closes)
    p0 = closes[0]
    step = math.log(1 + spacing)

    def level_of(p):
        return math.floor(math.log(p / p0) / step)

    pos = 0.0        # 库存(+多 -空),单位:档(qty=1/档)
    cash = 0.0       # 纯成交现金
    fees = 0.0       # 累计手续费
    trades = 0
    last_level = level_of(p0)
    capital = max_inv * p0
    peak = 0.0
    maxdd = 0.0
    on_bars = 0
    cool_left = 0

    def fill(price, dqty):
        nonlocal pos, cash, fees, trades
        cash -= dqty * price          # 买(dqty>0)花钱,卖(dqty<0)收钱
        fees += abs(dqty) * price * fee
        pos += dqty
        trades += 1

    for i in range(n):
        p = closes[i]
        active = True
        if gate is not None:
            s = gate[i - 1] if i - 1 >= 0 else None
            volatile = (s is None) or (s >= gate_thresh)
            if volatile:
                cool_left = cooldown       # 重置冷却:高波后至少再等 cooldown 根
                active = False
            elif cool_left > 0:
                cool_left -= 1
                active = False
            else:
                active = True

        if not active:
            if pos != 0.0:            # 收网:按现价平掉库存
                fill(p, -pos)
            last_level = level_of(p)  # 重置参考,避免回来时补一堆追档单
        else:
            on_bars += 1
            lvl = level_of(p)
            while lvl < last_level and pos < max_inv:   # 跌破下一档 → 买
                last_level -= 1
                fill(p0 * math.exp(step * last_level), 1.0)
            while lvl > last_level and pos > -max_inv:   # 涨破上一档 → 卖
                last_level += 1
                fill(p0 * math.exp(step * last_level), -1.0)

        equity = cash + pos * p - fees
        peak = max(peak, equity)
        maxdd = max(maxdd, peak - equity)

    final_p = closes[-1]
    pnl = cash + pos * final_p - fees
    return {
        "pnl": pnl, "ret": pnl / capital, "maxdd": maxdd / capital,
        "trades": trades, "fees": fees, "on_frac": on_bars / n,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--symbol", default="BTCUSDT")
    ap.add_argument("--interval", default="1h")
    ap.add_argument("--window", type=int, default=100)
    ap.add_argument("--spacing", type=float, default=0.01, help="网格档距(等比),0.01=1%")
    ap.add_argument("--gate-thresh", type=float, default=0.5, help="量分<此值才开网(越低越保守)")
    ap.add_argument("--fee", type=float, default=0.0005, help="单边手续费,0.0005=5bp(taker)")
    ap.add_argument("--max-inv", type=int, default=10, help="库存上限(档),决定资本与最大方向暴露")
    ap.add_argument("--cooldown", type=int, default=0, help="高波后再连续平静 N 根才重新开网(压churn)")
    args = ap.parse_args()

    kl = fetch_klines(args.symbol, args.interval, 1500)
    closes = [k["c"] for k in kl]
    gate = volume_gate(kl, args.interval, args.window)
    ih = interval_hours(args.interval)
    days = len(closes) * ih / 24

    naked = run_grid(closes, None, spacing=args.spacing, fee=args.fee, max_inv=args.max_inv)
    gated = run_grid(closes, gate, args.gate_thresh, args.spacing, args.fee, args.max_inv, args.cooldown)
    bh = (closes[-1] / closes[0] - 1)

    print(f"# {args.symbol} {args.interval}  {len(closes)} 根 ~{days:.0f}天  "
          f"档距 {args.spacing*100:.2f}%  费 {args.fee*1e4:.0f}bp/边  库存上限 ±{args.max_inv}  "
          f"闸门阈 {args.gate_thresh}")
    print(f"# 买入持有基准: {bh*100:+.1f}%")
    print("=" * 74)
    print(f"  {'':<10}{'收益%':>9}{'最大回撤%':>11}{'成交数':>9}{'手续费$':>11}{'在场%':>8}")
    print(f"  {'裸网格':<10}{naked['ret']*100:>9.1f}{naked['maxdd']*100:>11.1f}"
          f"{naked['trades']:>9}{naked['fees']:>11.0f}{100.0:>8.0f}")
    print(f"  {'带闸网格':<10}{gated['ret']*100:>9.1f}{gated['maxdd']*100:>11.1f}"
          f"{gated['trades']:>9}{gated['fees']:>11.0f}{gated['on_frac']*100:>8.0f}")
    print("-" * 74)
    dret = (gated['ret'] - naked['ret']) * 100
    ddd = (naked['maxdd'] - gated['maxdd']) * 100
    print(f"  闸门效果: 收益 {dret:+.1f}pp   回撤 {ddd:+.1f}pp(正=闸门减少回撤)"
          f"   成交 {naked['trades']-gated['trades']:+d}(负=省了churn)")
    verdict = ("✅ 闸门同时改善收益和回撤" if dret > 0 and ddd > 0 else
               "🟡 闸门减回撤但削收益(降波动)" if ddd > 0 else
               "❌ 闸门没帮助")
    print(f"  {verdict}")


if __name__ == "__main__":
    main()
