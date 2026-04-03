# AI Strategy Logic — Complete Signal Chain

## Overview

GPT-powered dual-mode trading strategy for crypto futures (ETH/USDT). Multi-timeframe analysis: 5m (entry) + 15m (trend). Supports Trend mode (let profits run) and Range mode (quick scalp).

---

## 1. Signal Acquisition

Every 5m bar → call GPT with market context:

**Input to GPT:**
- 5m indicators: RSI, MACD, EMA20/50, BB, ATR, volume ratio, swing high/low
- 15m indicators: RSI, MACD, EMA20/50, return_8bar, **structure** (pre-computed: 1=bullish, -1=bearish, 0=range)
- Last 10 bars OHLCV
- Current position status

**Output from GPT:**
```json
{
  "long":  {"confidence": 0.0-1.0, "entry_price": ..., "reasoning": "..."},
  "short": {"confidence": 0.0-1.0, "entry_price": ..., "reasoning": "..."}
}
```

**GPT Prompt Rules:**
- structure = -1 (bearish) → long conf < 0.50 (except early reversal: up to 0.70)
- structure = 1 (bullish) → short conf < 0.50 (except early reversal: up to 0.70)
- Early reversal exception: price moved >2% against structure + 5m momentum shifting
- Reasoning kept under 2 sentences (prevents JSON truncation)

---

## 2. Multi-Timeframe Scoring (MTF)

**Score range: -5 to +5. Positive = bullish, negative = bearish.**

| Component | Range | Logic |
|-----------|-------|-------|
| 15m return_8bar | ±2 | >+1% → +2, >+0.2% → +1, <-1% → -2, <-0.2% → -1 |
| 15m EMA structure | ±1 | EMA20 > EMA50 → +1, EMA20 < EMA50 → -1 |
| 5m MACD + RSI (AND) | ±1 | MACD>0 AND RSI>40 → +1, MACD<0 AND RSI<60 → -1 |
| 1m 3-bar change | ±1 | >+0.1% → +1, <-0.1% → -1 |

**MTF effects on signals:**

| Score | LONG effect | SHORT effect |
|-------|-------------|--------------|
| ≤ -3 | **BLOCKED** (conf=0) | Full size |
| -2 | 70% size | Full size |
| -1 | 85% size | Full size |
| 0 | Full size | Full size |
| +1 | Full size | 85% size |
| +2 | Full size | 70% size |
| ≥ +3 | Full size | **BLOCKED** (conf=0) |

---

## 3. Boost Mechanisms

All boosts require **conf ≥ 0.60** (prevents low-quality signals from being forced through). Blocked signals (conf=0) cannot be boosted.

### Swing Boost
- Price within 0.15% of swing_low → LONG conf = 0.82 (if MTF ≥ -1)
- Price within 0.15% of swing_high → SHORT conf = 0.82 (if MTF ≤ +1)

### MTF Momentum Boost
- MTF ≥ +2 AND LONG conf ≥ 0.60 → LONG conf = 0.82
- MTF ≤ -2 AND SHORT conf ≥ 0.60 → SHORT conf = 0.82

---

## 4. Mode Selection

```
isRangeRegime (EMA20 ≈ EMA50, convergence < 0.3%)
  │
  ├─ MTF |score| ≥ 2 AND ATR expanding (> 20bar avg × 1.2)
  │   → Trend mode (let profits run)
  │
  ├─ MTF |score| ≥ 2 AND ATR NOT expanding
  │   → Range mode (oscillation with high MTF — don't force Trend)
  │
  └─ MTF |score| < 2
      → Range mode

NOT isRangeRegime → Trend mode
```

**Range direction filter:** Range LONG requires MTF ≥ 0. Range SHORT requires MTF ≤ 0. Counter-MTF Range is skipped.

**Dynamic mode upgrade:** During position lifecycle, Range → Trend if MTF becomes ≥ 2 and ATR starts expanding.

---

## 5. Entry Price Selection

| Condition | Entry logic |
|-----------|-------------|
| Normal | GPT entry if better than offset (±0.13%); otherwise offset |
| High conf (≥ 0.90) + GPT entry worse than price | Market price |
| Strong MTF (|score| ≥ 3) + GPT entry > 0.2% away | Cap at price ± 0.2% |
| Pending order exists + new entry closer to price | Cancel old, place new |
| Flip (reversal) | Market price (immediate direction change) |

---

## 6. Range Mode Exit

| Exit | Condition | Notes |
|------|-----------|-------|
| **TP** | price hits `min(BB×0.6, ATR×1.0, entry×0.8%)` | Triple cap prevents unreachable TP |
| **SL** | price hits `TP×1.5` (max 1%) | Always 1:1.5 R/R vs actual TP |
| **Trailing** | +0.4% profit → start trailing, 0.3% distance | Locks in profit |
| **TP + momentum** | TP hit + 5m momentum strong → upgrade to Trend | Don't exit early in breakouts |
| **Safety timeout** | 4 hours max hold | Prevents overnight funding rate drain |

---

## 7. Trend Mode Exit

### Exchange-Native (Staged TP)
Placed immediately after entry fill. Exchange executes automatically — no dependency on local code.

| Level | R-multiple | Qty % | Purpose |
|-------|-----------|-------|---------|
| TP1 | +1.0R | 40% | Recover 2× risk → "free position" |
| TP2 | +1.5R | 30% | Lock profit, 70% total closed |
| TP3 | +2.5R | 20% | Trend confirmed |
| TP4 | +4.0R | 10% | "Lottery ticket" |
| SL | ATR×ATRK (oscillation cap 1%) | 100% | Exchange algo order |

### ~~Breakeven Move~~ (Disabled)
- Removed — moving SL to breakeven causes premature exits in oscillation
- SL stays at original position; staged TP handles profit-taking

### GPT Reversal
- Every N bars → call GPT for reversal check
- Reversal conf ≥ 0.72 → close position
- Reversal conf ≥ 0.77 → **flip** (close + market open reverse direction)

### Fallback Trailing (paper/backtest/recovered positions)
- Adaptive ATR-based trailing with profit-level tightening
- Below +1.0R: wide (ATR × TrailingATRK)
- +1.0R ~ +1.5R: moderate (baseTrailPct)
- +1.5R ~ +2.0R: tight (65% of base)
- Above +2.0R: aggressive (40% of base)

---

## 8. Position Recovery

### Startup
```
Redis → load positions
Exchange → GetMarginRatios → confirm
  API returns empty → keep Redis data (may be API lag)
  API confirms position → keep
  API says no position + has other data → clear (phantom)
```

### Recovered Trend Position
- `stagedTPPlaced = false` → auto-place staged TP on first bar
- R > ATR × 2 → cap R to ATR × 1.5 (prevents stale R from inflating TP distances)

### External Close Detection
```
Exchange SL/manual close → WS fill (unmatched)
  → SyncFromExchange → syncer detects position gone
  → Redis cleared → PositionClosedExternally flag set
  → Next 1m bar → strategy reads flag → immediate GPT call
  → May open reverse direction
```

---

## 9. Hedge Mode (Default: OFF)

Trigger conditions (all must be true):
- `HedgeOnDrawdown = true`
- Main position drawdown ≥ 0.5%
- AI counter-signal conf ≥ 0.82
- Cooldown ≥ 15 minutes since last hedge close

Hedge position:
- Forced Range mode
- 30% of main position qty
- TP = min(1U equivalent, mainSL_distance × 50%)
- SL = ATR × 1.5

---

## 10. Config Defaults

### Core
| Param | Default | Description |
|-------|---------|-------------|
| ConfidenceThreshold | 0.82 | Min conf to open position |
| ReversalConf | 0.72 | Min conf to close on reversal |
| MarketEntryConf | 0.90 | Min conf for market (vs limit) entry |
| CallIntervalBars | 1 | GPT call frequency |
| RiskPerTrade | 0.02 | 2% equity per trade |
| ATRK | 4.0 | SL = ATR × this |

### Range
| Param | Default | Description |
|-------|---------|-------------|
| RangeTPPct | 0.012 | BB-based TP (before triple cap) |
| RangeSLPct | 0.01 | Max SL (ATR×1.5 preferred) |
| RangeTrailPct | 0.004 | Start trailing at +0.4% |
| RangeTrailDist | 0.003 | Trailing distance 0.3% |
| RangeBEPct | 0.003 | Move SL to BE at +0.3% |

### Staged TP
| Param | Default | Description |
|-------|---------|-------------|
| TPLevels | [1.0, 1.5, 2.5, 4.0] | R-multiples |
| TPQtySplits | [0.40, 0.30, 0.20, 0.10] | Qty fractions |
| BreakevenR | 0.5 | R threshold for BE move |
| BreakevenBuf | 0.001 | Min BE buffer (0.1%) |

### MTF
| Param | Default | Description |
|-------|---------|-------------|
| MTFStrongTrend | 0.01 | 15m return for strong trend |
| MTFWeakTrend | 0.002 | 15m return for weak trend |
| MTFBullRSI / MTFBearRSI | 60 / 40 | RSI thresholds |
| MTFQtyScaleHard / Soft | 0.70 / 0.85 | Position size reduction |
