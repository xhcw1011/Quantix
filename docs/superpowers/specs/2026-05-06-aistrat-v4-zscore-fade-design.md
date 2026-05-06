# AI Strategy v4 — Single-Shot Z-Score Fade

**Status**: Draft for review
**Date**: 2026-05-06
**Replaces**: `internal/strategy/aistrat/` v3 (regime detection + grid + trend + tech_reversal hybrid)
**Author**: brainstorming session 2026-05-06

## Problem Statement

The current `aistrat` (v3) over 105 days of ETH 5m backtest returns **-3.61% with PF 0.84**. Live trading 6 days on demo account: **-6.54%**, with **$273 in fees from 386 fills** (84% of total loss). Diagnosis from log analysis:

- 76% of exits are tick-level `trailing` at avg **-$0.54/trade** — the strategy is shaken out by 5m noise and re-enters, paying double fees
- Only `grid_tp` (5 trades, 80% win rate, +$32.73 total) shows positive expectancy — mean reversion is the only path that actually works
- All other paths (`trailing`, `tech_reversal`) net negative
- 1h-aligned skip (deployed 4/30) saved 476 false reversals — strategy 100% worse without it, but not enough to fix the underlying loss
- **Root cause**: trying to be a 4-in-1 strategy (grid + trend + breakout + reversion) on 5m data has no demonstrated edge

## Edge Thesis

**ETH on 5m bars exhibits statistical mean reversion: when price is >2.5 standard deviations from its 100-min moving average, the next 1-hour return has positive expectancy in the reversion direction.**

This thesis is testable, falsifiable, and matches the only profitable subset of v3 (`grid_tp` 80% win rate). Everything else in v3 piles complexity on top of this single edge — and the complexity loses money.

If this thesis is **wrong** (verified by 105-day backtest), no parameter tuning of v4 will save it; we'll need a different thesis. That's the point: v4 commits to one falsifiable claim.

## Architecture

### Core Loop

```
For each closed 5m bar:
  1. Compute z = (close - SMA(close, lookback)) / std(close, lookback)
  2. If position exists:
       Check exit conditions (TP, SL, time-stop)
  3. If no position AND no cooldown active:
       Check entry conditions (|z| >= entry_threshold + ATR floor + cooldown)
  4. Persist state, log, push TG
```

**Critical: bar-close events only.** No `OnTick`, no `tickManage`, no live-broker poll-based exits. The over-trading in v3 came from tick-level trailing firing 100× more often than bar-level signals. v4 explicitly removes the tick path.

### Components

```
internal/strategy/aistrat_v4/
├── strategy.go      # Strategy interface + OnBar dispatch
├── signal.go        # z-score computation
├── entry.go         # entry decision + order placement
├── exit.go          # TP / SL / time-stop checks + close
├── config.go        # Config struct + registry.Register("ai_v4")
└── strategy_test.go # unit tests for signal math + entry/exit logic
```

**Target size: < 500 lines total** (v3 is > 3000 lines spread across 12 files).

### Data Flow

```
exchange WS kline (5m closed)
  → engine.OnBar(bar)
    → strategy.OnBar(bar, ctx)
      → updateBarBuffer(bar)
      → checkExits(bar) → if exit triggered, ctx.PlaceMarketOrder(close)
      → checkEntry(bar) → if entry triggered, ctx.PlaceMarketOrder(open) [+ exchange-side SL/TP staged orders]
```

No tick subscription. No grid layer state. No regime/mode field on position.

### Position State (single struct, not multi-mode)

```go
type Position struct {
    Side          string    // "LONG" or "SHORT"
    EntryPrice    float64
    EntryBar      int       // bar index at entry, for time-stop
    Qty           float64
    EntryZScore   float64   // for diagnostics
    StopLossPrice float64   // exchange-staged
    TakeProfitPx  float64   // exchange-staged
    ExchangeOrderID string  // protective order group
}
```

Single position only (LONG xor SHORT, not both). No grid orders. No partial TP. No layers.

### Configuration

```yaml
v4:
  Lookback:       20      # SMA / std window (bars). Tune via backtest.
  EntryZScore:    2.5     # |z| threshold to trigger fade. Tune.
  StopZScore:     3.5     # |z| threshold to abort. Tune.
  TimeStopBars:   12      # Force market close after N bars without TP/SL. Tune.
  CooldownBars:   3       # Skip same-side entries for N bars after close. Tune.
  MinATRPct:      0.003   # ATR / price < this → no trades (kill low-vol churn). Tune.
  RiskPerTrade:   0.005   # 0.5% equity at risk per trade.
  Leverage:       2       # Down from 10×. Single-shot doesn't need leverage for sizing.
```

**All thresholds are tune-via-backtest.** The defaults above are starting points based on intuition; real values come from grid-search across 105 days of historical data.

## Exit Logic Detail

A position closes when **any** of these fires (checked in this order at every bar close):

1. **TP**: `z` returns to 0 (price = SMA) → market close
2. **SL**: `|z| >= StopZScore` → market close
3. **Time stop**: `current_bar - EntryBar >= TimeStopBars` → market close

No trailing. No bounce TP. No tech_reversal. No regime conversion. Three rules, period.

Exchange-side SL/TP orders are placed at entry as a backup (if API connection drops, exchange still closes the position at limits). The bar-close logic is the primary path.

## Position Sizing

```
risk_amount = equity * RiskPerTrade        # e.g., 5000 * 0.005 = $25
sl_distance = |entry_price - sl_price|     # depends on current vol
qty = min(
  risk_amount / sl_distance,               # R-based sizing
  equity * MaxPositionPct / entry_price,   # margin cap
)
```

Max loss per trade is hard-capped at `RiskPerTrade × equity` regardless of vol. A bigger SL distance produces a smaller qty, keeping $-risk constant.

## Entry Filters

Two filters, no more:

1. **ATR floor**: skip if `ATR(14) / current_price < MinATRPct` (default 0.3%)
   - Reason: low-vol periods are where v3 burns most fees on noise
2. **Cooldown**: skip same-side entries for `CooldownBars` after a close
   - Reason: prevent the trailing→reopen→trailing death loop that v3 has

Explicitly **NOT** filtered by: regime, 1h EMA direction, RSI, BB position, time of day, day of week, news. The whole point is "pure statistical fade." Filters get added in v4.1 only if backtest shows a specific failure mode they'd help with.

## What Stays vs Goes

| v3 component                          | v4    | Reason                                    |
|---------------------------------------|-------|-------------------------------------------|
| Regime detection                      | ❌    | Frequent false flips, not predictive      |
| Grid layering                         | ❌    | Hides true edge in sizing                 |
| TickManage / tick-level trailing      | ❌    | Source of 100× over-trading               |
| Tech reversal exit                    | ❌    | Net -$1.25 over 6 days, drag              |
| 1h EMA direction filter               | ❌    | Saves 476 in v3, but a band-aid           |
| Bounce TP                             | ❌    | Only fires for grid path                  |
| Fixed-distance grid SL                | ❌    | Conflates layers and SL                   |
| Confidence threshold (0.65–0.95)      | ❌    | Replaced by z-score threshold             |
| Multi-TF (1m + 5m + 15m)              | ❌    | Adds complexity, no proven edge from 1m   |
| GPT scorer                            | ❌    | Already removed in v3 (4/16)              |
| Hedge mode (LONG + SHORT both open)   | ❌    | Single position only                      |
| Session recovery from DB              | ✅    | Keep — operational requirement             |
| TG notifier (notify package)          | ✅    | Keep                                       |
| OMS / order persistence               | ✅    | Keep                                       |
| Engine auto-restart                   | ✅    | Keep                                       |

## Migration Plan

v4 ships as a **separate strategy registered as `ai_v4`** (not a replacement of `ai`). This means:

1. Both can coexist in the binary
2. Live engine keeps running `ai` (v3); we don't disturb production state
3. We start a **second engine** for `ai_v4` on a separate `engine_id` (e.g., `ETHUSDT-5m-ai-v4`)
4. **But**: Binance hedge mode forbids two engines holding same symbol/side simultaneously. So in practice:
   - **Option α** (recommended): Stop `ai` engine on demo, start `ai_v4` on demo. Compare 1-2 weeks of live data.
   - Option β: Run `ai_v4` only in paper mode initially (paper engine ≠ live broker, no exchange conflict)

We pick option α: this is a demo account, the cost of running v4 alongside is just opportunity cost on v3, which is already losing money.

After 2 weeks of live data + acceptable backtest (Sharpe > 0.5, MaxDD < 8%, fee% < 30% of gross PnL), v4 graduates to "default" strategy and v3 is archived.

## Validation Sequence

Before any v4 code goes live:

1. **Unit tests**: signal math (z-score), entry/exit decisions, sizing math
2. **Backtest baseline**: same 105-day period as current `ai` baseline. Required: PF > 1.0, Sharpe > 0
3. **Backtest grid-search**: vary Lookback (10/20/30/50), EntryZScore (2.0/2.5/3.0), StopZScore (3.0/3.5/4.0), TimeStopBars (6/12/24). Pick the combo with best Sharpe AND consistent across windows (not over-fit)
4. **Walk-forward**: split 105 days into 3 windows, optimize on each, test on next. Required: out-of-sample Sharpe > 0.3
5. **Paper trade 3 days** on server (same code path, no real orders)
6. **Live demo 2 weeks** on server, replacing v3
7. **Decision**: if v4 outperforms v3 over 2 weeks (lower DD AND higher net), promote. Otherwise: kill v4, write up findings, try another archetype.

## Architectural Differences vs v3 (summary)

| Dimension       | v3                              | v4                          |
|-----------------|---------------------------------|-----------------------------|
| Modes           | RANGE / SLOW_TREND / STRONG_TREND / EXPANSION | **None**                    |
| Entry signals   | grid + breakout + reversion + MTF | **z-score only**            |
| Exit reasons    | trailing, grid_tp, fixed_sl, tech_reversal, bounce_tp, stop_loss | **TP, SL, time-stop**       |
| Tick events     | tickManage 60s + grid TP + fixed_sl | **None — bar-close only**   |
| Position model  | grid layers + weighted entry + multi-mode | **Single struct, no layers**|
| Code size       | > 3000 lines / 12 files         | **target < 500 lines / 5 files** |
| State machine   | hourlyMode × regime × position mode | **None**                    |
| Filters         | regime + 1h EMA + tech_reversal + bb_width + adverse_momentum + ... | **ATR floor + cooldown**    |
| Leverage        | 10×                             | **2×**                      |
| Concurrent positions | LONG and SHORT simultaneously | **Single position only**    |

## Risks and Open Questions

**Edge thesis may be wrong.** If 105-day backtest of v4 shows PF < 1.0, the thesis is falsified and we revert. This is a feature, not a risk — it's how we learn.

**Time-stop is opinionated.** 12 bars (1 hour) is a guess; backtest will refine. If actual mean-reversion timescale on ETH 5m is 4 hours, time-stop at 12 bars cuts winners short.

**Single position vs multi-leg.** Without the LONG+SHORT hedge mode, we miss some opportunities (e.g., conflicting signals from different TFs). Acceptable in v4 — minimum viable design.

**No 1h trend filter.** Pure statistical fade WILL get hurt during sustained trends. The bet is that the mathematical edge from 100s of trades > the damage from a few bad trend periods. Backtest will tell us.

**Demo ≠ live mainnet.** Demo fills are clean; mainnet has slippage, partial fills, IP throttling, etc. Even after v4 graduates on demo, mainnet deployment requires its own validation phase.

## Out of Scope

- Multi-symbol (BTC + ETH together)
- Multi-timeframe signal fusion (1h confirmation, etc.)
- Funding rate signals
- Order book / microstructure signals
- Anything machine learning related
- Layer / scale-in entries
- Position pyramiding

These are explicitly deferred to v4.1+ if v4 demonstrates baseline edge.
