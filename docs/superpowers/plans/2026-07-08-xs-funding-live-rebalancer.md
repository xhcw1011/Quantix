# XS-Funding Live Rebalancer (shadow-first) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Build the live rebalancer around the parity-confirmed `internal/xsfunding` core: assemble the point-in-time universe state from the DB, compute the target book + deltas vs current exchange positions, and — in **shadow mode** — log the rotation with zero orders. This is design §7 phase 3 (Shadow), the gate before paper-forward.

**Architecture:** A new `internal/rebalancer` package of small, mostly-pure pieces that reuse the validated `xsfunding` brain and the DB data layer (`data.GetKlinesBetween` + `data.GetFunding`). Order-placement (ORG→broker→exchange) and the scheduler live behind a `Mode` switch; SHADOW logs only. Paper/live execution and pool wiring are follow-on tasks (§ Out of scope). Design: `docs/superpowers/specs/2026-07-07-xs-funding-strategy-design.md`.

**Tech Stack:** Go 1.24; reuses `internal/xsfunding`, `internal/data`, `go.uber.org/zap`. Table-driven tests matching repo style.

---

### Task 1: Signal assembly — DB series → `[]CoinState`

The parity harness assembles CoinState inline (trailing funding, trailing volume, days-listed). Extract that as a tested function so live + backtest share ONE assembly (design §3: "live + backtest read the same source").

**Files:**
- Create: `internal/rebalancer/signal.go`
- Create: `internal/rebalancer/signal_test.go`

- [ ] **Step 1: Failing test**

```go
package rebalancer

import "testing"

func TestBuildStates(t *testing.T) {
	// dates d0..d3; W=2 trailing funding window, volWin=2.
	series := map[string]Series{
		"A": {
			Price:   map[string]float64{"d0": 10, "d1": 11, "d2": 12, "d3": 13},
			Volume:  map[string]float64{"d0": 5e6, "d1": 5e6, "d2": 5e6, "d3": 5e6},
			Funding: map[string]float64{"d0": 0.001, "d1": 0.002, "d2": 0.003, "d3": 0.004},
			First:   "d0",
		},
	}
	dates := []string{"d0", "d1", "d2", "d3"}
	got := BuildStates(series, dates, "d3", 2, 2)
	if len(got) != 1 {
		t.Fatalf("want 1 state, got %d", len(got))
	}
	c := got[0]
	if c.Symbol != "A" || c.Price != 13 {
		t.Fatalf("price/symbol wrong: %+v", c)
	}
	if c.TrailFunding != 0.007 { // d2+d3 = 0.003+0.004
		t.Fatalf("trail funding = %v, want 0.007", c.TrailFunding)
	}
	if c.TrailVolume != 5e6 {
		t.Fatalf("trail vol = %v, want 5e6", c.TrailVolume)
	}
	if c.DaysListed != 3 { // index(d3)=3 - firstIdx(d0)=0
		t.Fatalf("days listed = %v, want 3", c.DaysListed)
	}
}

func TestBuildStatesSkipsMissingPrice(t *testing.T) {
	series := map[string]Series{
		"A": {Price: map[string]float64{"d3": 13}, Volume: map[string]float64{"d3": 5e6},
			Funding: map[string]float64{"d3": 0.001}, First: "d3"},
		"B": {Price: map[string]float64{}, Volume: map[string]float64{},
			Funding: map[string]float64{}, First: "d3"}, // no price on asOf → skipped
	}
	got := BuildStates(series, []string{"d3"}, "d3", 2, 2)
	if len(got) != 1 || got[0].Symbol != "A" {
		t.Fatalf("want only A, got %+v", got)
	}
}
```

- [ ] **Step 2: Verify fail** — `go test ./internal/rebalancer/ -run TestBuildStates` → `undefined: Series/BuildStates`.

- [ ] **Step 3: Implement**

```go
// Package rebalancer builds the live cross-sectional funding rebalancer around the
// parity-confirmed internal/xsfunding brain: assemble point-in-time universe state
// from the DB, target vs current positions, and (shadow) log the rotation.
package rebalancer

import (
	"sort"

	"github.com/Quantix/quantix/internal/xsfunding"
)

// Series is one coin's daily history (date string -> value) plus its first listed date.
type Series struct {
	Price   map[string]float64
	Volume  map[string]float64
	Funding map[string]float64
	First   string // earliest date the coin has data (for DaysListed)
}

// BuildStates assembles the CoinState for each coin as of asOf: trailing funding over
// the last W dates, trailing volume over the last volWin dates, current price, and
// days listed. Coins without a price on asOf are skipped. `dates` must be sorted asc.
func BuildStates(series map[string]Series, dates []string, asOf string, W, volWin int) []xsfunding.CoinState {
	idx := make(map[string]int, len(dates))
	for i, d := range dates {
		idx[d] = i
	}
	ai, ok := idx[asOf]
	if !ok {
		return nil
	}
	syms := make([]string, 0, len(series))
	for s := range series {
		syms = append(syms, s)
	}
	sort.Strings(syms)

	var out []xsfunding.CoinState
	for _, s := range syms {
		sr := series[s]
		price, ok := sr.Price[asOf]
		if !ok || price <= 0 {
			continue
		}
		var tf float64
		for j := ai - W + 1; j <= ai; j++ {
			if j >= 0 {
				tf += sr.Funding[dates[j]]
			}
		}
		var tv float64
		var n int
		for j := ai - volWin + 1; j <= ai; j++ {
			if j >= 0 {
				if v, ok := sr.Volume[dates[j]]; ok {
					tv += v
					n++
				}
			}
		}
		if n > 0 {
			tv /= float64(n)
		}
		days := ai
		if fi, ok := idx[sr.First]; ok {
			days = ai - fi
		}
		out = append(out, xsfunding.CoinState{
			Symbol: s, TrailFunding: tf, Price: price, TrailVolume: tv, DaysListed: days,
		})
	}
	return out
}
```

- [ ] **Step 4: Verify pass** — `go test ./internal/rebalancer/ -run TestBuildStates`.

- [ ] **Step 5: Commit** — `feat(rebalancer): DB-series -> CoinState signal assembly (shared w/ backtest)`

---

### Task 2: Position → signed-notional map

The rebalancer diffs target against **current exchange positions**. Convert an exchange position list into the signed-notional map `xsfunding.Deltas` consumes.

**Files:**
- Create: `internal/rebalancer/translate.go`
- Create: `internal/rebalancer/translate_test.go`

- [ ] **Step 1: Failing test**

```go
package rebalancer

import (
	"math"
	"testing"
)

func TestPositionsToNotional(t *testing.T) {
	// long 0.5 BTC @ 60000 = +30000 ; short 10 SOL @ 150 = -1500
	pos := []Position{
		{Symbol: "BTCUSDT", SignedQty: 0.5, Price: 60000},
		{Symbol: "SOLUSDT", SignedQty: -10, Price: 150},
		{Symbol: "ETHUSDT", SignedQty: 0, Price: 3000}, // flat → omitted
	}
	got := PositionsToNotional(pos)
	if math.Abs(got["BTCUSDT"]-30000) > 1e-9 || math.Abs(got["SOLUSDT"]+1500) > 1e-9 {
		t.Fatalf("notional wrong: %+v", got)
	}
	if _, ok := got["ETHUSDT"]; ok {
		t.Fatalf("flat position must be omitted, got %+v", got)
	}
}
```

- [ ] **Step 2: Verify fail** — `undefined: Position/PositionsToNotional`.

- [ ] **Step 3: Implement**

```go
package rebalancer

// Position is current exchange truth for one symbol (signed qty: + long, − short).
type Position struct {
	Symbol    string
	SignedQty float64
	Price     float64
}

// PositionsToNotional converts positions to the signed-notional map the rebalancer
// diffs against (flat positions omitted).
func PositionsToNotional(pos []Position) map[string]float64 {
	out := make(map[string]float64, len(pos))
	for _, p := range pos {
		if p.SignedQty == 0 {
			continue
		}
		out[p.Symbol] = p.SignedQty * p.Price
	}
	return out
}
```

- [ ] **Step 4: Verify pass.**
- [ ] **Step 5: Commit** — `feat(rebalancer): positions -> signed-notional map`

---

### Task 3: Order notional → concrete trade (side + rounded qty)

`xsfunding.Order` is signed notional. Turn it into a placeable trade: side + qty rounded to the symbol's step, skipping sub-step dust.

**Files:**
- Modify: `internal/rebalancer/translate.go` (append)
- Modify: `internal/rebalancer/translate_test.go` (append)

- [ ] **Step 1: Failing test**

```go
func TestOrdersToTrades(t *testing.T) {
	orders := []xsfunding.Order{
		{Symbol: "BTCUSDT", Notional: 6000},   // buy 0.1 BTC @ 60000
		{Symbol: "SOLUSDT", Notional: -1500},  // sell 10 SOL @ 150
		{Symbol: "XRPUSDT", Notional: 0.4},    // dust < 1 step*price → skipped
	}
	prices := map[string]float64{"BTCUSDT": 60000, "SOLUSDT": 150, "XRPUSDT": 1}
	steps := map[string]float64{"BTCUSDT": 0.001, "SOLUSDT": 1, "XRPUSDT": 1}
	got := OrdersToTrades(orders, prices, steps)
	if len(got) != 2 {
		t.Fatalf("want 2 trades (dust skipped), got %d: %+v", len(got), got)
	}
	if got[0].Symbol != "BTCUSDT" || got[0].Side != "BUY" || math.Abs(got[0].Qty-0.1) > 1e-9 {
		t.Fatalf("BTC trade wrong: %+v", got[0])
	}
	if got[1].Symbol != "SOLUSDT" || got[1].Side != "SELL" || math.Abs(got[1].Qty-10) > 1e-9 {
		t.Fatalf("SOL trade wrong: %+v", got[1])
	}
}
```

- [ ] **Step 2: Verify fail** — `undefined: OrdersToTrades/Trade`.

- [ ] **Step 3: Implement** (append to `translate.go`)

```go
import "math"

// Trade is a concrete placeable order derived from a signed-notional delta.
type Trade struct {
	Symbol string
	Side   string // "BUY" | "SELL"
	Qty    float64
}

// OrdersToTrades converts signed-notional orders into side + step-rounded qty, using
// current prices and each symbol's lot step. Orders that round to zero qty are dropped.
func OrdersToTrades(orders []xsfunding.Order, prices, steps map[string]float64) []Trade {
	var out []Trade
	for _, o := range orders {
		px := prices[o.Symbol]
		if px <= 0 {
			continue
		}
		qty := math.Abs(o.Notional) / px
		if step := steps[o.Symbol]; step > 0 {
			qty = math.Floor(qty/step) * step
		}
		if qty <= 0 {
			continue
		}
		side := "BUY"
		if o.Notional < 0 {
			side = "SELL"
		}
		out = append(out, Trade{Symbol: o.Symbol, Side: side, Qty: qty})
	}
	return out
}
```

- [ ] **Step 4: Verify pass.**
- [ ] **Step 5: Commit** — `feat(rebalancer): signed-notional order -> step-rounded trade`

---

### Task 4: DB loader — `Series` for the universe

Load each universe coin's price/volume/funding series from the DB data layer into `map[string]Series` + the sorted date grid. I/O; a light integration test against the populated DB (skips if empty).

**Files:**
- Create: `internal/rebalancer/loader.go`
- Create: `internal/rebalancer/loader_test.go`

- [ ] **Step 1: Failing test** (integration; skips when DB unreachable/empty)

```go
package rebalancer

import (
	"context"
	"testing"
	"time"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"go.uber.org/zap"
)

func TestLoadSeriesFromDB(t *testing.T) {
	cfg, err := config.Load("../../config/config.yaml")
	if err != nil {
		t.Skip("no config")
	}
	ctx := context.Background()
	store, err := data.New(ctx, cfg.Database.DSN(), zap.NewNop())
	if err != nil {
		t.Skip("no db")
	}
	defer store.Close()
	start, _ := time.Parse("2006-01-02", "2024-07-01")
	end, _ := time.Parse("2006-01-02", "2026-07-08")
	series, dates := LoadSeries(ctx, store, []string{"BTCUSDT", "ETHUSDT"}, start, end)
	if len(series) == 0 || len(dates) == 0 {
		t.Skip("db not populated (run ingest-funding + backfill)")
	}
	if _, ok := series["BTCUSDT"]; !ok {
		t.Fatalf("expected BTCUSDT series")
	}
	if s := series["BTCUSDT"]; len(s.Price) == 0 || len(s.Funding) == 0 {
		t.Fatalf("BTCUSDT series incomplete: px=%d fund=%d", len(s.Price), len(s.Funding))
	}
}
```

- [ ] **Step 2: Verify fail** — `undefined: LoadSeries`.

- [ ] **Step 3: Implement**

```go
package rebalancer

import (
	"context"
	"sort"
	"time"

	"github.com/Quantix/quantix/internal/data"
)

func daykey(t time.Time) string { return t.UTC().Format("2006-01-02") }

// LoadSeries reads 1d klines (close, quote-volume) + funding for each symbol from the
// DB into map[symbol]Series, and returns the sorted union of all dates.
func LoadSeries(ctx context.Context, store *data.Store, symbols []string, start, end time.Time) (map[string]Series, []string) {
	series := make(map[string]Series, len(symbols))
	dateSet := map[string]bool{}
	for _, s := range symbols {
		kl, err := store.GetKlinesBetween(ctx, s, "1d", start, end)
		if err != nil || len(kl) == 0 {
			continue
		}
		price := map[string]float64{}
		vol := map[string]float64{}
		first := daykey(kl[0].OpenTime)
		for _, k := range kl {
			d := daykey(k.OpenTime)
			price[d], vol[d] = k.Close, k.QuoteVolume
			dateSet[d] = true
		}
		fr, _ := store.GetFunding(ctx, s)
		fund := map[string]float64{}
		for _, r := range fr {
			fund[daykey(r.Time)] += r.Rate
		}
		series[s] = Series{Price: price, Volume: vol, Funding: fund, First: first}
	}
	dates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	return series, dates
}
```

- [ ] **Step 4: Verify pass** (or skip if DB empty).
- [ ] **Step 5: Commit** — `feat(rebalancer): DB loader for universe price/volume/funding series`

---

### Task 5: Shadow planner — `PlanRotation` (compute, no orders)

Tie it together into ONE pure planning call: given series+dates+asOf+current positions+config, return the target book, the deltas, and the concrete trades — the exact thing a shadow run logs and a live run would place.

**Files:**
- Create: `internal/rebalancer/plan.go`
- Create: `internal/rebalancer/plan_test.go`

- [ ] **Step 1: Failing test**

```go
package rebalancer

import "testing"

func TestPlanRotation(t *testing.T) {
	// 4 coins on d1; K=1 → long lowest-funding A, short highest-funding D. Flat start.
	series := map[string]Series{
		"A": {Price: map[string]float64{"d0": 100, "d1": 100}, Volume: map[string]float64{"d0": 5e6, "d1": 5e6}, Funding: map[string]float64{"d0": -0.02, "d1": -0.02}, First: "d0"},
		"B": {Price: map[string]float64{"d0": 100, "d1": 100}, Volume: map[string]float64{"d0": 5e6, "d1": 5e6}, Funding: map[string]float64{"d0": 0.00, "d1": 0.00}, First: "d0"},
		"C": {Price: map[string]float64{"d0": 100, "d1": 100}, Volume: map[string]float64{"d0": 5e6, "d1": 5e6}, Funding: map[string]float64{"d0": 0.01, "d1": 0.01}, First: "d0"},
		"D": {Price: map[string]float64{"d0": 100, "d1": 100}, Volume: map[string]float64{"d0": 5e6, "d1": 5e6}, Funding: map[string]float64{"d0": 0.05, "d1": 0.05}, First: "d0"},
	}
	cfg := Config{K: 1, GrossFrac: 1.0, MinDaysListed: 1, MinVolume: 1e6, W: 2, VolWin: 2, MinOrder: 1, Capital: 10000}
	steps := map[string]float64{"A": 1, "B": 1, "C": 1, "D": 1}
	plan := PlanRotation(series, []string{"d0", "d1"}, "d1", map[string]float64{}, cfg, steps)
	byS := map[string]float64{}
	for _, tg := range plan.Targets {
		byS[tg.Symbol] = tg.Notional
	}
	if byS["A"] != 5000 || byS["D"] != -5000 {
		t.Fatalf("targets wrong: %+v", plan.Targets)
	}
	tr := map[string]Trade{}
	for _, x := range plan.Trades {
		tr[x.Symbol] = x
	}
	if tr["A"].Side != "BUY" || tr["A"].Qty != 50 || tr["D"].Side != "SELL" || tr["D"].Qty != 50 {
		t.Fatalf("trades wrong: %+v", plan.Trades)
	}
}
```

- [ ] **Step 2: Verify fail** — `undefined: Config/PlanRotation/Plan`.

- [ ] **Step 3: Implement**

```go
package rebalancer

import "github.com/Quantix/quantix/internal/xsfunding"

// Config parameterizes a rebalance plan.
type Config struct {
	K             int
	GrossFrac     float64
	MinDaysListed int
	MinVolume     float64
	W             int // trailing funding window (dates)
	VolWin        int // trailing volume window (dates)
	MinOrder      float64
	Capital       float64
}

// Plan is one rotation's output: the target book, the notional deltas vs current, and
// the concrete step-rounded trades.
type Plan struct {
	Targets []xsfunding.Target
	Orders  []xsfunding.Order
	Trades  []Trade
}

// PlanRotation computes the full rotation (no side effects). `current` is signed
// notional per symbol; `steps` is each symbol's lot step. Returns an empty Plan when
// the eligible universe is too small to form 2K positions.
func PlanRotation(series map[string]Series, dates []string, asOf string, current map[string]float64, cfg Config, steps map[string]float64) Plan {
	coins := BuildStates(series, dates, asOf, cfg.W, cfg.VolWin)
	longs, shorts := xsfunding.Rank(xsfunding.Eligible(coins, cfg.MinDaysListed, cfg.MinVolume), cfg.K)
	if longs == nil {
		return Plan{}
	}
	targets := xsfunding.BuildTargets(longs, shorts, cfg.Capital, cfg.GrossFrac)
	orders := xsfunding.Deltas(current, targets, cfg.MinOrder)
	prices := map[string]float64{}
	for _, c := range coins {
		prices[c.Symbol] = c.Price
	}
	return Plan{Targets: targets, Orders: orders, Trades: OrdersToTrades(orders, prices, steps)}
}
```

- [ ] **Step 4: Verify pass.**
- [ ] **Step 5: Commit** — `feat(rebalancer): PlanRotation — full shadow rotation (target/deltas/trades)`

---

### Task 6: Shadow command — log the live rotation

A `cmd/xsfunding-shadow` that loads the DB universe as of the latest date, plans the rotation from a flat book (shadow assumes no positions yet), and logs the target book + trades. No exchange, no orders — the design's phase-3 shadow, runnable now against the populated DB.

**Files:**
- Create: `cmd/xsfunding-shadow/main.go`

- [ ] **Step 1** — write `main.go`: `config.Load` → `data.New` → `LoadSeries(universe, 2024-07-01..2026-07-08)` → pick `asOf = last date` → `PlanRotation(..., current=empty, steps=1.0 each)` → print target longs/shorts + funding + trades. (Universe = the validated 50-coin list; step defaults 0 = no rounding for the shadow print.)
- [ ] **Step 2** — `go run ./cmd/xsfunding-shadow/` → prints the current target book (e.g. "LONG: ORDI,JUP,… / SHORT: BTC,ETH,…") with each coin's trailing funding.
- [ ] **Step 3** — sanity: longs are the most-negative funding, shorts the most-positive.
- [ ] **Step 4: Commit** — `feat(cmd): xsfunding-shadow — log live target rotation (no orders)`

---

## Out of scope (follow-on plans)

1. **Scheduler + position syncer (live positions)** — fire every REB days post-funding; read real positions from the exchange as `current` (replace the flat-book assumption).
2. **ORG-gated execution** — `Trades` → ORG (Layer-1) → broker/OMS → exchange; limit-with-timeout → market fallback (cost ≤ ~20bp).
3. **Pool registration** — the strategy as its own market-neutral pool (DD/gross caps, halt=close-only).
4. **Paper-forward harness** — run on the paper broker over weeks; measure realized cost vs the ≤20bp budget.
5. **Step/tick from exchangeInfo** — real lot steps per symbol (shadow uses 1.0/none).

## Self-review

- **Spec coverage:** Universe assembly (design §3 Universe Manager/Data), Ranker/Target (reused from `xsfunding`), Rebalancer delta (§3 Rebalancer, §4 only-trade-delta), position sync as signed notional (§3 syncer), shadow logging (§7 phase 3). Execution/ORG/pool/paper-forward → deferred (§ Out of scope), matching design §7 gates. ✅
- **Placeholder scan:** Tasks 1–5 have complete code + tests; Task 6 is a thin I/O command described step-by-step. ✅
- **Type consistency:** `Series`, `Config`, `Position`, `Trade`, `Plan` defined once and reused; `BuildStates`→`Eligible`→`Rank`→`BuildTargets`→`Deltas`→`OrdersToTrades` chain matches `xsfunding` signatures. ✅
