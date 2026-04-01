package position

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// RedisStore handles position read/write in Redis.
type RedisStore struct {
	client *redis.Client
	prefix string // "quantix:pos:{userID}:"
	log    *zap.Logger
}

// NewRedisStore creates a Redis-backed position store.
func NewRedisStore(client *redis.Client, userID int, engineID string, log *zap.Logger) *RedisStore {
	return &RedisStore{
		client: client,
		prefix: fmt.Sprintf("quantix:pos:%d:%s:", userID, engineID),
		log:    log,
	}
}

func (r *RedisStore) key(symbol, side string) string {
	return r.prefix + symbol + ":" + side
}

// SetPosition writes a position to Redis with 24h TTL.
func (r *RedisStore) SetPosition(ctx context.Context, pos StrategyPosition) error {
	data, err := json.Marshal(pos)
	if err != nil {
		return err
	}
	return r.client.Set(ctx, r.key(pos.Symbol, pos.Side), data, 0).Err()
}

// GetPosition reads a single position. Returns nil if not found.
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

// DeletePosition removes a position from Redis.
func (r *RedisStore) DeletePosition(ctx context.Context, symbol, side string) error {
	return r.client.Del(ctx, r.key(symbol, side)).Err()
}

// GetAllPositions returns all positions for this user.
func (r *RedisStore) GetAllPositions(ctx context.Context) ([]StrategyPosition, error) {
	var positions []StrategyPosition
	var cursor uint64
	pattern := r.prefix + "*:*"
	for {
		keys, next, err := r.client.Scan(ctx, cursor, pattern, 20).Result()
		if err != nil {
			return positions, err
		}
		for _, k := range keys {
			data, err := r.client.Get(ctx, k).Bytes()
			if err != nil { continue }
			var pos StrategyPosition
			if err := json.Unmarshal(data, &pos); err != nil { continue }
			positions = append(positions, pos)
		}
		cursor = next
		if cursor == 0 { break }
	}
	return positions, nil
}

// SetEquity caches the latest exchange equity.
func (r *RedisStore) SetEquity(ctx context.Context, equity float64) error {
	return r.client.Set(ctx, r.prefix+"equity", equity, 0).Err()
}

// GetEquity returns cached equity. Returns 0 if not found.
func (r *RedisStore) GetEquity(ctx context.Context) (float64, error) {
	v, err := r.client.Get(ctx, r.prefix+"equity").Float64()
	if err == redis.Nil {
		return 0, nil
	}
	return v, err
}
