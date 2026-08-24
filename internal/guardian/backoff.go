package guardian

import "time"

// rejectBackoff paces retries for an order that can be rejected for a
// persistent reason (e.g. a risk-gate leverage cap, insufficient margin)
// that won't resolve on its own with the same request. A flat retry
// interval alone isn't enough: retried forever at a fixed cadence, it
// floods both the exchange API and Telegram (2026-08-24 incident — a
// leverage-cap rejection retried every 30s for 10 straight hours, one
// notification each time). Backoff grows the wait between attempts up to a
// cap; notification is throttled independently so the user is told once,
// then reminded periodically, not paged every single retry.
type rejectBackoff struct {
	count      int
	retryAfter time.Time
	notifiedAt time.Time
}

// ready reports whether enough time has passed since the last rejection to
// attempt again. True (zero value) before any rejection has been recorded.
func (b *rejectBackoff) ready() bool {
	return time.Now().After(b.retryAfter)
}

// record bumps the backoff after a fresh rejection and reports whether the
// caller should notify this time: always on the first rejection, then at
// most once per notifyEvery while the problem persists.
func (b *rejectBackoff) record(base, maxWait, notifyEvery time.Duration) (notify bool) {
	b.count++
	shift := b.count - 1
	if shift > 20 { // guard against overflow on a very long streak
		shift = 20
	}
	wait := base * time.Duration(int64(1)<<uint(shift))
	if wait > maxWait || wait <= 0 { // wait<=0 also catches the overflow case
		wait = maxWait
	}
	b.retryAfter = time.Now().Add(wait)
	if b.notifiedAt.IsZero() || time.Since(b.notifiedAt) >= notifyEvery {
		b.notifiedAt = time.Now()
		return true
	}
	return false
}

// reset clears the backoff after a successful attempt, so a future
// rejection starts fresh instead of inheriting a long-grown wait/silence
// from an unrelated, already-resolved problem.
func (b *rejectBackoff) reset() {
	*b = rejectBackoff{}
}
