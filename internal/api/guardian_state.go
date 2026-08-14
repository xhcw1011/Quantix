package api

import (
	"context"
	"encoding/json"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/guardian"
)

// guardianStateStore adapts the DB store to guardian.StateStore so a live Guardian
// persists and restores its trailed stop across engine/server restarts.
//
// userID is required: the guardian package's "key" (== engine_id, e.g.
// "BTCUSDT-5m-guardian") is only unique WITHIN one user's engines, not
// globally — two different users can run the identical symbol+interval+
// strategy combo. Without userID scoping, a restart can load one user's
// trail state into another user's guardian and trigger a spurious protective
// close on real money (incident: 2026-08-05). See migration 015.
type guardianStateStore struct {
	store  *data.Store
	userID int
}

// Save marshals and upserts the guardian's trail state.
func (g *guardianStateStore) Save(key string, s guardian.GuardianState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return g.store.UpsertGuardianState(context.Background(), g.userID, key, b)
}

// Load fetches and unmarshals the guardian's trail state (ok=false if absent/bad).
func (g *guardianStateStore) Load(key string) (guardian.GuardianState, bool) {
	var st guardian.GuardianState
	b, ok, err := g.store.GetGuardianState(context.Background(), g.userID, key)
	if err != nil || !ok {
		return st, false
	}
	if json.Unmarshal(b, &st) != nil {
		return st, false
	}
	return st, true
}

// Delete removes the guardian's persisted state row.
func (g *guardianStateStore) Delete(key string) error {
	return g.store.DeleteGuardianState(context.Background(), g.userID, key)
}
