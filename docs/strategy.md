# Quantix AI Trading Strategy V3

ETHUSDT Futures | 10x Leverage | 5m Primary Interval

## Core Design

Two operating modes based on regime detection:

| | STRONG_TREND / EXPANSION | SLOW_TREND |
|---|---|---|
| SL | Swing low/high (wide, survives pullbacks) | ATR × 1.5 (tight) |
| TP | None — trailing rides the trend | 1.0R fixed TP (100% qty) |
| Sizing | Fixed $ risk / R (smaller position) | Fixed $ risk / R (larger position) |
| Breakeven | ATR × 1.5 profit | 0.8R profit |
| Trailing | ATR × 2.5 profit → peak ± ATR × 1.2 | 1.3R profit → peak ± ATR × 1.2 |
| RANGE | No trading (disabled) | — |

## 1. Regime Detection

Every bar, computed from last 20 primary (5m) bars.

```
trendStrength = |price_now - price_20bars_ago| / ATR
directionScore = % of bars moving with overall direction
trendDir = +1 (bullish) / -1 (bearish) / 0 (neutral)
```

| Regime | Condition |
|--------|-----------|
| EXPANSION | Bar range > ATR×2, body > ATR×1, directional, trend-aligned, prev bar confirms |
| STRONG_TREND | trendStrength > 2.5 AND ATR/price > 0.1% |
| SLOW_TREND | trendStrength > 1.5 AND directionScore > 0.60 |
| RANGE | Default → no trading |

## 2. Entry Signal

### GPT Call
- Every bar (CallIntervalBars=1), temperature 0.1, max 200 tokens
- Prompt: scorer role — rates trend_dir quality, counter-trend hard cap 0.40
- Code-level clamp: if trend_dir=-1, longConf capped at 0.40 (vice versa)

### Confidence Thresholds (directional)
| Regime | With-trend | Counter-trend |
|--------|-----------|---------------|
| STRONG_TREND / EXPANSION | 0.65 | 0.82 |
| SLOW_TREND | 0.735 | 0.82 |
| RANGE | disabled | disabled |

### Signal Accumulation
- Each GPT call adds `conf - 0.3` to accumLong/accumShort
- Counter-trend accumulation halved (× 0.5)
- Decay 0.80 per bar, cap 1.0
- Conflicting signals cancel (net only)
- Reset to 0 on position close

### Entry Mode
| Regime | Order Type |
|--------|-----------|
| STRONG_TREND / EXPANSION | Aggressive limit ±0.02% (maker fee) |
| SLOW_TREND | Limit ±0.05%, market if conf ≥ 0.90 |

## 3. Position Sizing

**R-based: fixed dollar risk per trade.**

```
riskAmount = equity × RiskPerTrade (3%)
qty = riskAmount / R
```

- SL wider → smaller position → same $ loss
- SL tighter → larger position → same $ loss
- Safety cap: margin ≤ 60% of equity

Example ($85 equity):
- STRONG_TREND (R=30pt): qty=0.085, loss=$2.55, margin=$19
- SLOW_TREND (R=12pt): qty=0.213, loss=$2.55, margin=$47

## 4. Stop Loss

### STRONG_TREND / EXPANSION
Swing-based: SL below swing low (LONG) / above swing high (SHORT) with ATR×0.5 buffer.
```
LONG:  SL = swingLow_20 - ATR × 0.5
SHORT: SL = swingHigh_20 + ATR × 0.5
Cap:   1.5 ATR ≤ SL distance ≤ 4 ATR
```

### SLOW_TREND
ATR-based: `SL = entry ± ATR × 1.5`

## 5. Take Profit

### STRONG_TREND / EXPANSION
**No TP order.** Trailing stop handles exit. One trade rides the entire trend.

### SLOW_TREND
Single TP at 1.0R (100% qty). ATR-adaptive: `dist = min(1.0R, ATR×3)`. Floor: 0.5R.

## 6. Trailing Stop

### STRONG_TREND / EXPANSION (ATR-based distances)
| Phase | Condition | Action |
|-------|-----------|--------|
| Breakeven | profit ≥ ATR × 1.5 | SL → entry |
| ATR Trail | profit ≥ ATR × 2.5 | SL → peak ± ATR × 1.2 |

### SLOW_TREND (R-based distances)
| Phase | Condition | Action |
|-------|-----------|--------|
| Breakeven | profit ≥ 0.8R | SL → entry |
| ATR Trail | profit ≥ 1.3R | SL → peak ± ATR × 1.2 |

- Trailing only tightens, never widens
- Exchange SL updated via ReplaceSLOrder (throttled: max 1 per 3s)
- Tick-level and bar-level use identical logic

### Bounce TP (SLOW_TREND only, after partial TP fill)
- Peak retreat ≥ 0.8R → close remaining at market

## 7. Exit

### Reversal (bar-level)
- Only when pnlR < 1.0 (don't cut profitable trends)
- conf ≥ 0.75 → close at market, no flip
- After close, same bar can open new direction (lastCallBar reset)

### Emergency (tick-level)
- Trigger: pnlR < -0.9, cooldown 60s
- Async GPT call, threshold = ReversalConf (0.75)
- Close at market, no flip

### All closes use market order (no ghost positions).

## 8. Risk Management

| Parameter | Value |
|-----------|-------|
| RiskPerTrade | 3% of equity |
| MaxDailyLossPct | 10% |
| MaxConsecLoss | 5 |
| Max SL distance | 4 × ATR |
| Max margin | 60% of equity |

## 9. GPT Prompt (scorer role)

```
- trend_dir is PRIMARY signal
- 15m structure CONFIRMS trend_dir
- 5m indicators fine-tune timing
- COUNTER-TREND HARD CAP: 0.40
- Code-level clamp enforces this regardless of GPT output
```

## 10. File Structure

| File | Lines | Responsibility |
|------|-------|---------------|
| types.go | ~70 | Regime enum, posState, stagedTPRecord |
| config.go | ~305 | All parameters + defaults + registry |
| signal.go | ~660 | OnBar: regime, GPT, accumulation, entry |
| strategy.go | ~450 | OnFill, OnTick: trailing, emergency, TP fill |
| manage.go | ~260 | Bar-level: trailing, bounce, reversal |
| entry.go | ~230 | openTrend, sizing, placeOrder |
| exit.go | ~180 | placeStagedExitOrders, closePos |
| helpers.go | ~320 | detectRegime, calcATR, recovery, Redis |
| gpt.go | ~250 | GPT API, buildContext, signal caching |
