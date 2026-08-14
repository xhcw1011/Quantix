package macross

import "sync"

// PendingEntryState is the persisted shape of a resting limit entry. Only the
// order ID matters — a restart doesn't try to resume tracking a stale limit
// order (its price may no longer be favourable, and macross has no mechanism
// analogous to guardian's live-position resync to safely re-adopt it); it
// just cancels the order and starts clean, closing the "orphaned resting
// limit order" gap left when EntryOrderType="limit" first shipped without
// restart-safety.
type PendingEntryState struct {
	OrderID string `json:"order_id"`
}

// StateStore persists a MACross's pending-entry order ID so a restart can
// clean up a still-resting limit order instead of silently forgetting about
// it. nil disables persistence (backtest/paper — restart-safety doesn't
// apply there). The live engine supplies a DB-backed implementation, keyed by
// (user, engine) in the concrete adapter; tests use MemStateStore.
type StateStore interface {
	Save(state PendingEntryState) error
	Load() (PendingEntryState, bool)
	Clear() error
}

// MemStateStore is an in-memory StateStore (tests, and a safe default).
type MemStateStore struct {
	mu    sync.Mutex
	state PendingEntryState
	has   bool
}

// NewMemStateStore creates an empty in-memory store.
func NewMemStateStore() *MemStateStore { return &MemStateStore{} }

func (s *MemStateStore) Save(state PendingEntryState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state, s.has = state, true
	return nil
}

func (s *MemStateStore) Load() (PendingEntryState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.has
}

func (s *MemStateStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state, s.has = PendingEntryState{}, false
	return nil
}
