package guardian

import "testing"

func TestMemStateStore_Delete(t *testing.T) {
	s := NewMemStateStore()
	_ = s.Save("k1", GuardianState{Stop: 100})

	if _, ok := s.Load("k1"); !ok {
		t.Fatal("expected state to exist before Delete")
	}
	if err := s.Delete("k1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.Load("k1"); ok {
		t.Error("expected state to be gone after Delete")
	}
}

func TestGuardianState_ClosingField(t *testing.T) {
	s := NewMemStateStore()
	_ = s.Save("k1", GuardianState{Stop: 100, Closing: true})

	st, ok := s.Load("k1")
	if !ok {
		t.Fatal("expected state to load")
	}
	if !st.Closing {
		t.Error("expected Closing to round-trip as true")
	}
}
