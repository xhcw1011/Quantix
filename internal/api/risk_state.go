package api

import (
	"context"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/enginestate"
)

// riskPersistedState is the JSON shape enginestate.Store persists for the
// "risk" namespace.
type riskPersistedState struct {
	Halted     bool    `json:"halted"`
	PeakEquity float64 `json:"peak_equity"`
}

// riskStateStore adapts the shared enginestate.Store to risk.StateStore so a
// live engine's circuit breaker persists and restores its halted/peak-equity
// state across engine/server restarts.
//
// userID is required for the same reason as guardianStateStore: engine_id
// ("SYMBOL-INTERVAL-STRATEGY") is only unique WITHIN one user's engines, not
// globally — see internal/enginestate for the shared abstraction this is
// built on (2026-08-06).
type riskStateStore struct {
	store    *enginestate.Store[riskPersistedState]
	userID   int
	engineID string
}

// newRiskStateStore wires the shared engine-scoped state store for the
// "risk" namespace.
func newRiskStateStore(db *data.Store, userID int, engineID string) *riskStateStore {
	return &riskStateStore{
		store:    enginestate.New[riskPersistedState](db, "risk"),
		userID:   userID,
		engineID: engineID,
	}
}

// Save persists the circuit breaker's current state.
func (r *riskStateStore) Save(halted bool, peakEquity float64) error {
	return r.store.Save(context.Background(), r.userID, r.engineID, riskPersistedState{
		Halted: halted, PeakEquity: peakEquity,
	})
}

// Load fetches the persisted state (ok=false if none exists yet).
func (r *riskStateStore) Load() (halted bool, peakEquity float64, ok bool) {
	v, ok, err := r.store.Load(context.Background(), r.userID, r.engineID)
	if err != nil || !ok {
		return false, 0, false
	}
	return v.Halted, v.PeakEquity, true
}
