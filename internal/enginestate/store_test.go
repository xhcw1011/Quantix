package enginestate

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

// fakeDB is an in-memory enginestate.DB for tests. Recording the exact args
// passed to Save/Load lets tests verify Store[T] threads userID/engineID/
// namespace through unchanged instead of dropping or merging them.
type fakeDB struct {
	rows        map[[3]string][]byte // {userID, engineID, namespace} -> raw bytes
	lastSaveKey [3]string
	saveErr     error
	loadErr     error
}

func newFakeDB() *fakeDB { return &fakeDB{rows: map[[3]string][]byte{}} }

func key(userID int, engineID, namespace string) [3]string {
	return [3]string{strconv.Itoa(userID), engineID, namespace}
}

func (f *fakeDB) SaveEngineState(_ context.Context, userID int, engineID, namespace string, state []byte) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	k := key(userID, engineID, namespace)
	f.lastSaveKey = k
	cp := append([]byte(nil), state...)
	f.rows[k] = cp
	return nil
}

func (f *fakeDB) LoadEngineState(_ context.Context, userID int, engineID, namespace string) ([]byte, bool, error) {
	if f.loadErr != nil {
		return nil, false, f.loadErr
	}
	b, ok := f.rows[key(userID, engineID, namespace)]
	return b, ok, nil
}

type testState struct {
	Halted     bool    `json:"halted"`
	PeakEquity float64 `json:"peak_equity"`
}

// TestStore_SaveLoadRoundTrip verifies a saved value comes back unchanged.
func TestStore_SaveLoadRoundTrip(t *testing.T) {
	db := newFakeDB()
	s := New[testState](db, "risk")
	ctx := context.Background()

	want := testState{Halted: true, PeakEquity: 12345.6}
	if err := s.Save(ctx, 4, "BTCUSDT-15m-macross", want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, ok, err := s.Load(ctx, 4, "BTCUSDT-15m-macross")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true after a Save")
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestStore_LoadMissingReturnsZeroValue covers the first-ever-start case.
func TestStore_LoadMissingReturnsZeroValue(t *testing.T) {
	db := newFakeDB()
	s := New[testState](db, "risk")

	got, ok, err := s.Load(context.Background(), 4, "no-such-engine")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for a missing state row")
	}
	if got != (testState{}) {
		t.Fatalf("expected zero value, got %+v", got)
	}
}

// TestStore_ThreadsUserAndEngineIDUnchanged is the whole point of this
// package: verify userID/engineID/namespace reach the DB layer exactly as
// given, so this abstraction can't itself reintroduce the missing-userID bug
// class (guardian_state/experiments/composite, 2026-08-05/06) by silently
// dropping or reformatting a key component.
func TestStore_ThreadsUserAndEngineIDUnchanged(t *testing.T) {
	db := newFakeDB()
	s := New[testState](db, "risk")

	if err := s.Save(context.Background(), 13, "ETHUSDT-5m-guardian", testState{Halted: true}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if db.lastSaveKey != key(13, "ETHUSDT-5m-guardian", "risk") {
		t.Fatalf("Save did not thread through the exact (userID, engineID, namespace) given: got %v", db.lastSaveKey)
	}
}

// TestStore_NamespaceIsolation: two Store[T] instances for different
// namespaces sharing a DB must never see each other's state for the same
// (userID, engineID) — this is the "same engine, unrelated subsystems" case.
func TestStore_NamespaceIsolation(t *testing.T) {
	db := newFakeDB()
	risk := New[testState](db, "risk")
	other := New[testState](db, "other")
	ctx := context.Background()

	if err := risk.Save(ctx, 4, "ETHUSDT-5m-guardian", testState{Halted: true}); err != nil {
		t.Fatalf("save risk: %v", err)
	}
	if err := other.Save(ctx, 4, "ETHUSDT-5m-guardian", testState{Halted: false}); err != nil {
		t.Fatalf("save other: %v", err)
	}

	got, ok, err := risk.Load(ctx, 4, "ETHUSDT-5m-guardian")
	if err != nil || !ok {
		t.Fatalf("load risk: ok=%v err=%v", ok, err)
	}
	if !got.Halted {
		t.Fatal("risk namespace state contaminated by the other namespace")
	}
}

// TestStore_MalformedPayloadDegradesToFreshStart matches the established
// convention elsewhere in this codebase (e.g. guardian.recoverState): a
// corrupt/incompatible stored payload must not crash the caller, just be
// treated as "no prior state."
func TestStore_MalformedPayloadDegradesToFreshStart(t *testing.T) {
	db := newFakeDB()
	db.rows[key(4, "BTCUSDT-15m-macross", "risk")] = []byte("not json")
	s := New[testState](db, "risk")

	got, ok, err := s.Load(context.Background(), 4, "BTCUSDT-15m-macross")
	if err != nil {
		t.Fatalf("unexpected error on malformed payload: %v", err)
	}
	if ok {
		t.Fatal("malformed payload must degrade to ok=false, not silently succeed")
	}
	if got != (testState{}) {
		t.Fatalf("expected zero value on malformed payload, got %+v", got)
	}
}

// TestStore_PropagatesDBErrors ensures a genuine DB error (not "missing row")
// surfaces to the caller rather than being swallowed like the malformed-JSON
// case.
func TestStore_PropagatesDBErrors(t *testing.T) {
	db := newFakeDB()
	db.loadErr = errors.New("connection reset")
	s := New[testState](db, "risk")

	_, _, err := s.Load(context.Background(), 4, "BTCUSDT-15m-macross")
	if err == nil {
		t.Fatal("expected a genuine DB error to propagate")
	}
}
