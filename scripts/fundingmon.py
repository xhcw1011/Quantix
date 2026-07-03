#!/usr/bin/env python3
"""fundingmon — 永续资金费率错位监控 / 退出告警.

用途:你在"收资金费率"的一侧持有一个小仓(long=收负费率,short=收正费率),
本脚本定时拉该合约的 funding + 基差,当出现下面任一情况就**响铃 + 醒目告警**,
提醒你按纪律撤出、别死等:

  ① 翻转:费率翻到对你不利的方向(你开始倒贴)。
  ② 收敛:对你有利的费率(近 N 期均值)年化跌破阈值——错位在消失、顺风变弱。

只用公开 fapi 端点(无需 API key)。示例:
  python3 scripts/fundingmon.py --symbol LABUSDT --side long --exit-annual 300 --interval 300

注:这只是"纪律性退出"的告警器,不下单、不碰你的仓位。价格风险自己盯。
"""
import argparse
import json
import os
import sys
import time
import urllib.parse
import urllib.request
from datetime import datetime, timezone

FAPI = "https://fapi.binance.com"

# Telegram 通知(可选):设了这两个环境变量就自动往你 TG 发告警,人在哪都收得到。
#   export QUANTIX_TG_BOT_TOKEN=...   export QUANTIX_TG_CHAT_ID=...
TG_TOKEN = os.environ.get("QUANTIX_TG_BOT_TOKEN", "")
TG_CHAT = os.environ.get("QUANTIX_TG_CHAT_ID", "")


def tg_send(msg: str) -> None:
    if not TG_TOKEN or not TG_CHAT:
        return
    try:
        data = urllib.parse.urlencode({"chat_id": TG_CHAT, "text": msg}).encode()
        urllib.request.urlopen(
            f"https://api.telegram.org/bot{TG_TOKEN}/sendMessage", data=data, timeout=10)
    except Exception as e:
        print(f"  (TG 发送失败: {e})")


def now() -> str:
    return datetime.now(timezone.utc).strftime("%m-%d %H:%M:%S UTC")


def get(path: str):
    with urllib.request.urlopen(FAPI + path, timeout=15) as r:
        return json.load(r)


def funding_interval_hours(symbol: str) -> int:
    """结算间隔小时数(默认 8h;热门/波动大的常是 1h/4h)。"""
    try:
        for f in get("/fapi/v1/fundingInfo"):
            if f["symbol"] == symbol:
                return int(f.get("fundingIntervalHours", 8))
    except Exception:
        pass
    return 8


def snapshot(symbol: str, ih: int, lookback: int) -> dict:
    prem = get(f"/fapi/v1/premiumIndex?symbol={symbol}")
    hist = get(f"/fapi/v1/fundingRate?symbol={symbol}&limit={lookback}")
    mark = float(prem["markPrice"])
    index = float(prem.get("indexPrice", 0) or 0)
    last = float(prem.get("lastFundingRate", 0))
    rates = [float(h["fundingRate"]) for h in hist] or [last]
    avg = sum(rates) / len(rates)
    per_yr = 24 / ih * 365
    return {
        "mark": mark,
        "basis": (mark / index - 1) if index else 0.0,
        "last": last,
        "avg": avg,
        "last_ann": last * per_yr,
        "avg_ann": avg * per_yr,
    }


def main() -> None:
    ap = argparse.ArgumentParser(description="永续资金费率错位监控/退出告警")
    ap.add_argument("--symbol", default="LABUSDT")
    ap.add_argument("--side", choices=["long", "short"], default="long",
                    help="你的持仓方向:long=收负费率;short=收正费率")
    ap.add_argument("--exit-rate", type=float, default=0.3,
                    help="有利费率(近均值,每期%%)跌破此值就告警收敛/撤出。"
                         "注意:是'每期'——1h 合约 0.3%%=7.2%%/天,8h 合约 0.3%%=0.9%%/天")
    ap.add_argument("--lookback", type=int, default=8, help="用最近几期算均值")
    ap.add_argument("--price-stop", type=float, default=0.0,
                    help="价格止损位:long 时 mark 跌破它、short 时 mark 涨破它就告警(0=关)。"
                         "仅告警,不下单——真止损请在交易所挂原生 STOP 单")
    ap.add_argument("--interval", type=int, default=300, help="轮询间隔(秒)")
    ap.add_argument("--once", action="store_true", help="只查一次后退出")
    args = ap.parse_args()

    # 收费率的一侧:long 收负费率、short 收正费率 → favor 值 = favor_sign * rate,>0 表示在收钱
    favor_sign = -1.0 if args.side == "long" else 1.0
    ih = funding_interval_hours(args.symbol)
    exit_ann = args.exit_rate * (24 / ih * 365)  # 阈值年化(仅用于展示)
    print(f"# 监控 {args.symbol}  结算 {ih}h  方向 {args.side}"
          f"(收{'负' if args.side == 'long' else '正'}费率)  "
          f"收敛阈值 {args.exit_rate:.3f}%/期(≈{exit_ann:.0f}%/yr, ≈{args.exit_rate * 24 / ih:.2f}%/天)")
    print(f"# 退出信号 ① 费率翻转到对你不利  ② 有利每期费率均值跌破 {args.exit_rate:.3f}%"
          + (f"  ③ 价格{'跌破' if args.side == 'long' else '涨破'} {args.price_stop}" if args.price_stop else ""))
    print(f"# TG 通知: {'开(会推到你 Telegram)' if TG_TOKEN and TG_CHAT else '关(未设 QUANTIX_TG_BOT_TOKEN/CHAT_ID,仅终端响铃)'}")
    print("-" * 90)
    if not args.once:
        tg_send(f"✅ fundingmon 已启动,盯 {args.symbol}({args.side}):费率翻转/收敛<{args.exit_rate}%"
                + (f"/价格{'跌破' if args.side == 'long' else '涨破'}{args.price_stop}" if args.price_stop else "")
                + " 会推你。")

    alerted_flip = False
    alerted_conv = False
    alerted_price = False
    while True:
        try:
            s = snapshot(args.symbol, ih, args.lookback)
        except Exception as e:
            print(f"{now()}  拉取失败: {e}")
            if args.once:
                return
            time.sleep(args.interval)
            continue

        favor_now = favor_sign * s["last"]                 # >0 当前在收钱
        favor_avg_pct = favor_sign * s["avg"] * 100        # 有利每期费率均值(%)
        favor_avg_ann = favor_sign * s["avg_ann"]          # 有利年化
        state = "收钱" if favor_now > 0 else "倒贴!"
        print(f"{now()}  费率 {s['last'] * 100:+.4f}%/期  年化 {s['last_ann'] * 100:+.0f}%  "
              f"近{args.lookback}期均值 {favor_avg_pct:+.4f}%/期(年化 {favor_avg_ann * 100:+.0f}%)  "
              f"基差 {s['basis'] * 100:+.2f}%  mark {s['mark']:.4f}  → {state}")

        # ① 翻转告警(只在首次翻转时响)
        if favor_now < 0:
            if not alerted_flip:
                bell_alert(args.symbol, f"🔴🔴 费率已翻转到对你不利({s['last'] * 100:+.4f}%/期)—— 你在倒贴,立即撤!")
                alerted_flip = True
        else:
            alerted_flip = False

        # ② 收敛告警(有利每期费率均值跌破阈值,只在首次跌破时响)
        if favor_now > 0 and favor_avg_pct < args.exit_rate:
            if not alerted_conv:
                bell_alert(args.symbol, f"🟠 费率收敛:有利每期均值降到 {favor_avg_pct:+.4f}% < 阈值 {args.exit_rate:.3f}% "
                           f"(年化 {favor_avg_ann * 100:+.0f}%)—— 错位在消失、顺风变弱,按纪律考虑撤出。")
                alerted_conv = True
        elif favor_avg_pct >= args.exit_rate:
            alerted_conv = False

        # ③ 价格止损告警(long 跌破 / short 涨破;只在首次破线时响)
        if args.price_stop > 0:
            breached = (s["mark"] <= args.price_stop) if args.side == "long" else (s["mark"] >= args.price_stop)
            if breached:
                if not alerted_price:
                    bell_alert(args.symbol, f"🔴🔴 价格{'跌破' if args.side == 'long' else '涨破'}止损位 "
                               f"{args.price_stop}(当前 {s['mark']:.4f})—— 平仓!")
                    alerted_price = True
            else:
                alerted_price = False

        if args.once:
            return
        time.sleep(args.interval)


def bell_alert(symbol: str, msg: str) -> None:
    sys.stdout.write("\a")  # 终端响铃
    print("=" * 90)
    print(msg)
    print("=" * 90)
    sys.stdout.flush()
    tg_send(f"[fundingmon {symbol}]\n{msg}")


if __name__ == "__main__":
    main()
