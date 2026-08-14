package data

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// UpsertGuardianState persists a Position Guardian's trail-state JSON, keyed by
// (user_id, engine_id). engine_id alone ("SYMBOL-INTERVAL-STRATEGY") is only
// unique WITHIN one user's engines — two different users can run the identical
// symbol+interval+strategy combo — so userID must be included or a restart can
// load one user's trail state into another user's guardian (see migration 015).
func (s *Store) UpsertGuardianState(ctx context.Context, userID int, engineID string, state []byte) error {
	const q = `
		INSERT INTO guardian_state (user_id, engine_id, state, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (user_id, engine_id) DO UPDATE SET
			state      = EXCLUDED.state,
			updated_at = NOW()`
	if _, err := s.pool.Exec(ctx, q, userID, engineID, state); err != nil {
		return fmt.Errorf("upsert guardian state: %w", err)
	}
	return nil
}

// GetGuardianState loads a Guardian's persisted state JSON for (userID, engineID);
// ok=false when none exists.
func (s *Store) GetGuardianState(ctx context.Context, userID int, engineID string) ([]byte, bool, error) {
	var state []byte
	err := s.pool.QueryRow(ctx,
		`SELECT state FROM guardian_state WHERE user_id = $1 AND engine_id = $2`, userID, engineID,
	).Scan(&state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get guardian state: %w", err)
	}
	return state, true, nil
}

// DeleteGuardianState removes a Guardian's persisted state row for
// (userID, engineID). Called when a guardian reaches PhaseClosed so a future,
// unrelated guardian reusing the same engine_id never inherits stale state
// (see GuardianState.Closing doc comment for why this matters).
func (s *Store) DeleteGuardianState(ctx context.Context, userID int, engineID string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM guardian_state WHERE user_id = $1 AND engine_id = $2`, userID, engineID,
	); err != nil {
		return fmt.Errorf("delete guardian state: %w", err)
	}
	return nil
}
