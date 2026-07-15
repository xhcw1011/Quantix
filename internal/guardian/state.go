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
}

// StateStore persists a Guardian's trail state, keyed by engine id. The live
// engine supplies a DB-backed implementation; tests use MemStateStore.
type StateStore interface {
	Save(key string, s GuardianState) error
	Load(key string) (GuardianState, bool)
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
