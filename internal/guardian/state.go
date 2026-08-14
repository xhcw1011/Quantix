package guardian

import "sync"

// GuardianState is the minimal trail state persisted so a restart restores the
// advanced stop instead of resetting it to the initial protective level.
type GuardianState struct {
	Stop             float64 `json:"stop"`
	PeakR            float64 `json:"peak_r"`
	Activated        bool    `json:"activated"`
	StopOrderID      string  `json:"stop_order_id"`
	RestingStopPrice float64 `json:"resting_stop_price"`
	// EntryPlaced records that this guardian's one-shot "place the entry for me"
	// instruction (PlaceEntry/wantEntry) has already fired at least once. Without
	// this surviving a restart, a wantEntry guardian re-reads its stored config on
	// every restart and — finding the account flat, for ANY reason, including one
	// it was never told about — silently places a brand-new entry (2026-08-05
	// incident). Once true, restarts while flat stay flat instead of re-entering.
	EntryPlaced bool `json:"entry_placed"`
	// PartialDone records that the one-time partial take-profit has already
	// fired, for the same reason as EntryPlaced: the in-memory guard resets on
	// restart, and if price is still above the trigger, an un-persisted flag
	// would let a restart close ANOTHER fraction of the already-reduced position.
	PartialDone bool `json:"partial_done"`
	// Closing records that a protective close was submitted (or was in flight)
	// and not yet confirmed filled. A restart must NOT re-arm/re-trail while
	// this is true — it must only resync against the live position until the
	// close resolves. Cleared (the whole row deleted, see StateStore.Delete)
	// the moment the guardian reaches PhaseClosed by ANY path, so a future,
	// unrelated guardian reusing the same engine_id never inherits it.
	Closing bool `json:"closing"`
}

// StateStore persists a Guardian's trail state, keyed by engine id. The live
// engine supplies a DB-backed implementation; tests use MemStateStore.
type StateStore interface {
	Save(key string, s GuardianState) error
	Load(key string) (GuardianState, bool)
	// Delete removes the persisted state for key. Called when the guardian
	// reaches PhaseClosed (self-retire or externally-closed) so a future,
	// unrelated guardian instance that reuses the same engine_id never
	// inherits stale state (Closing:true above all — see its doc comment).
	Delete(key string) error
}

// MemStateStore is an in-memory StateStore (tests, and a safe default).
type MemStateStore struct {
	mu sync.Mutex
	m  map[string]GuardianState
}

// NewMemStateStore creates an empty in-memory store.
func NewMemStateStore() *MemStateStore { return &MemStateStore{m: map[string]GuardianState{}} }

// Save records the state for a key.
func (s *MemStateStore) Save(key string, st GuardianState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = st
	return nil
}

// Load returns the stored state and whether it exists.
func (s *MemStateStore) Load(key string) (GuardianState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.m[key]
	return st, ok
}

// Delete removes the state for a key.
func (s *MemStateStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}
