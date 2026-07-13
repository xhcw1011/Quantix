package rebalancer

import (
	"testing"
	"time"
)

func TestNextTickSameDayBeforeHour(t *testing.T) {
	now := time.Date(2026, 3, 10, 7, 0, 0, 0, time.UTC) // before 08:00
	got := NextTick(now, 1, 8)
	want := time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNextTickRollsToNextDayAfterHour(t *testing.T) {
	now := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC) // past 08:00
	got := NextTick(now, 1, 8)
	want := time.Date(2026, 3, 11, 8, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNextTickEveryNDaysProperties(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	got := NextTick(now, 3, 8)
	if !got.After(now) {
		t.Fatalf("next tick %v must be after now %v", got, now)
	}
	if got.Hour() != 8 || got.Minute() != 0 || got.Second() != 0 {
		t.Fatalf("tick must land at 08:00:00, got %v", got)
	}
	if int(got.Unix()/86400)%3 != 0 {
		t.Fatalf("tick day must be on the every-3-day grid, got %v", got)
	}
	if got.Sub(now) > 3*24*time.Hour {
		t.Fatalf("next tick within 3 days, got gap %v", got.Sub(now))
	}
	// consecutive ticks are exactly everyDays apart
	next := NextTick(got.Add(time.Second), 3, 8)
	if next.Sub(got) != 3*24*time.Hour {
		t.Fatalf("consecutive ticks must be 3 days apart, got %v", next.Sub(got))
	}
}

func TestNextTickHours(t *testing.T) {
	// every 8h → fires at 00:00 / 08:00 / 16:00 UTC.
	now := time.Date(2026, 3, 10, 9, 30, 0, 0, time.UTC) // 09:30 → next is 16:00
	got := NextTickHours(now, 8)
	want := time.Date(2026, 3, 10, 16, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// at exactly 16:00 → next is 00:00 the next day (strictly after)
	got2 := NextTickHours(want, 8)
	want2 := time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC)
	if !got2.Equal(want2) {
		t.Fatalf("got %v, want %v", got2, want2)
	}
	// consecutive ticks are exactly 8h apart
	if NextTickHours(want.Add(time.Second), 8).Sub(want) != 8*time.Hour {
		t.Fatalf("consecutive 8h ticks must be 8h apart")
	}
}
