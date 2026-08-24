package guardian

import (
	"testing"
	"time"
)

// TestRejectBackoff_GrowsAndCaps is the regression test for the 2026-08-24
// incident: a persistent rejection (risk-gate leverage cap) retried every
// 30s for 10 straight hours because the cooldown never grew. Backoff must
// escalate and settle at a cap, not stay flat forever.
func TestRejectBackoff_GrowsAndCaps(t *testing.T) {
	var b rejectBackoff
	base, maxWait := time.Second, 8*time.Second

	waits := []time.Duration{}
	for i := 0; i < 6; i++ {
		before := time.Now()
		b.record(base, maxWait, time.Hour) // notifyEvery large: isolate backoff growth
		waits = append(waits, b.retryAfter.Sub(before))
	}
	// 1s, 2s, 4s, 8s(cap), 8s, 8s -- allow slack for test execution time.
	want := []time.Duration{1, 2, 4, 8, 8, 8}
	for i, w := range want {
		got := waits[i]
		if got < w*time.Second-100*time.Millisecond || got > w*time.Second+200*time.Millisecond {
			t.Fatalf("attempt %d: wait = %v, want ~%v", i+1, got, w*time.Second)
		}
	}
}

func TestRejectBackoff_NotifiesFirstThenThrottles(t *testing.T) {
	var b rejectBackoff
	notifyEvery := 50 * time.Millisecond

	if notify := b.record(time.Millisecond, time.Millisecond, notifyEvery); !notify {
		t.Fatal("first rejection must always notify")
	}
	if notify := b.record(time.Millisecond, time.Millisecond, notifyEvery); notify {
		t.Fatal("a rejection immediately after must not notify again (within notifyEvery)")
	}
	time.Sleep(notifyEvery + 10*time.Millisecond)
	if notify := b.record(time.Millisecond, time.Millisecond, notifyEvery); !notify {
		t.Fatal("a rejection after notifyEvery has elapsed must notify again")
	}
}

func TestRejectBackoff_ReadyAndReset(t *testing.T) {
	var b rejectBackoff
	if !b.ready() {
		t.Fatal("a fresh backoff must be immediately ready")
	}
	b.record(time.Hour, time.Hour, time.Hour)
	if b.ready() {
		t.Fatal("must not be ready immediately after a rejection with an hour-long wait")
	}
	b.reset()
	if !b.ready() {
		t.Fatal("reset must clear the pending wait")
	}
	if b.count != 0 {
		t.Fatalf("reset must clear the count, got %d", b.count)
	}
}
