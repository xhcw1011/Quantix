#!/usr/bin/env python3
"""Quick validation: does staged (partial, multi-step) position reduction below
the grid's own floor help or hurt, on top of the already-validated TED gate?

This is explicitly NOT the same thing as the binary full-flatten `StopLossPct`
that volume_gate_grid_2026-07-06 tested and rejected (that one locks in the
whole loss at once and can't tell "recoverable dip" from "terminal decline").
This reduces the position in stages (a fraction per additional grid-spacing
of decline past the point the grid ran out of buying room), only after the
grid is already fully invested — i.e. it's a last-resort tail-risk cap for
the specific "grid pinned at max_inv, price keeps falling" scenario the
2026-07-31 no-vol-decline gauge found the current grid+TED never actually
hit (0/1430 candidate stretches ever pinned).

Usage:
  python3 scripts/grid_staged_reduce_backtest.py
"""
import math

from breakout_score import compute_signals, interval_hours
from grid_gate_backtest import gate_timeline, run_grid
from grid_trend_switch import fetch_range, to_ms
from datetime import datetime, timezone


def run_grid_staged_reduce(closes, gate=None, exit_thresh=0.5, enter_thresh=0.5,
                            cooldown=0, persistence=1, spacing=0.01, fee=0.0005, max_inv=10,
                            reduce_levels=3, reduce_fraction=0.3):
    """Same mechanics as grid_gate_backtest.run_grid, plus: once the grid is
    fully long (pos==max_inv) and price keeps falling past the level the grid
    ran out of room to buy, reduce `reduce_fraction` of the *current* position
    for each additional `spacing`-sized level of further decline, up to
    `reduce_levels` stages. Resets (re-arms) whenever the TED gate flattens
    the position, same as the existing re-center behavior.
    """
    n = len(closes)
    p0 = closes[0]
    step = math.log(1 + spacing)

    def level_of(p):
        return math.floor(math.log(p / p0) / step)

    pos = 0.0
    cash = 0.0
    fees = 0.0
    trades = 0
    last_level = level_of(p0)
    capital = max_inv * p0
    peak = 0.0
    maxdd = 0.0
    on_bars = 0
    equity_curve = []
    pos_curve = []
    reduce_events = []
    triggered_stages = 0

    on = gate_timeline(gate, exit_thresh, enter_thresh, cooldown, persistence) if gate is not None else [True] * n

    def fill(price, dqty):
        nonlocal pos, cash, fees, trades
        cash -= dqty * price
        fees += abs(dqty) * price * fee
        pos += dqty
        trades += 1

    for i in range(n):
        p = closes[i]
        active = on[i]

        if not active:
            if pos != 0.0:
                fill(p, -pos)
            last_level = level_of(p)
            triggered_stages = 0
        else:
            on_bars += 1
            lvl = level_of(p)
            while lvl < last_level and pos < max_inv:
                last_level -= 1
                fill(p0 * math.exp(step * last_level), 1.0)
            while lvl > last_level and pos > -max_inv:
                last_level += 1
                fill(p0 * math.exp(step * last_level), -1.0)

            if pos >= max_inv:
                deficit = last_level - lvl
                target_stage = min(reduce_levels, max(0, deficit))
                while triggered_stages < target_stage and pos > 0:
                    triggered_stages += 1
                    qty = pos * reduce_fraction
                    fill(p, -qty)
                    reduce_events.append((i, p, qty))

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
        "reduce_events": reduce_events,
    }


def main():
    combos = [("BTCUSDT", "15m"), ("ETHUSDT", "15m"), ("BTCUSDT", "5m"), ("ETHUSDT", "5m")]
    param_sets = [(2, 0.20), (3, 0.30), (4, 0.40)]

    end_dt = datetime(2026, 7, 30, tzinfo=timezone.utc)
    start_dt = datetime(2026, 5, 1, tzinfo=timezone.utc)
    start_ms, end_ms = int(start_dt.timestamp() * 1000), int(end_dt.timestamp() * 1000)

    common = dict(exit_thresh=0.70, enter_thresh=0.40, cooldown=3, persistence=3,
                  spacing=0.01, fee=0.0005, max_inv=10)

    for sym, iv in combos:
        kl = fetch_range(sym, iv, start_ms, end_ms)
        closes = [k["c"] for k in kl]
        ih = interval_hours(iv)
        score = compute_signals(kl, [], [], 100, 24 / ih)["score"]

        baseline = run_grid(closes, score, **common)
        print(f"# {sym} {iv}  {len(closes)}根  baseline(TED-gated, no reduce): "
              f"ret={baseline['ret']*100:+.2f}%  maxdd={baseline['maxdd']*100:.2f}%  "
              f"trades={baseline['trades']}  fees=${baseline['fees']:.1f}")

        for rl, rf in param_sets:
            r = run_grid_staged_reduce(closes, score, reduce_levels=rl, reduce_fraction=rf, **common)
            n_events = len(r["reduce_events"])
            print(f"    +staged_reduce(levels={rl},frac={rf:.0%}): "
                  f"ret={r['ret']*100:+.2f}%  maxdd={r['maxdd']*100:.2f}%  "
                  f"trades={r['trades']}  fees=${r['fees']:.1f}  reduce_events={n_events}")
        print()


if __name__ == "__main__":
    main()
