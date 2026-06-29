package aistrat

import "testing"

// stickyHourlyDir applies hysteresis to the raw 1h trend direction so a confirmed
// ±1 holds for stickyBars subsequent neutral readings before decaying to 0 — this
// stops the entry filter from re-allowing counter-trend entries on every small
// bounce in a stair-step trend. An opposite confirmed reading flips immediately.
// stickyBars<=0 disables it (passthrough = raw, cooldown 0).
func TestStickyHourlyDir(t *testing.T) {
	tests := []struct {
		name                                 string
		raw, prevSticky, cooldown, stickyBars int
		wantDir, wantCd                       int
	}{
		{"off: passthrough bearish", -1, 0, 0, 0, -1, 0},
		{"off: passthrough neutral ignores prev", 0, -1, 5, 0, 0, 0},
		{"confirm bearish sets full cooldown", -1, 0, 0, 12, -1, 12},
		{"hold bearish on neutral flicker, decay", 0, -1, 12, 12, -1, 11},
		{"hold bearish on last cooldown bar", 0, -1, 1, 12, -1, 0},
		{"decay to neutral when cooldown exhausted", 0, -1, 0, 12, 0, 0},
		{"reconfirm bearish resets cooldown", -1, -1, 3, 12, -1, 12},
		{"opposite confirmed flips immediately", 1, -1, 5, 12, 1, 12},
		{"hold bullish on neutral flicker", 0, 1, 4, 12, 1, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDir, gotCd := stickyHourlyDir(tt.raw, tt.prevSticky, tt.cooldown, tt.stickyBars)
			if gotDir != tt.wantDir || gotCd != tt.wantCd {
				t.Errorf("stickyHourlyDir(raw=%d prev=%d cd=%d bars=%d) = (%d,%d), want (%d,%d)",
					tt.raw, tt.prevSticky, tt.cooldown, tt.stickyBars, gotDir, gotCd, tt.wantDir, tt.wantCd)
			}
		})
	}
}

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
