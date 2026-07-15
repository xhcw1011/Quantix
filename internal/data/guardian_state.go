package data

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// UpsertGuardianState persists a Position Guardian's trail-state JSON, keyed by
// engine id, so a restart can restore the advanced stop.
func (s *Store) UpsertGuardianState(ctx context.Context, engineID string, state []byte) error {
	const q = `
		INSERT INTO guardian_state (engine_id, state, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (engine_id) DO UPDATE SET
			state      = EXCLUDED.state,
			updated_at = NOW()`
	if _, err := s.pool.Exec(ctx, q, engineID, state); err != nil {
		return fmt.Errorf("upsert guardian state: %w", err)
	}
	return nil
}

// GetGuardianState loads a Guardian's persisted state JSON; ok=false when none exists.
func (s *Store) GetGuardianState(ctx context.Context, engineID string) ([]byte, bool, error) {
	var state []byte
	err := s.pool.QueryRow(ctx, `SELECT state FROM guardian_state WHERE engine_id = $1`, engineID).Scan(&state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get guardian state: %w", err)
	}
	return state, true, nil
}
