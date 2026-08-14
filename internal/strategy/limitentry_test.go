package strategy

import "testing"

func TestPendingEntry_ZeroValueInactive(t *testing.T) {
	var p PendingEntry
	if p.Active() {
		t.Error("zero-value PendingEntry should be inactive")
	}
}

func TestPendingEntry_ActiveAfterOrderIDSet(t *testing.T) {
	p := PendingEntry{OrderID: "abc"}
	if !p.Active() {
		t.Error("expected Active() to be true once OrderID is set")
	}
}

func TestPendingEntry_Clear(t *testing.T) {
	p := PendingEntry{OrderID: "abc", Bars: 2, Fallback: OpenLong("BTCUSDT", 1)}
	p.Clear()
	if p.Active() {
		t.Error("expected Clear() to reset Active() to false")
	}
	if p.Bars != 0 {
		t.Errorf("expected Bars reset to 0, got %d", p.Bars)
	}
}

func TestPendingEntry_TimeoutCountsBarsAndReportsWhenReached(t *testing.T) {
	p := PendingEntry{OrderID: "abc"}
	if p.Timeout(3) {
		t.Error("expected Timeout(3) to be false on bar 1")
	}
	if p.Timeout(3) {
		t.Error("expected Timeout(3) to be false on bar 2")
	}
	if !p.Timeout(3) {
		t.Error("expected Timeout(3) to be true on bar 3")
	}
}
