package spotgrid

import (
	"testing"
)

// ─────────────────────────────────────────────
// gridShouldBuy tests
// ─────────────────────────────────────────────

func TestGridShouldBuy(t *testing.T) {
	tests := []struct {
		name         string
		price        float64
		lastBuyPrice float64
		stepPct      float64
		want         bool
	}{
		// First buy — no prior buy recorded.
		{"first buy (lastBuyPrice=0)", 100, 0, 0.02, true},

		// Exactly at the step threshold: 100 * (1 - 0.02) = 98.0.
		{"price exactly stepPct below lastBuy", 98, 100, 0.02, true},

		// One tick above threshold: not yet fallen enough.
		{"price just above threshold", 98.01, 100, 0.02, false},

		// Well below threshold — definitely buy.
		{"price far below lastBuy", 90, 100, 0.02, true},

		// Price unchanged since last buy — no new buy.
		{"price equal to lastBuy", 100, 100, 0.02, false},

		// Price risen above last buy — no buy on rally.
		{"price above lastBuy", 105, 100, 0.02, false},

		// Slightly below but not enough (1% drop, need 2%).
		{"price 1pct below, step=2pct", 99, 100, 0.02, false},

		// Different step: 5% step, price 5% below.
		{"5pct step, 5pct drop", 95, 100, 0.05, true},

		// 5% step, only 4% drop — not enough.
		{"5pct step, 4pct drop", 96, 100, 0.05, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gridShouldBuy(tt.price, tt.lastBuyPrice, tt.stepPct)
			if got != tt.want {
				t.Errorf("gridShouldBuy(price=%.2f, lastBuy=%.2f, step=%.3f) = %v, want %v",
					tt.price, tt.lastBuyPrice, tt.stepPct, got, tt.want)
			}
		})
	}
}

// ─────────────────────────────────────────────
// gridTrancheToSell tests
// ─────────────────────────────────────────────

func TestGridTrancheToSell(t *testing.T) {
	tests := []struct {
		name     string
		tranches []tranche
		price    float64
		stepPct  float64
		want     int // expected index or -1
	}{
		// Empty tranche list.
		{"empty tranches", nil, 103, 0.02, -1},

		// Single tranche eligible: 100 * 1.02 = 102 <= 103.
		{"single tranche eligible", []tranche{{price: 100, qty: 1}}, 103, 0.02, 0},

		// Single tranche not yet eligible: 100 * 1.02 = 102 > 101.
		{"single tranche not eligible", []tranche{{price: 100, qty: 1}}, 101, 0.02, -1},

		// Exactly at threshold: 100 * 1.02 = 102, price = 102.
		{"price exactly at threshold", []tranche{{price: 100, qty: 1}}, 102, 0.02, 0},

		// Two tranches [100, 105], price=103: only idx 0 eligible (100*1.02=102<=103; 105*1.02=107.1>103).
		{"two tranches, lowest eligible", []tranche{{price: 100, qty: 1}, {price: 105, qty: 1}}, 103, 0.02, 0},

		// Two tranches [100, 105], price=108: both eligible, return lowest (idx 0, FIFO).
		{"two tranches both eligible return lowest", []tranche{{price: 100, qty: 1}, {price: 105, qty: 1}}, 108, 0.02, 0},

		// Two tranches [100, 105], price=101: neither eligible.
		{"two tranches neither eligible", []tranche{{price: 100, qty: 1}, {price: 105, qty: 1}}, 101, 0.02, -1},

		// Three tranches [90, 95, 100], price=97: only 90*1.02=91.8<=97 and 95*1.02=96.9<=97 are eligible;
		// 100*1.02=102>97. Pick lowest = idx 0.
		{"three tranches pick lowest", []tranche{{price: 90, qty: 1}, {price: 95, qty: 1}, {price: 100, qty: 1}}, 97, 0.02, 0},

		// Non-ascending order: tranches [105, 100], price=103. idx 1 has price 100 eligible (102<=103),
		// idx 0 has price 105 (107.1>103). Should pick the tranche with the lowest price = idx 1.
		{"out-of-order tranches pick lowest price", []tranche{{price: 105, qty: 1}, {price: 100, qty: 1}}, 103, 0.02, 1},

		// No tranche — price risen but list empty.
		{"empty, high price", nil, 200, 0.02, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gridTrancheToSell(tt.tranches, tt.price, tt.stepPct)
			if got != tt.want {
				t.Errorf("gridTrancheToSell(%v, price=%.2f, step=%.3f) = %d, want %d",
					tt.tranches, tt.price, tt.stepPct, got, tt.want)
			}
		})
	}
}
