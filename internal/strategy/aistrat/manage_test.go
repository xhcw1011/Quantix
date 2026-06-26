package aistrat

import "testing"

// trendCutTriggered decides whether a range/grid position (which has no normal SL
// and otherwise rides to the -3R catastrophic stop) should be cut EARLY because the
// 1h trend has confirmed against it. It must be both (a) underwater past trendCutR
// and (b) facing a confirmed trend in the wrong direction. A normal pre-reversion
// dip (hourlyDir==0) is NOT cut — it keeps full -3R room.
func TestTrendCutTriggered(t *testing.T) {
	tests := []struct {
		name      string
		side      string
		pnlR      float64
		trendCutR float64
		hourlyDir int
		want      bool
	}{
		{"long: deep loss + downtrend confirmed → cut", "LONG", -1.6, -1.5, -1, true},
		{"long: deep loss but no trend → ride to -3R", "LONG", -1.6, -1.5, 0, false},
		{"long: not deep enough → no cut", "LONG", -1.0, -1.5, -1, false},
		{"long: uptrend is WITH the long → no cut", "LONG", -2.0, -1.5, 1, false},
		{"long: exactly at threshold → cut", "LONG", -1.5, -1.5, -1, true},
		{"short: deep loss + uptrend confirmed → cut", "SHORT", -1.6, -1.5, 1, true},
		{"short: downtrend is WITH the short → no cut", "SHORT", -1.6, -1.5, -1, false},
		{"short: deep loss but no trend → ride to -3R", "SHORT", -2.0, -1.5, 0, false},
		{"disabled: trendCutR==0 never cuts", "LONG", -5.0, 0, -1, false},
		{"disabled: trendCutR positive never cuts", "LONG", -5.0, 1.5, -1, false},
		{"winning position never cuts", "LONG", 2.0, -1.5, -1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trendCutTriggered(tt.side, tt.pnlR, tt.trendCutR, tt.hourlyDir); got != tt.want {
				t.Errorf("trendCutTriggered(side=%s pnlR=%.2f trendCutR=%.2f dir=%d) = %v, want %v",
					tt.side, tt.pnlR, tt.trendCutR, tt.hourlyDir, got, tt.want)
			}
		})
	}
}
