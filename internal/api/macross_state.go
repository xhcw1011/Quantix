package api

import (
	"context"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/enginestate"
	"github.com/Quantix/quantix/internal/strategy/macross"
)

// macrossStateStore adapts the shared enginestate.Store to macross.StateStore
// so a live engine's pending limit entry survives across restarts long enough
// to be cleaned up (see macross.StateStore doc comment — restart-safety was
// added after limit entries first shipped without it, 2026-08-07).
//
// userID is required for the same reason as guardianStateStore/riskStateStore
// — see internal/enginestate.
type macrossStateStore struct {
	store    *enginestate.Store[macross.PendingEntryState]
	userID   int
	engineID string
}

func newMacrossStateStore(db *data.Store, userID int, engineID string) *macrossStateStore {
	return &macrossStateStore{
		store:    enginestate.New[macross.PendingEntryState](db, "macross_pending_entry"),
		userID:   userID,
		engineID: engineID,
	}
}

func (s *macrossStateStore) Save(state macross.PendingEntryState) error {
	return s.store.Save(context.Background(), s.userID, s.engineID, state)
}

func (s *macrossStateStore) Load() (macross.PendingEntryState, bool) {
	v, ok, err := s.store.Load(context.Background(), s.userID, s.engineID)
	if err != nil || !ok {
		return macross.PendingEntryState{}, false
	}
	return v, true
}

func (s *macrossStateStore) Clear() error {
	return s.store.Save(context.Background(), s.userID, s.engineID, macross.PendingEntryState{})
}
