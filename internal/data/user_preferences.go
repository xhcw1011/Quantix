package data

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// GetUserBoolPref reads a boolean flag from user_preferences.preferences JSONB.
// Missing user row or missing key both return false (fail-closed).
func (s *Store) GetUserBoolPref(ctx context.Context, userID int, key string) (bool, error) {
	var v *bool
	err := s.pool.QueryRow(ctx,
		`SELECT (preferences->>$2)::bool FROM user_preferences WHERE user_id = $1`,
		userID, key).Scan(&v)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("get user bool pref %q: %w", key, err)
	}
	if v == nil {
		return false, nil
	}
	return *v, nil
}

// SetUserBoolPref upserts a boolean flag into user_preferences.preferences JSONB,
// preserving any other keys already stored.
func (s *Store) SetUserBoolPref(ctx context.Context, userID int, key string, val bool) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_preferences (user_id, preferences)
		VALUES ($1, jsonb_build_object($2::text, $3::bool))
		ON CONFLICT (user_id) DO UPDATE SET
			preferences = user_preferences.preferences || jsonb_build_object($2::text, $3::bool),
			updated_at  = now()`,
		userID, key, val)
	if err != nil {
		return fmt.Errorf("set user bool pref %q: %w", key, err)
	}
	return nil
}
