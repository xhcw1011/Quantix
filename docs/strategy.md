# Quantix AI Trading Strategy V3

ETHUSDT Futures | 10x Leverage | 5m Primary Interval

## Architecture

```
Every 5m bar:
  detectRegime(20 bars) → STRONG_TREND / SLOW_TREND / EXPANSION / RANGE
  ↓
  RANGE → skip (no trading)
  Others → GPT call → signal accumulation → confidence threshold → entry
  ↓
  Tick-level management: SL / trailing / bounce TP / emergency
```

## 1. Regime Detection

Computed every bar from the last 20 primary bars.

| Regime | Condition | Entry Threshold |
|--------|-----------|-----------------|
| EXPANSION | Bar range > ATR*2, body > ATR*1, directional close, trend-aligned, prev bar confirms | 0.65 (with-trend) / 0.82 (counter) |
| STRONG_TREND | trendStrength > 2.5 AND ATR/price > 0.1% | 0.65 (with-trend) / 0.82 (counter) |
| SLOW_TREND | trendStrength > 1.5 AND directionScore > 0.60 | 0.735 (with-trend) / 0.82 (counter) |
| RANGE | Default | No trading (disabled) |

- `trendStrength = |price_now - price_20bars_ago| / ATR`
- `directionScore = % of bars moving with overall direction`
- `trendDir = +1 (bullish) / -1 (bearish) / 0 (neutral)` based on price change vs ATR*0.5

**Key rule: thresholds are directional.** STRONG_TREND bearish lowers SHORT threshold to 0.65, but LONG stays at 0.82. Prevents counter-trend entries.

## 2. Signal Flow

### GPT Call
- Every bar (CallIntervalBars=1) in trend regimes
- Returns `long.confidence`, `short.confidence`, entry prices, reasoning
- Model: gpt-5.4-mini, temperature 0.3, timeout 15s

### Signal Accumulation
- Each GPT call adds `conf - 0.3` to accumLong/accumShort
- Decays by 0.80x every bar (including non-GPT bars)
- Conflicting signals cancel (net only)
- Cap: 1.0
- Effective conf = max(raw GPT, accumulated)
- **Reset to 0 on position close** (prevents stale signals triggering re-entry)

### MTF Scoring (range -5 to +5)
- 15m return (+-2) + 15m EMA structure (+-1) + 5m MACD/RSI (+-1) + 1m change (+-1)
- Score <= -3: block LONG entirely
- Score >= +3: block SHORT entirely
- Score +-2: scale qty by 0.70; +-1: scale by 0.85

### Boost Rules (with-trend only)
- Swing proximity boost: price near swing low/high + conf >= 0.60 → boost to threshold
- MTF momentum boost: MTF score >= +-2 + conf >= 0.50 → boost to threshold

## 3. Entry

### Position Sizing
```
qty = equity * PosSizePct(20%) * Leverage(10) / entryPrice
```
Adjusted by: MTF scale, confidence scale (floor 0.70), margin cap (80% equity).

### Entry Mode
| Regime | Mode |
|--------|------|
| STRONG_TREND / EXPANSION | Market (taker) |
| SLOW_TREND | Limit (offset 0.05%), market if conf >= 0.90 or MTF >= +-3 |
| RANGE | N/A (disabled) |

### SL Calculation
```
SL = entry +- ATR * ATRK(2.0)
R = |entry - SL|
MinSLDistPct = 0.8% floor
MaxRPercent = 1% ceiling
```

## 4. Take Profit (ATR-Adaptive)

TP distances adapt to current volatility:
```
dist = min(R * level, ATR * atrMult)
floor = 0.3 * R (must cover fees)
```

| Mode | TP1 | TP2 | Remaining |
|------|-----|-----|-----------|
| Default | 0.7R / ATR*1.5 (50%) | 1.5R / ATR*3.0 (30%) | 20% trailing |
| Trend (STRONG/EXPANSION) | 0.8R / ATR*1.5 (40%) | 2.0R / ATR*3.0 (30%) | 30% trailing |

**TP1 fill → auto breakeven:** Trailing immediately moves to entry price. Remaining position cannot lose.

## 5. Position Management

### Trailing Stop (tick + bar level, synchronized)
| Phase | Condition | Action |
|-------|-----------|--------|
| Breakeven | pnlR >= BreakevenR (0.80) | SL → entry price |
| ATR Trail | pnlR >= BreakevenR + 0.5 | SL → peak +- ATR * 1.5, floor at entry |

- Trailing only tightens, never widens
- Exchange SL updated via `ReplaceSLOrder` (throttled: max 1 per 3s)
- Tick-level and bar-level use identical config values

### Bounce TP
- After partial TP fill (remainQty < initQty)
- Peak retreat >= 0.8R → close remaining at market
- Requires pnlR > 0 (only for profitable positions)

### Emergency Reversal
- Trigger: pnlR < -0.9, cooldown 60s
- Async GPT call (non-blocking)
- Threshold: ReversalConf (0.75)
- Action: close only (no flip)

### GPT Reversal (bar-level)
- Only when pnlR < 1.0 (don't cut winners)
- Threshold: ReversalConf (0.75)
- Action: close only, let next bar decide new direction

### Post-SL Recovery
- SL fires → postSLReeval flag → next bar runs GPT to update accumulation
- No immediate re-entry on SL bar (stopBar check)
- Next bar: normal regime + entry flow decides new direction

## 6. Risk Management

| Parameter | Value |
|-----------|-------|
| MaxDailyLossPct | 10% |
| MaxConsecLoss | 5 |
| Position size | 20% equity margin |
| SL width | ATR * 2.0 |
| Max R/price | 1% |

## 7. Key Config Defaults

```
ConfidenceThreshold: 0.82   RegimeEntryConf: 0.65
ReversalConf: 0.75          BreakevenR: 0.80
TrendBreakevenR: 0.80       TrailingATRK: 1.5
SignalDecay: 0.80            SignalAccumMax: 1.0
RegimeN: 20                 ATRK: 2.0
PosSizePct: 0.20             Leverage: 10
CallIntervalBars: 1          ATRPeriod: 60
```

## 8. File Structure

| File | Responsibility |
|------|---------------|
| `types.go` | Regime enum, posState, stagedTPRecord |
| `config.go` | All parameters + defaults + registry |
| `signal.go` | OnBar: regime detection, GPT call, accumulation, entry decisions |
| `strategy.go` | OnFill, OnTick: tickManage, emergency reversal, TP fill handling |
| `manage.go` | Bar-level: managePos, manageTrend, checkReversal |
| `entry.go` | openTrend, openHedgeScalp, placeOrder, placeCloseOrder |
| `exit.go` | placeStagedExitOrders, closePos, checkDayReset |
| `helpers.go` | detectRegime, calcATR, recovery, Redis sync |
| `gpt.go` | GPT API, buildContext, signal caching |
