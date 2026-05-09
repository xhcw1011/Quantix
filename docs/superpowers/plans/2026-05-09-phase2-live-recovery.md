# Phase 2 Implementation Plan — Composite Live Capability + Recovery

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development.

**Goal:** Composite strategy runs in `cmd/api` live engine on demo, survives restart with state preserved. Plus close the Phase 1 backtest loop (SimBroker honors StopLoss → OnFill fires → posQty resets → next entry possible).

**Architecture:** Mirror aistrat's lifecycle hooks (read `ctx.Extra` on first warmup bar to get store/syncer/userID/engineID). Persist minimal Composite state to Redis. SimBroker tracks active stops per position and simulates close fills on price cross.

**Tech Stack:** Go 1.24, existing PositionSyncer, existing Redis, existing live engine. NO new infrastructure.

**Honest framing:**
- Phase 1 produced 1 trade in 24 days because SimBroker doesn't trigger SL → no exit → alignment gate blocks all subsequent entries. **Task 2.4 is the gating fix.** Without it, Alpha Exploration backtests are noise.
- Phase 2 is plumbing only. Still no alpha improvement. Profitability remains a Phase 3 / Decision Gate question.

---

## Roadmap

| Task | Scope | Files | Est |
|---|---|---|---|
| 2.1 | Composite reads `ctx.Extra` on first bar (store, syncer, user_id, engine_id) | `composite/recovery.go` (new) | 0.5d |
| 2.2 | Persist posQty to Redis on OnFill | `composite/recovery.go` | 0.5d |
| 2.3 | Recover posQty from Redis on warmup | `composite/recovery.go` | 0.5d |
| 2.4 | **SimBroker StopLoss trigger** — simulate close fill when bar crosses SL | `internal/backtest/broker.go` (modify) | 1d |
| 2.5 | Deploy composite to demo server with new engine_id; verify systemd restart preserves state | server cron + `engine_sessions` row | 0.5d |

**Total:** ~3 days

**Acceptance criteria:**
1. `cmd/backtest -strategy composite` over 24 days produces ≥10 trades (was 1 in Phase 1)
2. Demo composite runs alongside aistrat (different engine_id, no collision)
3. After `sudo systemctl restart quantix-api`, composite resumes with same posQty (verified via Redis dump pre/post)
4. aistrat untouched

**Kill criteria:** if Task 2.4 fix produces no measurable change in backtest trade count, the issue is elsewhere (alignment gate? alpha unable to fire reverse signals?). Investigate before proceeding to 2.5.

---

## Task 2.1: Wire Composite to Live Engine Context

**Files:**
- Create: `internal/strategy/composite/recovery.go`
- Modify: `internal/strategy/composite/strategy.go` — add `firstBarSeen` flag + invocation of setup

**Goal:** On the first bar after engine start, read live-engine dependencies from `ctx.Extra`. Mirror aistrat's pattern.

Reference: `internal/strategy/aistrat/signal.go:50-66` shows the pattern.

- [ ] **Step 1: Failing test for setup**

```go
// internal/strategy/composite/recovery_test.go
package composite

import (
	"testing"

	"github.com/Quantix/quantix/internal/alpha"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
)

func TestSetupReadsContextExtras(t *testing.T) {
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})

	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, &fakeBroker{}, zap.NewNop())
	ctx.Extra["user_id"] = 42
	ctx.Extra["engine_id"] = "test-engine"

	for _, b := range makeBars(70, 2300) {
		s.OnBar(ctx, b)
	}

	if s.userID != 42 {
		t.Fatalf("userID=%d want 42 (setup didn't read ctx.Extra)", s.userID)
	}
	if s.engineID != "test-engine" {
		t.Fatalf("engineID=%q want test-engine", s.engineID)
	}
}
```

- [ ] **Step 2: Run, verify FAIL** — fields don't exist

- [ ] **Step 3: Add fields + setup func**

In `strategy.go`, add to `Strategy` struct:
```go
type Strategy struct {
	cfg          Config
	alphas       []Alpha
	bars         []exchange.Kline
	posQty       float64
	firstBarSeen bool
	userID       int
	engineID     string
}
```

In `recovery.go`:
```go
package composite

import (
	"github.com/Quantix/quantix/internal/strategy"
)

// setupFromContext reads live-engine dependencies from ctx.Extra on the
// first bar. Backtest contexts have empty Extra — that's fine.
func (s *Strategy) setupFromContext(ctx *strategy.Context) {
	if v, ok := ctx.Extra["user_id"].(int); ok {
		s.userID = v
	}
	if v, ok := ctx.Extra["engine_id"].(string); ok {
		s.engineID = v
	}
}
```

In `OnBar`, at the top after `s.bars = append(...)`, add:
```go
if !s.firstBarSeen {
	s.setupFromContext(ctx)
	s.firstBarSeen = true
}
```

- [ ] **Step 4: Verify test passes**

- [ ] **Step 5: Commit:** `feat(composite): read user_id/engine_id from ctx.Extra on warmup`

---

## Task 2.2: Persist posQty to Redis on OnFill

**Files:**
- Modify: `internal/strategy/composite/recovery.go` — add `persistState`
- Modify: `internal/strategy/composite/strategy.go` — call `persistState` from `OnFill`
- Modify: `internal/strategy/composite/strategy.go` — `Strategy` struct adds `rdb *redis.Client`

**Storage:** Single Redis key per engine: `quantix:composite:{engine_id}:state` → JSON `{posQty, updatedAt}`. Phase 2 keeps state minimal — bars are recomputable from kline backfill, alphas are stateless.

- [ ] **Step 1: Failing test**

```go
func TestOnFillPersistsToRedis(t *testing.T) {
	t.Skip("requires redis miniredis or live redis — implement after step 3")
	// Placeholder; real implementation uses miniredis.
}
```

Use `github.com/alicebob/miniredis/v2` if not already a project dep — check `go.mod` first.

- [ ] **Step 2: Add `*redis.Client` field to Strategy + read from ctx.Extra in setup**

In `recovery.go` setupFromContext:
```go
if v, ok := ctx.Extra["redis_client"].(*redis.Client); ok {
	s.rdb = v
}
```

In strategy.go Strategy struct:
```go
rdb *redis.Client
```
Imports: `"github.com/redis/go-redis/v9"`.

- [ ] **Step 3: persistState helper**

In `recovery.go`:
```go
type compositeState struct {
	PosQty    float64   `json:"pos_qty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Strategy) stateKey() string {
	return fmt.Sprintf("quantix:composite:%s:state", s.engineID)
}

func (s *Strategy) persistState(ctx context.Context) {
	if s.rdb == nil || s.engineID == "" {
		return
	}
	st := compositeState{PosQty: s.posQty, UpdatedAt: time.Now()}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	if err := s.rdb.Set(ctx, s.stateKey(), b, 0).Err(); err != nil {
		// Non-fatal: logging only. Backtest has no rdb so this is normal.
	}
}
```

- [ ] **Step 4: Hook into OnFill**

```go
func (s *Strategy) OnFill(_ *strategy.Context, fill strategy.Fill) {
	if fill.Symbol != s.cfg.Symbol {
		return
	}
	if fill.Side == strategy.SideBuy {
		s.posQty += fill.Qty
	} else {
		s.posQty -= fill.Qty
	}
	s.persistState(context.Background())
}
```

- [ ] **Step 5: Real test using miniredis**

```go
func TestOnFillPersistsToRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	s := New([]Alpha{&fakeAlpha{}}, Config{Symbol: "ETHUSDT"})
	s.engineID = "test-engine"
	s.rdb = rdb

	s.OnFill(nil, strategy.Fill{Symbol: "ETHUSDT", Side: strategy.SideBuy, Qty: 0.5})

	got, err := rdb.Get(context.Background(), "quantix:composite:test-engine:state").Result()
	if err != nil {
		t.Fatalf("redis get: %v", err)
	}
	var st compositeState
	if err := json.Unmarshal([]byte(got), &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if st.PosQty != 0.5 {
		t.Fatalf("PosQty=%f want 0.5", st.PosQty)
	}
}
```

- [ ] **Step 6: Commit:** `feat(composite): persist posQty to Redis on fill`

---

## Task 2.3: Recover posQty from Redis on Warmup

**Files:**
- Modify: `internal/strategy/composite/recovery.go` — add `recoverState`
- Modify: `internal/strategy/composite/strategy.go` — call `recoverState` from setupFromContext

- [ ] **Step 1: Failing test (miniredis)**

```go
func TestRecoverStateFromRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	// Pre-populate Redis with prior state
	st := compositeState{PosQty: -0.7, UpdatedAt: time.Now()}
	b, _ := json.Marshal(st)
	rdb.Set(context.Background(), "quantix:composite:test-engine:state", b, 0)

	s := New([]Alpha{&fakeAlpha{out: alpha.Signal{Direction: alpha.DirShort, Strength: 0.9}}}, Config{Symbol: "ETHUSDT"})

	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, &fakeBroker{}, zap.NewNop())
	ctx.Extra["redis_client"] = rdb
	ctx.Extra["engine_id"] = "test-engine"

	// One bar triggers setup
	s.OnBar(ctx, makeBars(1, 2300)[0])

	if s.posQty != -0.7 {
		t.Fatalf("posQty=%f want -0.7 (recovery failed)", s.posQty)
	}
}
```

- [ ] **Step 2: Run, verify FAIL**

- [ ] **Step 3: Implement recoverState**

In `recovery.go`:
```go
func (s *Strategy) recoverState(ctx context.Context) {
	if s.rdb == nil || s.engineID == "" {
		return
	}
	got, err := s.rdb.Get(ctx, s.stateKey()).Result()
	if err != nil {
		return // no prior state — fresh start
	}
	var st compositeState
	if err := json.Unmarshal([]byte(got), &st); err != nil {
		return
	}
	s.posQty = st.PosQty
}
```

In setupFromContext, after reading rdb:
```go
s.recoverState(context.Background())
```

- [ ] **Step 4: Verify test passes**

- [ ] **Step 5: Commit:** `feat(composite): recover posQty from Redis on warmup`

---

## Task 2.4: SimBroker StopLoss Trigger (CRITICAL — unblocks Phase 1 trade count)

**Files:**
- Read first: `internal/backtest/broker.go` (find PlaceOrder + bar processing logic)
- Modify: same file — track per-position SL; check on each bar; emit close fill

**Goal:** When a bar's High crosses a SHORT's StopLoss (or Low crosses a LONG's StopLoss), simulate a market close at the SL price and call `strategy.OnFill` to inform the strategy.

Reference: aistrat handles SL via real Binance protective orders (live) and via tickManage (5m bar close). For backtest, SimBroker is the only path — currently it ignores StopLoss.

- [ ] **Step 1: Read existing SimBroker structure**

Run:
```bash
grep -n "StopLoss\|PlaceOrder\|OnBar\|Fill" internal/backtest/broker.go | head -30
wc -l internal/backtest/broker.go
```

Get a clear picture before changing anything. Report back what's there if the change is non-trivial; the controller will provide more context.

- [ ] **Step 2: Add per-symbol SL tracking**

Add a `stops map[string]stopRecord` field where:
```go
type stopRecord struct {
	side   strategy.Side  // SideBuy means closing a SHORT (BUY to cover)
	price  float64
	qty    float64
}
```

When `PlaceOrder` is called with `OrderRequest{StopLoss: x, Qty: q}` and is an opening order, register `stops[symbol] = ...` with the OPPOSITE side and qty (so triggering means closing).

When `OnBar` is called (or wherever bars flow through), check each tracked stop:
- LONG (opened with BUY): SL trigger if `bar.Low <= stops.price`
- SHORT (opened with SELL): SL trigger if `bar.High >= stops.price`

On trigger:
- Construct a Fill at `stops.price` (worst-case execution)
- Adjust portfolio position
- Call strategy's OnFill(...)
- Delete the stop record

- [ ] **Step 3: Tests**

A standalone unit test for SimBroker:
- Set up SimBroker
- Place a long order with SL=2280 (qty 0.5, entry ~2300)
- Feed a bar with Low=2275 (crosses SL)
- Assert: a close fill was emitted to the strategy at price 2280

- [ ] **Step 4: Re-run Task 9 sanity backtest (after changes)**

Run `cmd/backtest -strategy composite` over 24 days. Trade count should jump from 1 to ≥10. Document new numbers in plan file as a Phase 2 sanity update.

- [ ] **Step 5: Commit:** `fix(backtest): SimBroker triggers OnFill on StopLoss cross`

If implementation is more involved than expected (>200 lines), STOP and escalate before merging — the broker is a foundational component used by all strategies' backtests.

---

## Task 2.5: Demo Deploy + Restart Test

**Out of scope for this plan iteration** — Tasks 2.1-2.4 must complete first. Once they do, controller will:
1. Cross-compile binary
2. Insert new `engine_sessions` row with `engine_id=ETHUSDT-5m-composite` and `params` containing composite Config
3. Restart `quantix-api` on server
4. Verify composite engine started + position state persisted in Redis
5. Force `sudo systemctl restart quantix-api`
6. Verify state restored

This is operational, not test-driven. Detailed run-book written when 2.1-2.4 are merged.

---

## Risks & Rollback

| Risk | Mitigation |
|---|---|
| Task 2.4 changes affect aistrat's backtest (uses same SimBroker) | Verify aistrat backtest produces same result pre/post change. If different, isolate the path. |
| Composite + aistrat both running on demo causes collisions | Different `engine_id` → different Redis keys + different strategy_id in fills table. No interaction. |
| Recovery test passes but state still gets lost | Monitor Redis TTL (we set 0 = no expiry). Verify key persists after restart. |
| miniredis dep adds noise | Already common in Go projects; if not in go.mod, add via `go get`. |

**Rollback per task:** Each task is independent commit. Revert composite/recovery.go file or specific commits. SimBroker change in Task 2.4 has highest blast radius — keep that commit independent.

---

## Notes for Implementing Engineer

- aistrat's setup pattern is at `internal/strategy/aistrat/signal.go:50-66`. Mirror but don't import aistrat code.
- `strategy.Context.Extra` is a `map[string]any`. Type assertions in setup must be defensive (`v, ok := x.(T)`).
- Redis client is `*redis.Client` from `github.com/redis/go-redis/v9` — same package aistrat uses.
- For SimBroker change, the file is `internal/backtest/broker.go`. Read it FIRST before designing the change.
