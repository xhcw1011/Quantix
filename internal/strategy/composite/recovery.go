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
}

// compositeState is the JSON shape persisted to Redis.
type compositeState struct {
	PosQty    float64   `json:"pos_qty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// stateKey returns the Redis key used to persist this strategy instance's state.
// Empty engineID returns "" — caller must skip if so.
func (s *Strategy) stateKey() string {
	if s.engineID == "" {
		return ""
	}
	return fmt.Sprintf("quantix:composite:%s:state", s.engineID)
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
