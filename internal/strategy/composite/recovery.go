// Package composite — recovery.go contains lifecycle hooks for live engine
// integration (read dependencies from ctx.Extra, persist/restore state).
// Backtest contexts have empty Extra so all hooks degrade to no-ops.
package composite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Quantix/quantix/internal/strategy"
	"github.com/redis/go-redis/v9"
)

// setupFromContext reads live-engine dependencies from ctx.Extra. Called
// once on the first bar. Backtest contexts have empty Extra — fields stay
// at zero values and nothing breaks.
func (s *Strategy) setupFromContext(ctx *strategy.Context) {
	if v, ok := ctx.Extra["user_id"].(int); ok {
		s.userID = v
	}
	if v, ok := ctx.Extra["engine_id"].(string); ok {
		s.engineID = v
	}
	if v, ok := ctx.Extra["redis_client"].(*redis.Client); ok {
		s.rdb = v
	}
	// Recovery: read any prior state. Order matters — must come after
	// rdb and engineID are set.
	s.recoverState(context.Background())
}

// compositeState is the JSON shape persisted to Redis.
type compositeState struct {
	PosQty    float64   `json:"pos_qty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// stateKey returns the Redis key used to persist this strategy instance's
// state. Empty engineID returns "" — caller must skip if so. Includes
// userID: engineID ("SYMBOL-INTERVAL-composite") is only unique WITHIN one
// user's engines, so two users running composite on the same symbol+interval
// would otherwise share one key and corrupt each other's posQty on restart
// (2026-08-06 finding, same root cause as the guardian_state incident).
func (s *Strategy) stateKey() string {
	if s.engineID == "" {
		return ""
	}
	return fmt.Sprintf("quantix:composite:%d:%s:state", s.userID, s.engineID)
}

// persistState writes posQty to Redis. Silently no-op when rdb or engineID
// missing (backtest, or pre-setup). Errors are non-fatal — Redis hiccups
// must not stop trading.
func (s *Strategy) persistState(ctx context.Context) {
	key := s.stateKey()
	if s.rdb == nil || key == "" {
		return
	}
	st := compositeState{PosQty: s.posQty, UpdatedAt: time.Now()}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = s.rdb.Set(ctx, key, b, 0).Err()
}

// recoverState reads any persisted state from Redis. Silently no-ops when
// rdb or key is missing, or when no prior state exists. Errors are logged
// nowhere yet (Phase 5 polish) — fresh start on any failure is the safe
// default.
func (s *Strategy) recoverState(ctx context.Context) {
	key := s.stateKey()
	if s.rdb == nil || key == "" {
		return
	}
	got, err := s.rdb.Get(ctx, key).Result()
	if err != nil {
		return // missing key OR Redis error — start fresh
	}
	var st compositeState
	if err := json.Unmarshal([]byte(got), &st); err != nil {
		return // malformed payload — start fresh, don't crash
	}
	s.posQty = st.PosQty
}
