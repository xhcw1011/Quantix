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


def gate_timeline(score, exit_thresh=0.5, enter_thresh=0.5, cooldown=0, persistence=1):
    """逐 bar 的开关闸状态(三机制,因果、滞后1根,无前视),从 TED 分数序列算出:
      ① 迟滞 Hysteresis:量分 ≥ exit_thresh 才退出;<enter_thresh 才考虑回来(exit>enter=死区)。
      ② 冷却 Cooldown:退出后至少等 cooldown 根才允许回来。
      ③ 持续 Persistence:要连续 persistence 根 Low(<enter_thresh)才真回来。
    单阈退化:exit=enter, cooldown=0, persistence=1。
    返回 list[bool],长度=len(score),True=该 bar 网格开。独立于 run_grid,
    是 grid_no_vol_decline_gauge.py 复用的那部分,避免重新照抄一份、和 Go 版(volgate.go)
    的行为漂移(暖机例外:score[i-1] is None 时本函数判 gate OFF,Go 生产版
    volgate.go 暖机期保持 gate ON;暖机后状态机一致,细节见
    grid_no_vol_decline_gauge.py 模块 docstring)。"""
    n = len(score)
    state_on = True     # 闸门状态:True=开网,False=收网中
    since_exit = 0      # 退出后过了几根
    low_streak = 0      # 连续 Low 计数
    on = [True] * n
    for i in range(n):
        s = score[i - 1] if i - 1 >= 0 else None
        if state_on:
            if s is None or s >= exit_thresh:      # ① 迟滞:高于退出阈 → 收网
                state_on = False
                since_exit = 0
                low_streak = 0
        else:
            since_exit += 1
            low_streak = low_streak + 1 if (s is not None and s < enter_thresh) else 0
            # ②冷却够 + ③连续 Low 够 + 迟滞(低于回入阈,已含在 low_streak 里)→ 回来
            if since_exit >= cooldown and low_streak >= persistence:
                state_on = True
        on[i] = state_on
    return on


def run_grid(closes, gate=None, exit_thresh=0.5, enter_thresh=0.5,
             cooldown=0, persistence=1, spacing=0.01, fee=0.0005, max_inv=10):
    """逐 close 跑网格。gate=None 即裸网格(永远开)。开关闸状态用 gate_timeline()。
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
    equity_curve = []  # per-bar equity (cash + pos*p - fees), same units as pnl/capital
    pos_curve = []     # per-bar inventory (档), for episode-level "did we pin ±max_inv" checks
    # precondition: `on` must be the same length as `closes` (indexed 0..n-1 below).
    # gate_timeline(gate, ...) always returns len(gate) entries, so this holds whenever
    # `gate` is the same TED score array as `closes`' klines (every real call site passes
    # exactly that). Unreachable in practice, but note: the pre-refactor inline version
    # indexed gate[i-1] directly (guarded by i-1>=0) rather than pre-materializing a
    # same-length array, so it was slightly more tolerant of a mismatched-length `gate`
    # than this is — this refactor does not defensively pad/truncate `on`.
    on = gate_timeline(gate, exit_thresh, enter_thresh, cooldown, persistence) if gate is not None else [True] * n

    def fill(price, dqty):
        nonlocal pos, cash, fees, trades
        cash -= dqty * price          # 买(dqty>0)花钱,卖(dqty<0)收钱
        fees += abs(dqty) * price * fee
        pos += dqty
        trades += 1

    for i in range(n):
        p = closes[i]
        active = on[i]

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
        equity_curve.append(equity)
        pos_curve.append(pos)

    final_p = closes[-1]
    pnl = cash + pos * final_p - fees
    return {
        "pnl": pnl, "ret": pnl / capital, "maxdd": maxdd / capital,
        "trades": trades, "fees": fees, "on_frac": on_bars / n,
        "equity_curve": equity_curve, "pos_curve": pos_curve,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--symbol", default="BTCUSDT")
    ap.add_argument("--interval", default="1h")
    ap.add_argument("--window", type=int, default=100)
    ap.add_argument("--spacing", type=float, default=0.01, help="网格档距(等比),0.01=1%")
    ap.add_argument("--fee", type=float, default=0.0005, help="单边手续费,0.0005=5bp(taker)")
    ap.add_argument("--max-inv", type=int, default=10, help="库存上限(档),决定资本与最大方向暴露")
    # 三机制参数(默认按你给的图:退出0.70 / 回入0.40 / 冷却3 / 持续3)
    ap.add_argument("--single-thresh", type=float, default=0.5, help="对照:单阈闸门的阈值")
    ap.add_argument("--exit-thresh", type=float, default=0.70, help="迟滞:量分≥此值退出")
    ap.add_argument("--enter-thresh", type=float, default=0.40, help="迟滞:量分<此值才考虑回入")
    ap.add_argument("--cooldown", type=int, default=3, help="退出后至少等 N 根")
    ap.add_argument("--persistence", type=int, default=3, help="连续 N 根 Low 才回来")
    args = ap.parse_args()

    kl = fetch_klines(args.symbol, args.interval, 1500)
    closes = [k["c"] for k in kl]
    gate = volume_gate(kl, args.interval, args.window)
    ih = interval_hours(args.interval)
    days = len(closes) * ih / 24
    g = dict(spacing=args.spacing, fee=args.fee, max_inv=args.max_inv)

    naked = run_grid(closes, None, **g)
    single = run_grid(closes, gate, args.single_thresh, args.single_thresh, 0, 1, **g)
    three = run_grid(closes, gate, args.exit_thresh, args.enter_thresh,
                     args.cooldown, args.persistence, **g)
    bh = (closes[-1] / closes[0] - 1)

    print(f"# {args.symbol} {args.interval}  {len(closes)} 根 ~{days:.0f}天  "
          f"档距 {args.spacing*100:.2f}%  费 {args.fee*1e4:.0f}bp/边  库存 ±{args.max_inv}   买入持有 {bh*100:+.1f}%")
    print(f"# 三机制: 退出≥{args.exit_thresh} 回入<{args.enter_thresh} 冷却{args.cooldown}根 持续{args.persistence}根")
    print("=" * 78)
    print(f"  {'':<16}{'收益%':>9}{'最大回撤%':>11}{'成交数':>9}{'手续费$':>11}{'在场%':>8}")

    def row(name, r):
        onf = 100.0 if name == "裸网格" else r["on_frac"] * 100
        print(f"  {name:<16}{r['ret']*100:>9.1f}{r['maxdd']*100:>11.1f}"
              f"{r['trades']:>9}{r['fees']:>11.0f}{onf:>8.0f}")

    row("裸网格", naked)
    row("单阈闸门(0.5)", single)
    row("三机制闸门", three)
    print("-" * 78)
    print(f"  vs 裸: 单阈 收益{(single['ret']-naked['ret'])*100:+.1f}/回撤{(naked['maxdd']-single['maxdd'])*100:+.1f}pp   "
          f"三机制 收益{(three['ret']-naked['ret'])*100:+.1f}/回撤{(naked['maxdd']-three['maxdd'])*100:+.1f}pp")
    print(f"  三机制 vs 单阈: 收益{(three['ret']-single['ret'])*100:+.1f}pp  "
          f"回撤{(single['maxdd']-three['maxdd'])*100:+.1f}pp  "
          f"手续费{single['fees']-three['fees']:+.0f}$  成交{single['trades']-three['trades']:+d}")


if __name__ == "__main__":
    main()
