package live

import (
	"testing"
	"time"
)

func TestWatchdogStaleThreshold(t *testing.T) {
	cases := []struct {
		name        string
		barInterval time.Duration
		want        time.Duration
	}{
		{"unknown interval falls back to 10m floor", 0, 10 * time.Minute},
		{"1m stays at floor", time.Minute, 10 * time.Minute},
		{"5m stays at floor (15m<... no, 3x=15m)", 5 * time.Minute, 15 * time.Minute},
		{"15m -> 45m (3 missed bars)", 15 * time.Minute, 45 * time.Minute},
		{"1h -> 3h", time.Hour, 3 * time.Hour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := watchdogStaleThreshold(c.barInterval); got != c.want {
				t.Errorf("watchdogStaleThreshold(%v) = %v, want %v", c.barInterval, got, c.want)
			}
		})
	}
}
