package dca

import (
	"math"
	"testing"
	"time"
)

func TestDCAPeriodKey(t *testing.T) {
	mk := func(s string) time.Time { tm, _ := time.Parse(time.RFC3339, s); return tm }
	tests := []struct{ name, interval, ts, want string }{
		{"daily", "daily", "2026-07-01T09:00:00Z", "2026-07-01"},
		{"daily same day later hour", "daily", "2026-07-01T23:00:00Z", "2026-07-01"},
		{"daily next day", "daily", "2026-07-02T00:30:00Z", "2026-07-02"},
		{"weekly ISO week", "weekly", "2026-07-01T09:00:00Z", "2026-W27"},
		{"weekly case-insensitive", "WEEKLY", "2026-07-01T09:00:00Z", "2026-W27"},
		{"monthly", "monthly", "2026-07-15T09:00:00Z", "2026-07"},
		{"unknown interval defaults to daily", "hourly", "2026-07-01T09:00:00Z", "2026-07-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dcaPeriodKey(tt.interval, mk(tt.ts)); got != tt.want {
				t.Errorf("dcaPeriodKey(%s, %s) = %s, want %s", tt.interval, tt.ts, got, tt.want)
			}
		})
	}
}

func TestDCALimitPrice(t *testing.T) {
	tests := []struct {
		name                 string
		market, offset, want float64
	}{
		{"below market (patient maker)", 60000, -0.003, 59820},
		{"above market (crosses spread)", 60000, 0.001, 60060},
		{"at market (offset 0)", 60000, 0, 60000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dcaLimitPrice(tt.market, tt.offset)
			if math.Abs(got-tt.want) > 1e-6 {
				t.Errorf("dcaLimitPrice(%.0f, %.4f) = %.4f, want %.4f", tt.market, tt.offset, got, tt.want)
			}
		})
	}
}
