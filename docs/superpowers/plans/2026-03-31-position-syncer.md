# Position Syncer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a PositionSyncer that uses Redis as real-time cache + DB as backup, ensuring strategy always knows the true exchange positions — even after restarts or manual operations.

**Architecture:** Exchange WS pushes position changes → Redis updated in real-time → Strategy reads from Redis → DB persisted async. On startup: Redis → DB → Exchange API (fallback chain). Strategy never maintains its own position state — it reads from PositionSyncer.

**Tech Stack:** Go, Redis (github.com/redis/go-redis/v9), PostgreSQL, Binance Futures WS (User Data Stream)

---

## File Map

| Action | File | Responsibility |
|--------|------|---------------|
| Create | `internal/position/syncer.go` | PositionSyncer: Redis read/write, exchange query, reconciliation |
| Create | `internal/position/syncer_test.go` | Unit tests with mock Redis + mock exchange |
| Create | `internal/position/types.go` | Shared types: ExchangePosition, PositionChangeEvent |
| Create | `internal/position/redis.go` | Redis client wrapper for position operations |
| Create | `migrations/009_strategy_positions.sql` | DB table for position state backup |
| Modify | `internal/data/users.go` | Add UpsertStrategyPosition, GetStrategyPositions, DeleteStrategyPosition |
| Modify | `internal/live/engine.go` | Inject PositionSyncer, use it in onBar and processFills |
| Modify | `internal/strategy/aistrat/aistrat.go` | Remove longPos/shortPos self-management, read from PositionSyncer via new PortfolioView |
| Modify | `internal/strategy/strategy.go` | Extend PortfolioView with LongPosition/ShortPosition methods |
| Modify | `internal/exchange/binance_futures/orderbroker.go` | Add GetPositions() method, extend SubscribeUserData for position events |
| Modify | `internal/config/config.go` | Ensure RedisConfig is usable |
| Modify | `internal/api/manager.go` | Create PositionSyncer, pass to engine |
| Modify | `cmd/api/main.go` | Initialize Redis client on startup |

---

## Task 1: Position types and Redis client

**Files:**
- Create: `internal/position/types.go`
- Create: `internal/position/redis.go`

- [ ] **Step 1: Create types.go**

```go
// Package position provides a PositionSyncer that tracks exchange positions
// via Redis (real-time) + DB (backup), ensuring strategy state survives restarts.
package position

import "time"

// ExchangePosition represents a position as reported by the exchange.
type ExchangePosition struct {
	Symbol       string  `json:"symbol" redis:"symbol"`
	Side         string  `json:"side" redis:"side"` // "LONG" or "SHORT"
	Qty          float64 `json:"qty" redis:"qty"`
	EntryPrice   float64 `json:"entry_price" redis:"entry_price"`
	MarkPrice    float64 `json:"mark_price" redis:"mark_price"`
	UnrealizedPnL float64 `json:"unrealized_pnl" redis:"unrealized_pnl"`
	Leverage     int     `json:"leverage" redis:"leverage"`
	UpdatedAt    time.Time `json:"updated_at" redis:"updated_at"`
}

// StrategyPosition extends ExchangePosition with strategy-specific state.
type StrategyPosition struct {
	ExchangePosition
	Mode       string  `json:"mode" redis:"mode"` // "trend" or "range"
	StopLoss   float64 `json:"stop_loss" redis:"stop_loss"`
	TakeProfit float64 `json:"take_profit" redis:"take_profit"`
	Trailing   float64 `json:"trailing" redis:"trailing"`
	PeakPrice  float64 `json:"peak_price" redis:"peak_price"`
	R          float64 `json:"r" redis:"r"`
	InitQty    float64 `json:"init_qty" redis:"init_qty"`
	TP1Hit     bool    `json:"tp1_hit" redis:"tp1_hit"`
	BarsHeld   int     `json:"bars_held" redis:"bars_held"`
	OrderID    string  `json:"order_id" redis:"order_id"`
	Filled     bool    `json:"filled" redis:"filled"`
}

// PositionEvent describes a change detected by the syncer.
type PositionEvent struct {
	Type     string           // "opened", "closed", "modified", "external_close", "external_open"
	Position ExchangePosition
}
```

- [ ] **Step 2: Create redis.go**

```go
package position

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// RedisStore handles position read/write in Redis.
type RedisStore struct {
	client *redis.Client
	prefix string // key prefix, e.g. "quantix:pos:4:" (user_id)
	log    *zap.Logger
}

func NewRedisStore(client *redis.Client, userID int, log *zap.Logger) *RedisStore {
	return &RedisStore{
		client: client,
		prefix: fmt.Sprintf("quantix:pos:%d:", userID),
		log:    log,
	}
}

func (r *RedisStore) key(symbol, side string) string {
	return r.prefix + symbol + ":" + side
}

func (r *RedisStore) SetPosition(ctx context.Context, pos StrategyPosition) error {
	data, err := json.Marshal(pos)
	if err != nil {
		return err
	}
	return r.client.Set(ctx, r.key(pos.Symbol, pos.Side), data, 24*time.Hour).Err()
}

func (r *RedisStore) GetPosition(ctx context.Context, symbol, side string) (*StrategyPosition, error) {
	data, err := r.client.Get(ctx, r.key(symbol, side)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pos StrategyPosition
	if err := json.Unmarshal(data, &pos); err != nil {
		return nil, err
	}
	return &pos, nil
}

func (r *RedisStore) DeletePosition(ctx context.Context, symbol, side string) error {
	return r.client.Del(ctx, r.key(symbol, side)).Err()
}

func (r *RedisStore) GetAllPositions(ctx context.Context) ([]StrategyPosition, error) {
	keys, err := r.client.Keys(ctx, r.prefix+"*").Result()
	if err != nil {
		return nil, err
	}
	var positions []StrategyPosition
	for _, k := range keys {
		data, err := r.client.Get(ctx, k).Bytes()
		if err != nil {
			continue
		}
		var pos StrategyPosition
		if err := json.Unmarshal(data, &pos); err != nil {
			continue
		}
		positions = append(positions, pos)
	}
	return positions, nil
}

func (r *RedisStore) SetEquity(ctx context.Context, equity float64) error {
	return r.client.Set(ctx, r.prefix+"equity", equity, 24*time.Hour).Err()
}

func (r *RedisStore) GetEquity(ctx context.Context) (float64, error) {
	return r.client.Get(ctx, r.prefix+"equity").Float64()
}
```

- [ ] **Step 3: Build and verify**

Run: `go build ./internal/position/...`
Expected: no errors (may need `go get github.com/redis/go-redis/v9`)

- [ ] **Step 4: Commit**

---

## Task 2: PositionSyncer core

**Files:**
- Create: `internal/position/syncer.go`

- [ ] **Step 1: Create syncer.go**

```go
package position

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
)

// PositionQuerier queries exchange for current positions.
type PositionQuerier interface {
	GetMarginRatios(ctx context.Context) ([]exchange.PositionMarginInfo, error)
}

// Syncer keeps Redis + strategy in sync with exchange positions.
type Syncer struct {
	redis    *RedisStore
	store    *data.Store // DB backup (may be nil)
	log      *zap.Logger

	mu       sync.RWMutex
	long     *StrategyPosition // cached in-memory
	short    *StrategyPosition

	onChange func(PositionEvent) // callback to notify strategy
	userID   int
	engineID string
	symbol   string
}

type SyncerConfig struct {
	Redis    *RedisStore
	Store    *data.Store
	UserID   int
	EngineID string
	Symbol   string
	OnChange func(PositionEvent)
	Log      *zap.Logger
}

func NewSyncer(cfg SyncerConfig) *Syncer {
	return &Syncer{
		redis:    cfg.Redis,
		store:    cfg.Store,
		log:      cfg.Log,
		onChange: cfg.OnChange,
		userID:   cfg.UserID,
		engineID: cfg.EngineID,
		symbol:   cfg.Symbol,
	}
}

// LoadFromExchange queries exchange for current positions and seeds Redis + memory.
// Called once at engine startup.
func (s *Syncer) LoadFromExchange(ctx context.Context, querier PositionQuerier) error {
	ratios, err := querier.GetMarginRatios(ctx)
	if err != nil {
		return err
	}
	for _, r := range ratios {
		if r.Symbol != s.symbol || r.Size == 0 {
			continue
		}
		side := r.PositionSide
		if side == "" || side == "BOTH" {
			if r.Size > 0 { side = "LONG" } else { side = "SHORT" }
		}
		pos := StrategyPosition{
			ExchangePosition: ExchangePosition{
				Symbol: r.Symbol, Side: side, Qty: r.Size,
				UpdatedAt: time.Now(),
			},
			Filled: true,
		}
		s.updatePosition(ctx, &pos)
		s.log.Info("syncer: loaded position from exchange",
			zap.String("side", side), zap.Float64("qty", r.Size))
	}
	return nil
}

// LoadFromRedis loads cached positions from Redis on startup.
// Called before LoadFromExchange as fast path.
func (s *Syncer) LoadFromRedis(ctx context.Context) {
	if s.redis == nil { return }
	positions, err := s.redis.GetAllPositions(ctx)
	if err != nil {
		s.log.Warn("syncer: redis load failed", zap.Error(err))
		return
	}
	for _, p := range positions {
		p := p
		if p.Side == "LONG" { s.long = &p } else { s.short = &p }
	}
	if s.long != nil || s.short != nil {
		s.log.Info("syncer: loaded from Redis",
			zap.Bool("has_long", s.long != nil),
			zap.Bool("has_short", s.short != nil))
	}
}

// GetLong returns the current LONG position (nil if none).
func (s *Syncer) GetLong() *StrategyPosition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.long
}

// GetShort returns the current SHORT position (nil if none).
func (s *Syncer) GetShort() *StrategyPosition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.short
}

// UpdatePosition sets/updates a position in memory + Redis + DB.
func (s *Syncer) UpdatePosition(ctx context.Context, pos *StrategyPosition) {
	s.updatePosition(ctx, pos)
}

func (s *Syncer) updatePosition(ctx context.Context, pos *StrategyPosition) {
	s.mu.Lock()
	if pos.Side == "LONG" { s.long = pos } else { s.short = pos }
	s.mu.Unlock()

	pos.UpdatedAt = time.Now()

	// Write to Redis (fast)
	if s.redis != nil {
		if err := s.redis.SetPosition(ctx, *pos); err != nil {
			s.log.Warn("syncer: redis write failed", zap.Error(err))
		}
	}

	// Write to DB (async)
	if s.store != nil {
		go func() {
			dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.store.UpsertStrategyPosition(dbCtx, s.userID, s.engineID, pos); err != nil {
				s.log.Warn("syncer: db write failed", zap.Error(err))
			}
		}()
	}
}

// RemovePosition clears a position from memory + Redis + DB.
func (s *Syncer) RemovePosition(ctx context.Context, side string) {
	s.mu.Lock()
	if side == "LONG" { s.long = nil } else { s.short = nil }
	s.mu.Unlock()

	if s.redis != nil {
		s.redis.DeletePosition(ctx, s.symbol, side)
	}
	if s.store != nil {
		go func() {
			dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s.store.DeleteStrategyPosition(dbCtx, s.userID, s.engineID, side)
		}()
	}
}

// OnExchangePositionUpdate is called by User Data Stream when positions change.
// Detects external operations (manual close/open) and notifies strategy.
func (s *Syncer) OnExchangePositionUpdate(ctx context.Context, symbol, side string, qty, entryPrice float64) {
	if symbol != s.symbol { return }

	s.mu.RLock()
	var current *StrategyPosition
	if side == "LONG" { current = s.long } else { current = s.short }
	s.mu.RUnlock()

	if qty == 0 && current != nil {
		// Position closed (possibly externally)
		s.log.Info("syncer: position closed",
			zap.String("side", side), zap.String("source", "exchange"))
		s.RemovePosition(ctx, side)
		if s.onChange != nil {
			s.onChange(PositionEvent{
				Type: "external_close",
				Position: ExchangePosition{Symbol: symbol, Side: side},
			})
		}
		return
	}

	if qty != 0 && current == nil {
		// New position (possibly opened externally)
		s.log.Info("syncer: new position detected",
			zap.String("side", side), zap.Float64("qty", qty))
		pos := &StrategyPosition{
			ExchangePosition: ExchangePosition{
				Symbol: symbol, Side: side, Qty: qty,
				EntryPrice: entryPrice, UpdatedAt: time.Now(),
			},
			Filled: true,
		}
		s.updatePosition(ctx, pos)
		if s.onChange != nil {
			s.onChange(PositionEvent{
				Type: "external_open",
				Position: pos.ExchangePosition,
			})
		}
	}
}
```

- [ ] **Step 2: Build**

Run: `go build ./internal/position/...`

- [ ] **Step 3: Commit**

---

## Task 3: DB migration + data layer

**Files:**
- Create: `migrations/009_strategy_positions.sql`
- Modify: `internal/data/users.go`

- [ ] **Step 1: Create migration**

```sql
-- Strategy position state backup (Redis is primary, this is recovery fallback)
CREATE TABLE IF NOT EXISTS strategy_positions (
    user_id     INT NOT NULL,
    engine_id   TEXT NOT NULL,
    side        TEXT NOT NULL,  -- 'LONG' or 'SHORT'
    symbol      TEXT NOT NULL,
    mode        TEXT NOT NULL DEFAULT 'range',
    qty         DOUBLE PRECISION NOT NULL DEFAULT 0,
    entry_price DOUBLE PRECISION NOT NULL DEFAULT 0,
    stop_loss   DOUBLE PRECISION,
    take_profit DOUBLE PRECISION,
    trailing    DOUBLE PRECISION,
    peak_price  DOUBLE PRECISION,
    r_value     DOUBLE PRECISION,
    init_qty    DOUBLE PRECISION,
    tp1_hit     BOOLEAN DEFAULT FALSE,
    bars_held   INT DEFAULT 0,
    order_id    TEXT,
    filled      BOOLEAN DEFAULT TRUE,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (user_id, engine_id, side)
);
```

- [ ] **Step 2: Add data layer methods to users.go**

Add `UpsertStrategyPosition`, `GetStrategyPositions`, `DeleteStrategyPosition` to `internal/data/users.go` using the `position.StrategyPosition` type (or a local struct to avoid import cycle).

- [ ] **Step 3: Apply migration**

Run: `psql -U quantix -d quantix -f migrations/009_strategy_positions.sql`

- [ ] **Step 4: Build and commit**

---

## Task 4: Extend Binance Futures for position queries

**Files:**
- Modify: `internal/exchange/binance_futures/orderbroker.go`
- Modify: `internal/exchange/orderbroker.go`

- [ ] **Step 1: Add GetPositions interface to exchange package**

In `internal/exchange/orderbroker.go`, add:
```go
type PositionInfo struct {
    Symbol       string
    Side         string  // "LONG", "SHORT"
    Qty          float64
    EntryPrice   float64
    UnrealizedPnL float64
    MarkPrice    float64
    Leverage     int
}

type PositionQuerier interface {
    GetPositions(ctx context.Context, symbol string) ([]PositionInfo, error)
}
```

- [ ] **Step 2: Implement in Binance Futures OrderBroker**

```go
func (b *OrderBroker) GetPositions(ctx context.Context, symbol string) ([]exchange.PositionInfo, error) {
    risks, err := b.client.NewGetPositionRiskService().Do(ctx)
    // parse and return PositionInfo for non-zero positions matching symbol
}
```

- [ ] **Step 3: Extend SubscribeUserData to push position changes**

In the ACCOUNT_UPDATE handler, also call a position update handler with per-position qty/entry from `event.AccountUpdate.Positions`.

- [ ] **Step 4: Build and commit**

---

## Task 5: Integrate PositionSyncer into live engine

**Files:**
- Modify: `internal/live/engine.go`
- Modify: `internal/api/manager.go`
- Modify: `cmd/api/main.go`

- [ ] **Step 1: Add Redis client initialization to cmd/api/main.go**

```go
import "github.com/redis/go-redis/v9"

rdb := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB})
defer rdb.Close()
// pass rdb to NewServer / NewEngineManager
```

- [ ] **Step 2: Create PositionSyncer in manager.go Start()**

In the live engine creation path:
```go
redisStore := position.NewRedisStore(m.redis, userID, m.log)
syncer := position.NewSyncer(position.SyncerConfig{
    Redis: redisStore, Store: m.store, UserID: userID,
    EngineID: engineID, Symbol: req.Symbol, Log: m.log,
})
// Load positions: Redis → Exchange
syncer.LoadFromRedis(ctx)
syncer.LoadFromExchange(ctx, orderClient.(position.PositionQuerier))
```

Pass syncer to engine and strategy.

- [ ] **Step 3: Wire PositionSyncer into User Data Stream**

In the UDS handler, call `syncer.OnExchangePositionUpdate()` for ACCOUNT_UPDATE position events.

- [ ] **Step 4: Update equity from Redis**

Replace `cachedEquity` in engine with `syncer.redis.GetEquity()`.

- [ ] **Step 5: Build and commit**

---

## Task 6: Refactor AI strategy to use PositionSyncer

**Files:**
- Modify: `internal/strategy/strategy.go`
- Modify: `internal/strategy/aistrat/aistrat.go`

- [ ] **Step 1: Extend PortfolioView interface**

```go
type PortfolioView interface {
    Cash() float64
    Position(symbol string) (qty float64, avgPrice float64, ok bool)
    Equity(prices map[string]float64) float64
    LongPosition(symbol string) (qty float64, entryPrice float64, ok bool)   // NEW
    ShortPosition(symbol string) (qty float64, entryPrice float64, ok bool)  // NEW
}
```

- [ ] **Step 2: Implement in livePortfolioView**

Wire through to PositionSyncer's GetLong()/GetShort().

- [ ] **Step 3: Refactor aistrat.go**

Remove `longPos`/`shortPos` from AIStrategy struct. Instead:
- On each OnBar, read positions from `ctx.Portfolio.LongPosition()` / `ShortPosition()`
- Strategy still tracks mode/stopLoss/trailing/tp in a lightweight struct
- But position existence and qty comes from PositionSyncer (via PortfolioView)
- When opening position: call syncer.UpdatePosition() (via a new method on PortfolioView or Context)
- When closing: syncer.RemovePosition()

- [ ] **Step 4: Build, test, commit**

---

## Task 7: End-to-end test

- [ ] **Step 1: Start Redis**

```bash
docker compose -f deploy/docker-compose.yml up -d redis
```

- [ ] **Step 2: Start engine in paper demo mode, verify positions persist in Redis**

- [ ] **Step 3: Restart engine, verify positions recovered from Redis**

- [ ] **Step 4: Manually close position on exchange, verify syncer detects it**

---

## Verification Gate

```bash
go build ./...
go test ./... -count=1 -timeout 120s
go vet ./...
# Redis connectivity test
redis-cli ping
```
