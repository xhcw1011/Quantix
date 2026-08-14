// Package enginestate provides a generic, type-safe persistence helper for
// per-engine runtime state that must survive a process restart.
//
// engine_id ("SYMBOL-INTERVAL-STRATEGY") is only unique WITHIN one user's
// engines, not globally -- two different users can run the identical
// symbol+interval+strategy combo. On 2026-08-05/06 this exact bug (a state
// key built from engine_id alone) was independently found and fixed three
// times in three unrelated places: guardian_state, the experiments table,
// and composite's Redis key. Store[T] makes userID a required, explicit
// parameter of every Save/Load call so a future subsystem needing this
// pattern gets user-scoping by construction instead of by remembering to
// include it in a hand-rolled key.
package enginestate

import (
	"context"
	"encoding/json"
	"fmt"
)

// DB is the minimal persistence dependency, satisfied by *data.Store
// (internal/data/engine_state.go).
type DB interface {
	SaveEngineState(ctx context.Context, userID int, engineID, namespace string, state []byte) error
	LoadEngineState(ctx context.Context, userID int, engineID, namespace string) ([]byte, bool, error)
}

// Store persists values of type T, JSON-encoded, scoped to
// (userID, engineID, namespace). namespace separates unrelated subsystems
// sharing the same underlying table so they can never collide with each
// other even if they reuse the same engineID string.
type Store[T any] struct {
	db        DB
	namespace string
}

// New creates a Store for the given namespace (e.g. "risk").
func New[T any](db DB, namespace string) *Store[T] {
	return &Store[T]{db: db, namespace: namespace}
}

// Save JSON-encodes and persists v for (userID, engineID).
func (s *Store[T]) Save(ctx context.Context, userID int, engineID string, v T) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("enginestate: marshal: %w", err)
	}
	return s.db.SaveEngineState(ctx, userID, engineID, s.namespace, b)
}

// Load fetches and decodes the persisted value for (userID, engineID).
// ok=false when none exists yet (first-ever start for this engine) or the
// stored payload is malformed -- callers should treat both as "start
// fresh," matching the established convention elsewhere in this codebase
// (see guardian.recoverState). A genuine DB error still propagates.
func (s *Store[T]) Load(ctx context.Context, userID int, engineID string) (v T, ok bool, err error) {
	b, ok, err := s.db.LoadEngineState(ctx, userID, engineID, s.namespace)
	if err != nil || !ok {
		return v, false, err
	}
	if jerr := json.Unmarshal(b, &v); jerr != nil {
		var zero T
		return zero, false, nil
	}
	return v, true, nil
}
