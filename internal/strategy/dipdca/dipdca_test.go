package dipdca

import (
	"math"
	"testing"
	"time"
)

// ─── dcaPeriodKey ───────────────────────────────────────────────────────────

func TestDCAPeriodKey(t *testing.T) {
	mk := func(s string) time.Time { tm, _ := time.Parse(time.RFC3339, s); return tm }
	tests := []struct{ name, interval, ts, want string }{
		{"daily", "daily", "2026-07-01T09:00:00Z", "2026-07-01"},
		{"daily same day later hour", "daily", "2026-07-01T23:00:00Z", "2026-07-01"},
		{"daily next day", "daily", "2026-07-02T00:30:00Z", "2026-07-02"},
		{"weekly ISO week", "weekly", "2026-07-01T09:00:00Z", "2026-W27"},
		{"weekly case-insensitive upper", "WEEKLY", "2026-07-01T09:00:00Z", "2026-W27"},
		{"weekly case-insensitive mixed", "Weekly", "2026-07-01T09:00:00Z", "2026-W27"},
		{"monthly", "monthly", "2026-07-15T09:00:00Z", "2026-07"},
		{"monthly case-insensitive", "MONTHLY", "2026-07-15T09:00:00Z", "2026-07"},
		{"unknown interval defaults to daily", "hourly", "2026-07-01T09:00:00Z", "2026-07-01"},
		{"empty interval defaults to daily", "", "2026-07-01T09:00:00Z", "2026-07-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dcaPeriodKey(tt.interval, mk(tt.ts))
			if got != tt.want {
				t.Errorf("dcaPeriodKey(%q, %s) = %q, want %q", tt.interval, tt.ts, got, tt.want)
			}
		})
	}
}

// ─── dipBuyAmount ───────────────────────────────────────────────────────────

func TestDipBuyAmount(t *testing.T) {
	const epsilon = 1e-9

	tests := []struct {
		name                                  string
		base, price, sma, dipRefPct, dipMult float64
		want                                  float64
	}{
		{
			name: "price equal to sma — no scaling",
			base: 100, price: 1000, sma: 1000, dipRefPct: 0.10, dipMult: 2.0,
			want: 100, // clamp(0/0.1,0,1)=0 → 100*(1+0)=100
		},
		{
			name: "price above sma — no scaling",
			base: 100, price: 1050, sma: 1000, dipRefPct: 0.10, dipMult: 2.0,
			want: 100,
		},
		{
			name: "price 10% below sma, dipRefPct 0.10, mult 2.0 — full multiplier",
			base: 100, price: 900, sma: 1000, dipRefPct: 0.10, dipMult: 2.0,
			want: 200, // clamp(0.10/0.10,0,1)=1 → 100*(1+1*(2-1))=200
		},
		{
			name: "price 5% below sma, dipRefPct 0.10, mult 2.0 — half multiplier",
			base: 100, price: 950, sma: 1000, dipRefPct: 0.10, dipMult: 2.0,
			want: 150, // clamp(0.05/0.10,0,1)=0.5 → 100*(1+0.5*(2-1))=150
		},
		{
			name: "price 20% below sma, dipRefPct 0.10, mult 2.0 — clamped at full",
			base: 100, price: 800, sma: 1000, dipRefPct: 0.10, dipMult: 2.0,
			want: 200, // clamp(0.20/0.10,0,1)=1 → 100*(1+1*1)=200
		},
		{
			name: "sma <= 0 — return base unchanged",
			base: 100, price: 900, sma: 0, dipRefPct: 0.10, dipMult: 2.0,
			want: 100,
		},
		{
			name: "sma negative — return base unchanged",
			base: 100, price: 900, sma: -50, dipRefPct: 0.10, dipMult: 2.0,
			want: 100,
		},
		{
			name: "dipMultiplier < 1 treated as 1 — return base",
			base: 100, price: 900, sma: 1000, dipRefPct: 0.10, dipMult: 0.5,
			want: 100, // mult clamped to 1 → 100*(1+clamp*0)=100
		},
		{
			name: "dipMultiplier exactly 1 — no scaling regardless of dip",
			base: 100, price: 900, sma: 1000, dipRefPct: 0.10, dipMult: 1.0,
			want: 100,
		},
		{
			name: "dipRefPct 0 — edge: avoid divide-by-zero, clamp(inf,0,1)=1 full mult",
			base: 100, price: 900, sma: 1000, dipRefPct: 0.0, dipMult: 2.0,
			want: 200, // price<sma → dip>0 → dip/0 treated as infinity → clamp→1
		},
		{
			name: "price just below sma — tiny dip",
			base: 100, price: 999, sma: 1000, dipRefPct: 0.10, dipMult: 2.0,
			want: 101, // dip=0.001 → clamp(0.001/0.1,0,1)=0.01 → 100*(1+0.01)=101
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dipBuyAmount(tt.base, tt.price, tt.sma, tt.dipRefPct, tt.dipMult)
			if math.Abs(got-tt.want) > epsilon {
				t.Errorf("dipBuyAmount(base=%.0f price=%.0f sma=%.0f ref=%.2f mult=%.1f) = %.6f, want %.6f",
					tt.base, tt.price, tt.sma, tt.dipRefPct, tt.dipMult, got, tt.want)
			}
		})
	}
}
