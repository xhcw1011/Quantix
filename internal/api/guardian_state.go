package api

import (
	"context"
	"encoding/json"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/guardian"
)

// guardianStateStore adapts the DB store to guardian.StateStore so a live Guardian
// persists and restores its trailed stop across engine/server restarts.
type guardianStateStore struct{ store *data.Store }

// Save marshals and upserts the guardian's trail state.
func (g *guardianStateStore) Save(key string, s guardian.GuardianState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return g.store.UpsertGuardianState(context.Background(), key, b)
}

// Load fetches and unmarshals the guardian's trail state (ok=false if absent/bad).
func (g *guardianStateStore) Load(key string) (guardian.GuardianState, bool) {
	var st guardian.GuardianState
	b, ok, err := g.store.GetGuardianState(context.Background(), key)
	if err != nil || !ok {
		return st, false
	}
	if json.Unmarshal(b, &st) != nil {
		return st, false
	}
	return st, true
}
