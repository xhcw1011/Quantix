package data

import (
	"context"
	"fmt"
	"time"
)

// FundingRow is one perp funding payment.
type FundingRow struct {
	Time   time.Time
	Symbol string
	Rate   float64
}

// BulkUpsertFunding inserts/updates funding rows (idempotent on (time, symbol)).
func (s *Store) BulkUpsertFunding(ctx context.Context, rows []FundingRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	const q = `
		INSERT INTO funding_rates (time, symbol, funding_rate)
		VALUES ($1, $2, $3)
		ON CONFLICT (time, symbol) DO UPDATE SET funding_rate = EXCLUDED.funding_rate`
	for _, r := range rows {
		if _, err := tx.Exec(ctx, q, r.Time, r.Symbol, r.Rate); err != nil {
			return fmt.Errorf("insert funding %s@%s: %w", r.Symbol, r.Time, err)
		}
	}
	return tx.Commit(ctx)
}

// MaxFundingTime returns the latest stored funding time for a symbol (zero time if
// none), for resumable ingestion.
func (s *Store) MaxFundingTime(ctx context.Context, symbol string) (time.Time, error) {
	var t *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT max(time) FROM funding_rates WHERE symbol=$1`, symbol).Scan(&t); err != nil {
		return time.Time{}, err
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

// GetFunding returns all stored funding rows for a symbol, chronological.
func (s *Store) GetFunding(ctx context.Context, symbol string) ([]FundingRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT time, funding_rate FROM funding_rates WHERE symbol=$1 ORDER BY time`, symbol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FundingRow
	for rows.Next() {
		r := FundingRow{Symbol: symbol}
		if err := rows.Scan(&r.Time, &r.Rate); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
