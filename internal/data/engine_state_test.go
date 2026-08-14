package data

import (
	"context"
	"encoding/json"
	"testing"
)

// stateOf unmarshals just the "halted" field for comparison, sidestepping
// JSONB's storage-level reformatting.
func stateOf(t *testing.T, b []byte) bool {
	t.Helper()
	var v struct {
		Halted bool `json:"halted"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return v.Halted
}

// TestEngineState_IsolatedPerUser reproduces the same class of bug as
// TestGuardianState_IsolatedPerUser: two different users running an engine
// with the IDENTICAL engine_id string must never see each other's persisted
// state.
func TestEngineState_IsolatedPerUser(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const engineID = "BTCUSDT-15m-macross" // deliberately identical for both users
	const namespace = "risk"
	const userA, userB = 90010, 90011

	if err := s.SaveEngineState(ctx, userA, engineID, namespace, []byte(`{"halted":true}`)); err != nil {
		t.Fatalf("save userA: %v", err)
	}
	if err := s.SaveEngineState(ctx, userB, engineID, namespace, []byte(`{"halted":false}`)); err != nil {
		t.Fatalf("save userB: %v", err)
	}

	gotA, ok, err := s.LoadEngineState(ctx, userA, engineID, namespace)
	if err != nil || !ok {
		t.Fatalf("load userA: ok=%v err=%v", ok, err)
	}
	if stateOf(t, gotA) != true {
		t.Fatalf("userA got contaminated by userB's state: %s", gotA)
	}

	gotB, ok, err := s.LoadEngineState(ctx, userB, engineID, namespace)
	if err != nil || !ok {
		t.Fatalf("load userB: ok=%v err=%v", ok, err)
	}
	if stateOf(t, gotB) != false {
		t.Fatalf("userB got contaminated by userA's state: %s", gotB)
	}

	_, _ = s.pool.Exec(ctx, "DELETE FROM engine_state WHERE user_id IN ($1,$2)", userA, userB)
}

// TestEngineState_IsolatedPerNamespace verifies two unrelated subsystems
// sharing the same (user_id, engine_id) -- e.g. two different pieces of
// state for the same engine -- never collide with each other either.
func TestEngineState_IsolatedPerNamespace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const userID = 90012
	const engineID = "ETHUSDT-5m-guardian"

	if err := s.SaveEngineState(ctx, userID, engineID, "risk", []byte(`{"halted":true}`)); err != nil {
		t.Fatalf("save risk: %v", err)
	}
	if err := s.SaveEngineState(ctx, userID, engineID, "other", []byte(`{"halted":false}`)); err != nil {
		t.Fatalf("save other: %v", err)
	}

	gotRisk, ok, err := s.LoadEngineState(ctx, userID, engineID, "risk")
	if err != nil || !ok {
		t.Fatalf("load risk: ok=%v err=%v", ok, err)
	}
	if stateOf(t, gotRisk) != true {
		t.Fatalf("risk namespace contaminated by other: %s", gotRisk)
	}

	_, _ = s.pool.Exec(ctx, "DELETE FROM engine_state WHERE user_id = $1", userID)
}

// TestGetEngineState_MissingReturnsOK verifies the "no prior state" path
// (first ever start) doesn't error, just reports ok=false.
func TestGetEngineState_MissingReturnsOK(t *testing.T) {
	s := newTestStore(t)
	_, ok, err := s.LoadEngineState(context.Background(), 90013, "no-such-engine", "risk")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for a missing state row")
	}
}
