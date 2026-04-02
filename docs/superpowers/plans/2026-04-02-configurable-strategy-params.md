# Configurable Strategy Parameters Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract 60+ hardcoded trading parameters from `aistrat.go` into `Config` struct fields so they can be configured per-user per-strategy via API/config.

**Architecture:** Expand the existing `aistrat.Config` struct with sub-structs for staged TP, trailing, range management, MTF scoring, and indicator periods. Replace all hardcoded magic numbers with `s.cfg.X` references. Update the registry `init()` to parse new fields from `params`. No database changes needed — params flow through the existing `strategy_params` JSON in the engine start API.

**Tech Stack:** Go, existing `aistrat.Config` + `registry.Register` pattern

---

### Task 1: Staged TP Config — Add fields + replace hardcoded values

**Files:**
- Modify: `internal/strategy/aistrat/aistrat.go`

Currently hardcoded in `placeStagedExitOrders` (lines 676-693) and `checkBreakevenMove` (lines 758-775):
- TP R-multiples: 1.0, 1.5, 2.5, 4.0
- Qty splits: 40%, 30%, 20%, 10%
- Breakeven R threshold: 0.5
- Breakeven buffer: 0.001 (0.1%)

- [ ] **Step 1: Add StagedTP config fields to Config struct**

In `internal/strategy/aistrat/aistrat.go`, after the `// Grid mode` section (line 99), add:

```go
	// Staged TP (trend mode) — exchange-native limit orders
	TPLevels      []float64 // R-multiples for each TP level (default [1.0, 1.5, 2.5, 4.0])
	TPQtySplits   []float64 // fraction of qty for each level (default [0.40, 0.30, 0.20, 0.10])
	BreakevenR    float64   // R threshold to move SL to breakeven (default 0.5)
	BreakevenBuf  float64   // buffer above/below entry for breakeven SL (default 0.001 = 0.1%)
```

- [ ] **Step 2: Set defaults in DefaultConfig**

Add to `DefaultConfig()` return:

```go
		TPLevels: []float64{1.0, 1.5, 2.5, 4.0},
		TPQtySplits: []float64{0.40, 0.30, 0.20, 0.10},
		BreakevenR: 0.5, BreakevenBuf: 0.001,
```

- [ ] **Step 3: Add registry parsing in init()**

After the existing GridQtyRatio parsing (line 53), add:

```go
		if v, ok := params["TPLevels"]; ok {
			switch vv := v.(type) {
			case []float64: cfg.TPLevels = vv
			case []any:
				for _, item := range vv { if f, ok := item.(float64); ok { cfg.TPLevels = append(cfg.TPLevels, f) } }
			}
		}
		if v, ok := params["TPQtySplits"]; ok {
			switch vv := v.(type) {
			case []float64: cfg.TPQtySplits = vv
			case []any:
				for _, item := range vv { if f, ok := item.(float64); ok { cfg.TPQtySplits = append(cfg.TPQtySplits, f) } }
			}
		}
		if v, ok := params["BreakevenR"]; ok { cfg.BreakevenR = toFloat(v) }
		if v, ok := params["BreakevenBuf"]; ok { cfg.BreakevenBuf = toFloat(v) }
```

- [ ] **Step 4: Replace hardcoded values in placeStagedExitOrders**

Replace lines 674-693 with config-driven logic:

```go
	levels := s.cfg.TPLevels
	splits := s.cfg.TPQtySplits
	if len(levels) == 0 || len(splits) == 0 || len(levels) != len(splits) {
		s.log.Error("staged TP: invalid TPLevels/TPQtySplits config")
		return
	}

	tps := make([]strategy.StagedTP, 0, len(levels))
	usedQty := 0.0
	for i, lvl := range levels {
		var tpPrice float64
		if pos.side == "LONG" {
			tpPrice = math.Round((entry+lvl*R)*100) / 100
		} else {
			tpPrice = math.Round((entry-lvl*R)*100) / 100
		}
		var q float64
		if i == len(levels)-1 {
			q = qty - usedQty // remainder
			q = math.Floor(q*1000) / 1000
			if q <= 0 { q = 0.001 }
		} else {
			q = math.Floor(qty*splits[i]*1000) / 1000
		}
		usedQty += q
		tps = append(tps, strategy.StagedTP{Price: tpPrice, Qty: q})
	}
```

- [ ] **Step 5: Replace hardcoded values in checkBreakevenMove**

Replace `pnlR < 0.5` with `pnlR < s.cfg.BreakevenR` and `p.entryPrice*0.001` with `p.entryPrice*s.cfg.BreakevenBuf`.

- [ ] **Step 6: Build and verify**

Run: `go build ./...`

- [ ] **Step 7: Commit**

```bash
git add internal/strategy/aistrat/aistrat.go
git commit -m "feat: make staged TP levels, qty splits, breakeven threshold configurable"
```

---

### Task 2: Trend Trailing Config — Extract all trailing parameters

**Files:**
- Modify: `internal/strategy/aistrat/aistrat.go`

Currently hardcoded in `manageTrend` fallback (lines 1371-1392) and `openTrend` (line 954):
- baseTrailPct: 0.012, low-vol: 0.008, high-vol: 0.015
- ATR vol thresholds: 0.005, 0.01
- Trail tightening at 1.0R, 1.5R, 2.0R with multipliers 1.0, 0.65, 0.40
- Trail floor: 0.005
- Min SL distance: 0.008

- [ ] **Step 1: Add trailing config fields**

After the StagedTP fields in Config, add:

```go
	// Trailing stop (trend fallback when staged orders unavailable)
	TrailBasePct    float64 // base trailing % (default 0.012 = 1.2%)
	TrailLowVolPct  float64 // trailing % for low volatility (default 0.008)
	TrailHighVolPct float64 // trailing % for high volatility (default 0.015)
	TrailFloorPct   float64 // absolute minimum trailing distance % (default 0.005)
	MinSLDistPct    float64 // minimum SL distance from entry (default 0.008 = 0.8%)
	ReversalConf    float64 // confidence threshold for GPT reversal exit (default 0.72)
	MarketEntryConf float64 // confidence threshold for immediate market entry (default 0.90)
```

- [ ] **Step 2: Set defaults**

```go
		TrailBasePct: 0.012, TrailLowVolPct: 0.008, TrailHighVolPct: 0.015,
		TrailFloorPct: 0.005, MinSLDistPct: 0.008,
		ReversalConf: 0.72, MarketEntryConf: 0.90,
```

- [ ] **Step 3: Add registry parsing**

```go
		if v, ok := params["TrailBasePct"]; ok { cfg.TrailBasePct = toFloat(v) }
		if v, ok := params["TrailLowVolPct"]; ok { cfg.TrailLowVolPct = toFloat(v) }
		if v, ok := params["TrailHighVolPct"]; ok { cfg.TrailHighVolPct = toFloat(v) }
		if v, ok := params["TrailFloorPct"]; ok { cfg.TrailFloorPct = toFloat(v) }
		if v, ok := params["MinSLDistPct"]; ok { cfg.MinSLDistPct = toFloat(v) }
		if v, ok := params["ReversalConf"]; ok { cfg.ReversalConf = toFloat(v) }
		if v, ok := params["MarketEntryConf"]; ok { cfg.MarketEntryConf = toFloat(v) }
```

- [ ] **Step 4: Replace in manageTrend**

Replace `baseTrailPct := 0.012` with `baseTrailPct := s.cfg.TrailBasePct`, etc. for all ATR threshold comparisons and trail floor.

- [ ] **Step 5: Replace in openTrend**

Replace `entryPrice * 0.008` with `entryPrice * s.cfg.MinSLDistPct`.

- [ ] **Step 6: Replace in checkReversal and entry logic**

Replace `0.72` reversal threshold and `0.90` market entry threshold with `s.cfg.ReversalConf` and `s.cfg.MarketEntryConf`.

- [ ] **Step 7: Build and commit**

```bash
go build ./...
git add internal/strategy/aistrat/aistrat.go
git commit -m "feat: make trailing stop, SL distance, and confidence thresholds configurable"
```

---

### Task 3: Range Mode Config — Extract range management parameters

**Files:**
- Modify: `internal/strategy/aistrat/aistrat.go`

Currently hardcoded in `manageRange` (lines 1155-1233):
- Breakeven at 0.3%, lock at 0.6%, lock offset 0.3%
- Trailing at 0.8%, trail distance 0.3%
- Timeout: profit 60m, loss 20m, flat 30m
- BB width clamp: 0.006 - 0.015
- EMA convergence threshold: 0.003

- [ ] **Step 1: Add range config fields**

```go
	// Range position management
	RangeBEPct      float64       // PnL % to move SL to breakeven (default 0.003)
	RangeLockPct    float64       // PnL % to lock in partial profit (default 0.006)
	RangeLockOffset float64       // profit lock offset % (default 0.003)
	RangeTrailPct   float64       // PnL % to start trailing (default 0.008)
	RangeTrailDist  float64       // trailing distance % (default 0.003)
	RangeProfitTimeout time.Duration // timeout for profitable range position (default 60m)
	RangeLossTimeout   time.Duration // timeout for losing range position (default 20m)
	RangeFlatTimeout   time.Duration // timeout for flat range position (default 30m)
	BBWidthMin      float64       // min BB width for range TP (default 0.006)
	BBWidthMax      float64       // max BB width for range TP (default 0.015)
	RangeEMAConv    float64       // EMA convergence threshold for range detection (default 0.003)
```

- [ ] **Step 2: Set defaults in DefaultConfig**

```go
		RangeBEPct: 0.003, RangeLockPct: 0.006, RangeLockOffset: 0.003,
		RangeTrailPct: 0.008, RangeTrailDist: 0.003,
		RangeProfitTimeout: 60*time.Minute, RangeLossTimeout: 20*time.Minute, RangeFlatTimeout: 30*time.Minute,
		BBWidthMin: 0.006, BBWidthMax: 0.015, RangeEMAConv: 0.003,
```

- [ ] **Step 3: Add registry parsing**

```go
		if v, ok := params["RangeBEPct"]; ok { cfg.RangeBEPct = toFloat(v) }
		if v, ok := params["RangeLockPct"]; ok { cfg.RangeLockPct = toFloat(v) }
		if v, ok := params["RangeLockOffset"]; ok { cfg.RangeLockOffset = toFloat(v) }
		if v, ok := params["RangeTrailPct"]; ok { cfg.RangeTrailPct = toFloat(v) }
		if v, ok := params["RangeTrailDist"]; ok { cfg.RangeTrailDist = toFloat(v) }
		if v, ok := params["RangeProfitTimeout"]; ok { cfg.RangeProfitTimeout = time.Duration(toFloat(v)) * time.Minute }
		if v, ok := params["RangeLossTimeout"]; ok { cfg.RangeLossTimeout = time.Duration(toFloat(v)) * time.Minute }
		if v, ok := params["RangeFlatTimeout"]; ok { cfg.RangeFlatTimeout = time.Duration(toFloat(v)) * time.Minute }
		if v, ok := params["BBWidthMin"]; ok { cfg.BBWidthMin = toFloat(v) }
		if v, ok := params["BBWidthMax"]; ok { cfg.BBWidthMax = toFloat(v) }
		if v, ok := params["RangeEMAConv"]; ok { cfg.RangeEMAConv = toFloat(v) }
```

- [ ] **Step 4: Replace all hardcoded values in manageRange and isRangeRegime**

Replace every `0.003`, `0.006`, `0.008`, `0.003` in manageRange with `s.cfg.RangeBEPct`, `s.cfg.RangeLockPct`, etc. Replace timeout literals with config durations. Replace BB width clamps and EMA convergence threshold.

- [ ] **Step 5: Build and commit**

```bash
go build ./...
git add internal/strategy/aistrat/aistrat.go
git commit -m "feat: make range mode management parameters configurable"
```

---

### Task 4: MTF Scoring Config — Extract scoring thresholds

**Files:**
- Modify: `internal/strategy/aistrat/aistrat.go`

Currently hardcoded in signal evaluation (lines 411-465, 474-483):
- 15m return thresholds: 0.01, 0.002
- RSI thresholds: 60, 40
- 1m return thresholds: 0.001
- Qty scale factors: 0.70, 0.85
- Swing proximity: 0.0015
- Swing boost conf: 0.82

- [ ] **Step 1: Add MTF config fields**

```go
	// MTF scoring
	MTFStrongTrend  float64 // 15m return threshold for strong trend (default 0.01)
	MTFWeakTrend    float64 // 15m return threshold for weak trend (default 0.002)
	MTFMomentumRSI  [2]float64 // RSI bull/bear thresholds (default [60, 40])
	MTF1mThreshold  float64 // 1m return threshold (default 0.001)
	MTFQtyScaleHard float64 // qty scale for strong headwind (default 0.70)
	MTFQtyScaleSoft float64 // qty scale for mild headwind (default 0.85)
	SwingProximity  float64 // swing high/low proximity % (default 0.0015)
```

- [ ] **Step 2: Set defaults**

```go
		MTFStrongTrend: 0.01, MTFWeakTrend: 0.002,
		MTFMomentumRSI: [2]float64{60, 40},
		MTF1mThreshold: 0.001,
		MTFQtyScaleHard: 0.70, MTFQtyScaleSoft: 0.85,
		SwingProximity: 0.0015,
```

- [ ] **Step 3: Add registry parsing**

```go
		if v, ok := params["MTFStrongTrend"]; ok { cfg.MTFStrongTrend = toFloat(v) }
		if v, ok := params["MTFWeakTrend"]; ok { cfg.MTFWeakTrend = toFloat(v) }
		if v, ok := params["MTF1mThreshold"]; ok { cfg.MTF1mThreshold = toFloat(v) }
		if v, ok := params["MTFQtyScaleHard"]; ok { cfg.MTFQtyScaleHard = toFloat(v) }
		if v, ok := params["MTFQtyScaleSoft"]; ok { cfg.MTFQtyScaleSoft = toFloat(v) }
		if v, ok := params["SwingProximity"]; ok { cfg.SwingProximity = toFloat(v) }
```

- [ ] **Step 4: Replace hardcoded values in signal evaluation**

Replace all MTF scoring magic numbers with config references.

- [ ] **Step 5: Build and commit**

```bash
go build ./...
git add internal/strategy/aistrat/aistrat.go
git commit -m "feat: make MTF scoring thresholds and qty scaling configurable"
```

---

### Task 5: Indicator Periods Config

**Files:**
- Modify: `internal/strategy/aistrat/aistrat.go`

Currently hardcoded across buildContext and calcATR:
- RSI: 14, MACD: 12/26/9, EMA: 20/50, BB: 20/2.0, ATR: 60, Volume MA: 20

- [ ] **Step 1: Add indicator config fields**

```go
	// Technical indicator periods
	RSIPeriod      int     // RSI lookback (default 14)
	MACDFast       int     // MACD fast EMA (default 12)
	MACDSlow       int     // MACD slow EMA (default 26)
	MACDSignal     int     // MACD signal line (default 9)
	EMAFast        int     // fast EMA period (default 20)
	EMASlow        int     // slow EMA period (default 50)
	BBPeriod       int     // Bollinger Bands period (default 20)
	BBStdDev       float64 // BB standard deviation multiplier (default 2.0)
	ATRPeriod      int     // ATR lookback for position sizing (default 60)
	VolMAPeriod    int     // Volume MA period (default 20)
```

- [ ] **Step 2: Set defaults**

```go
		RSIPeriod: 14, MACDFast: 12, MACDSlow: 26, MACDSignal: 9,
		EMAFast: 20, EMASlow: 50, BBPeriod: 20, BBStdDev: 2.0,
		ATRPeriod: 60, VolMAPeriod: 20,
```

- [ ] **Step 3: Add registry parsing**

```go
		if v, ok := params["RSIPeriod"]; ok { cfg.RSIPeriod = toInt(v) }
		if v, ok := params["MACDFast"]; ok { cfg.MACDFast = toInt(v) }
		if v, ok := params["MACDSlow"]; ok { cfg.MACDSlow = toInt(v) }
		if v, ok := params["MACDSignal"]; ok { cfg.MACDSignal = toInt(v) }
		if v, ok := params["EMAFast"]; ok { cfg.EMAFast = toInt(v) }
		if v, ok := params["EMASlow"]; ok { cfg.EMASlow = toInt(v) }
		if v, ok := params["BBPeriod"]; ok { cfg.BBPeriod = toInt(v) }
		if v, ok := params["BBStdDev"]; ok { cfg.BBStdDev = toFloat(v) }
		if v, ok := params["ATRPeriod"]; ok { cfg.ATRPeriod = toInt(v) }
		if v, ok := params["VolMAPeriod"]; ok { cfg.VolMAPeriod = toInt(v) }
```

- [ ] **Step 4: Replace all hardcoded indicator calls**

In `buildContext`: replace `indicator.RSI(closes, 14)` with `indicator.RSI(closes, s.cfg.RSIPeriod)`, etc. for MACD, EMA, BB, Volume MA. In `calcATR`: replace `60` with `s.cfg.ATRPeriod`. In `OnFill` range BB: replace `20` and `2.0` with config values.

- [ ] **Step 5: Build and commit**

```bash
go build ./...
git add internal/strategy/aistrat/aistrat.go
git commit -m "feat: make all technical indicator periods configurable"
```

---

### Task 6: Misc Entry/Exit Config — remaining scattered values

**Files:**
- Modify: `internal/strategy/aistrat/aistrat.go`

Remaining hardcoded values:
- Entry offset: 0.0013 (0.13%)
- Max GPT entry deviation: 0.005 (0.5%)
- Limit order timeout: 2 bars
- Min hold period: 3 bars
- Min trend bars: 5
- Close order price offset: 0.01
- GPT temperature: 0.3, max tokens: 400, timeout: 15s

- [ ] **Step 1: Add remaining config fields**

```go
	// Entry/exit tuning
	EntryOffsetPct   float64       // limit entry offset from current price (default 0.0013)
	MaxEntryDevPct   float64       // max GPT entry deviation from spot (default 0.005)
	LimitTimeoutBars int           // bars to wait for limit fill (default 2)
	MinHoldBars      int           // minimum bars before TP/SL checks (default 3)
	MinTrendBars     int           // minimum bars before trend management (default 5)
	CloseOffsetPct   float64       // limit close price offset (default 0.01 in absolute terms → 0.0)

	// GPT tuning
	GPTTemperature   float64       // GPT temperature (default 0.3)
	GPTMaxTokens     int           // GPT max completion tokens (default 400)
	GPTTimeout       time.Duration // GPT API call timeout (default 15s)
```

- [ ] **Step 2: Set defaults and add parsing**

```go
		EntryOffsetPct: 0.0013, MaxEntryDevPct: 0.005,
		LimitTimeoutBars: 2, MinHoldBars: 3, MinTrendBars: 5,
		GPTTemperature: 0.3, GPTMaxTokens: 400, GPTTimeout: 15*time.Second,
```

- [ ] **Step 3: Replace all remaining hardcoded values**

Replace in signal processing, managePos, manageTrend, placeCloseOrder, and callGPT.

- [ ] **Step 4: Build and commit**

```bash
go build ./...
git add internal/strategy/aistrat/aistrat.go
git commit -m "feat: make entry/exit tuning and GPT parameters configurable"
```

---

### Task 7: WS + Engine System Config

**Files:**
- Modify: `internal/exchange/binance_futures_ws.go`
- Modify: `internal/exchange/binance_ws.go`
- Modify: `internal/config/config.go`

Move WS constants to config.

- [ ] **Step 1: Add WSConfig to config.go**

```go
type WSConfig struct {
	StaleTimeout     time.Duration `mapstructure:"stale_timeout"`      // default 30s
	StaleCheckInterval time.Duration `mapstructure:"stale_check_interval"` // default 3s
	ReconnectDelay   time.Duration `mapstructure:"reconnect_delay"`    // default 2s
	OpenErrorDelay   time.Duration `mapstructure:"open_error_delay"`   // default 5s
}
```

Add `WS WSConfig `mapstructure:"ws"`` to root Config struct.

- [ ] **Step 2: Apply defaults in config loading**

- [ ] **Step 3: Pass config to WS clients, replace package-level constants**

- [ ] **Step 4: Build and commit**

```bash
go build ./...
git add internal/exchange/binance_futures_ws.go internal/exchange/binance_ws.go internal/config/config.go
git commit -m "feat: make WebSocket reconnection timeouts configurable"
```

---

### Task 8: Final verification

- [ ] **Step 1: Full build**

Run: `go build ./...`

- [ ] **Step 2: Run all tests**

Run: `go test ./...`
Expected: only pre-existing `TestLiveBroker_DuplicateOrderBlocked` failure

- [ ] **Step 3: Verify no remaining magic numbers**

Grep for common magic number patterns to confirm none were missed:

```bash
grep -n '0\.008\b\|0\.012\b\|0\.015\b\|0\.003\b\|0\.006\b\|0\.72\b\|0\.90\b' internal/strategy/aistrat/aistrat.go
```

Expected: only config default values and unrelated constants.
