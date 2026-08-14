package data

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/zap"
)

// stopOf unmarshals just the "stop" field for comparison, sidestepping JSONB's
// storage-level reformatting (e.g. Postgres inserts a space after ':').
func stopOf(t *testing.T, b []byte) float64 {
	t.Helper()
	var v struct {
		Stop float64 `json:"stop"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return v.Stop
}

// newTestStore connects to the local dev Postgres (same DSN convention used by
// the project's other DB-backed tests/tools, e.g. cmd/*-probe). Skips instead
// of failing when no local DB is reachable (CI without Postgres).
func newTestStore(t *testing.T) *Store {
	t.Helper()
	log := zap.NewNop()
	s, err := New(context.Background(), "postgresql://quantix:quantix_secret@localhost:5432/quantix", log)
	if err != nil {
		t.Skipf("no local postgres available: %v", err)
	}
	t.Cleanup(func() { s.pool.Close() })
	return s
}

// TestGuardianState_IsolatedPerUser reproduces the 2026-08-05 incident: two
// different users running an engine with the IDENTICAL engine_id string
// ("SYMBOL-INTERVAL-STRATEGY" collides whenever two users pick the same
// symbol/interval/strategy) must never see each other's persisted trail state.
func TestGuardianState_IsolatedPerUser(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const engineID = "BTCUSDT-5m-guardian" // deliberately identical for both users
	const userA, userB = 90001, 90002      // high IDs unlikely to collide with real users

	if err := s.UpsertGuardianState(ctx, userA, engineID, []byte(`{"stop":63344.98}`)); err != nil {
		t.Fatalf("upsert userA: %v", err)
	}
	if err := s.UpsertGuardianState(ctx, userB, engineID, []byte(`{"stop":68289}`)); err != nil {
		t.Fatalf("upsert userB: %v", err)
	}

	gotA, ok, err := s.GetGuardianState(ctx, userA, engineID)
	if err != nil || !ok {
		t.Fatalf("get userA: ok=%v err=%v", ok, err)
	}
	if stopOf(t, gotA) != 63344.98 {
		t.Fatalf("userA got contaminated by userB's state: %s", gotA)
	}

	gotB, ok, err := s.GetGuardianState(ctx, userB, engineID)
	if err != nil || !ok {
		t.Fatalf("get userB: ok=%v err=%v", ok, err)
	}
	if stopOf(t, gotB) != 68289 {
		t.Fatalf("userB got contaminated by userA's state: %s", gotB)
	}

	// cleanup so repeated test runs don't accumulate rows
	_, _ = s.pool.Exec(ctx, "DELETE FROM guardian_state WHERE user_id IN ($1,$2)", userA, userB)
}

// TestDeleteGuardianState_RemovesRow is the regression test for the guardian
// Phase-machine refactor's required companion fix: a guardian must clear its
// persisted state on reaching PhaseClosed, or a future, unrelated guardian
// that reuses the same (user, engine_id) inherits stale state — most
// dangerously a stale Closing:true, which would permanently block a brand
// new guardian from ever arming/trailing (2026-08-07 finding).
func TestDeleteGuardianState_RemovesRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const engineID = "ETHUSDT-5m-guardian-delete-test"
	const userID = 90003

	if err := s.UpsertGuardianState(ctx, userID, engineID, []byte(`{"stop":1}`)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, ok, err := s.GetGuardianState(ctx, userID, engineID); err != nil || !ok {
		t.Fatalf("expected state to exist before delete: ok=%v err=%v", ok, err)
	}

	if err := s.DeleteGuardianState(ctx, userID, engineID); err != nil {
		t.Fatalf("DeleteGuardianState: %v", err)
	}

	if _, ok, err := s.GetGuardianState(ctx, userID, engineID); err != nil || ok {
		t.Fatalf("expected no state after delete: ok=%v err=%v", ok, err)
	}
}
