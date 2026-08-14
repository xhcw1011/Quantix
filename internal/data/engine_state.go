package data

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SaveEngineState persists a namespaced blob of engine-scoped state, keyed
// by (user_id, engine_id, namespace). engine_id ("SYMBOL-INTERVAL-STRATEGY")
// is only unique WITHIN one user's engines, not globally — userID must be
// included or a restart can load one user's state into another user's
// engine (see migration 015 for the original incident this pattern guards
// against). namespace separates unrelated subsystems sharing this table.
func (s *Store) SaveEngineState(ctx context.Context, userID int, engineID, namespace string, state []byte) error {
	const q = `
		INSERT INTO engine_state (user_id, engine_id, namespace, state, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (user_id, engine_id, namespace) DO UPDATE SET
			state      = EXCLUDED.state,
			updated_at = NOW()`
	if _, err := s.pool.Exec(ctx, q, userID, engineID, namespace, state); err != nil {
		return fmt.Errorf("save engine state: %w", err)
	}
	return nil
}

// LoadEngineState loads the persisted state blob for (userID, engineID,
// namespace); ok=false when none exists.
func (s *Store) LoadEngineState(ctx context.Context, userID int, engineID, namespace string) ([]byte, bool, error) {
	var state []byte
	err := s.pool.QueryRow(ctx,
		`SELECT state FROM engine_state WHERE user_id = $1 AND engine_id = $2 AND namespace = $3`,
		userID, engineID, namespace,
	).Scan(&state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("load engine state: %w", err)
	}
	return state, true, nil
}
