// Package data handles persistence of market data to TimescaleDB.
package data

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
)

// Store persists market data to TimescaleDB.
type Store struct {
	pool *pgxpool.Pool
	log  *zap.Logger
}

// New creates a Store and verifies the database connection.
// Configures connection pool lifecycle to prevent stale connections in long-running processes.
func New(ctx context.Context, dsn string, log *zap.Logger) (*Store, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}
	// Prevent holding dead connections indefinitely in production.
	poolCfg.MaxConnLifetime = 30 * time.Minute
	poolCfg.MaxConnIdleTime = 5 * time.Minute
	poolCfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connect to db: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	log.Info("database connected",
		zap.Int32("max_conns", poolCfg.MaxConns),
		zap.Duration("max_conn_lifetime", poolCfg.MaxConnLifetime),
	)
	return &Store{pool: pool, log: log}, nil
}

// Close releases pool connections.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping verifies the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// UpsertKline inserts or updates a single kline.
// Uses (time, symbol, interval) as the conflict key.
func (s *Store) UpsertKline(ctx context.Context, k exchange.Kline) error {
	const q = `
		INSERT INTO klines
			(time, symbol, interval, open, high, low, close, volume, quote_volume, num_trades, close_time)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (time, symbol, interval) DO UPDATE SET
			open         = EXCLUDED.open,
			high         = EXCLUDED.high,
			low          = EXCLUDED.low,
			close        = EXCLUDED.close,
			volume       = EXCLUDED.volume,
			quote_volume = EXCLUDED.quote_volume,
			num_trades   = EXCLUDED.num_trades,
			close_time   = EXCLUDED.close_time`

	_, err := s.pool.Exec(ctx, q,
		k.OpenTime,
		k.Symbol,
		k.Interval,
		k.Open,
		k.High,
		k.Low,
		k.Close,
		k.Volume,
		k.QuoteVolume,
		k.NumTrades,
		k.CloseTime,
	)
	return err
}

// BulkUpsertKlines inserts multiple klines efficiently in a single transaction.
func (s *Store) BulkUpsertKlines(ctx context.Context, klines []exchange.Kline) error {
	if len(klines) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	const q = `
		INSERT INTO klines
			(time, symbol, interval, open, high, low, close, volume, quote_volume, num_trades, close_time)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (time, symbol, interval) DO UPDATE SET
			open         = EXCLUDED.open,
			high         = EXCLUDED.high,
			low          = EXCLUDED.low,
			close        = EXCLUDED.close,
			volume       = EXCLUDED.volume,
			quote_volume = EXCLUDED.quote_volume,
			num_trades   = EXCLUDED.num_trades,
			close_time   = EXCLUDED.close_time`

	for _, k := range klines {
		if _, err := tx.Exec(ctx, q,
			k.OpenTime, k.Symbol, k.Interval,
			k.Open, k.High, k.Low, k.Close,
			k.Volume, k.QuoteVolume, k.NumTrades, k.CloseTime,
		); err != nil {
			return fmt.Errorf("insert kline: %w", err)
		}
	}

	return tx.Commit(ctx)
}

// InsertTicker persists a ticker snapshot.
func (s *Store) InsertTicker(ctx context.Context, t exchange.Ticker) error {
	const q = `
		INSERT INTO tickers (time, symbol, bid_price, ask_price, last_price, volume)
		VALUES ($1, $2, $3, $4, $5, $6)`

	_, err := s.pool.Exec(ctx, q,
		t.Timestamp,
		t.Symbol,
		t.BidPrice,
		t.AskPrice,
		t.LastPrice,
		t.Volume,
	)
	return err
}

// GetKlinesBetween retrieves klines for a symbol/interval within [start, end).
func (s *Store) GetKlinesBetween(
	ctx context.Context,
	symbol, interval string,
	start, end time.Time,
) ([]exchange.Kline, error) {
	const q = `
		SELECT time, symbol, interval, open, high, low, close,
		       volume, quote_volume, num_trades, close_time
		FROM klines
		WHERE symbol = $1 AND interval = $2
		  AND time >= $3 AND time < $4
		ORDER BY time ASC`

	rows, err := s.pool.Query(ctx, q, symbol, interval, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var klines []exchange.Kline
	for rows.Next() {
		var k exchange.Kline
		if err := rows.Scan(
			&k.OpenTime, &k.Symbol, &k.Interval,
			&k.Open, &k.High, &k.Low, &k.Close,
			&k.Volume, &k.QuoteVolume, &k.NumTrades, &k.CloseTime,
		); err != nil {
			return nil, err
		}
		k.IsClosed = true
		klines = append(klines, k)
	}
	return klines, rows.Err()
}

// GetLatestKlines retrieves the most recent `limit` closed klines for a symbol/interval.
func (s *Store) GetLatestKlines(ctx context.Context, symbol, interval string, limit int) ([]exchange.Kline, error) {
	const q = `
		SELECT time, symbol, interval, open, high, low, close, volume, quote_volume, num_trades, close_time
		FROM klines
		WHERE symbol = $1 AND interval = $2
		ORDER BY time DESC
		LIMIT $3`

	rows, err := s.pool.Query(ctx, q, symbol, interval, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var klines []exchange.Kline
	for rows.Next() {
		var k exchange.Kline
		if err := rows.Scan(
			&k.OpenTime, &k.Symbol, &k.Interval,
			&k.Open, &k.High, &k.Low, &k.Close,
			&k.Volume, &k.QuoteVolume, &k.NumTrades, &k.CloseTime,
		); err != nil {
			return nil, err
		}
		k.IsClosed = true
		klines = append(klines, k)
	}
	return klines, rows.Err()
}
