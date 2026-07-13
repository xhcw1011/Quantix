package live

import "testing"

func TestFillForOtherSymbol(t *testing.T) {
	cases := []struct {
		name            string
		fillSym, engSym string
		want            bool
	}{
		{"other engine's fill on shared account -> ignore", "ETHUSDT", "BTCUSDT", true},
		{"own symbol -> process", "BTCUSDT", "BTCUSDT", false},
		{"unknown fill symbol -> don't drop", "", "BTCUSDT", false},
		{"unknown engine symbol -> don't over-filter", "ETHUSDT", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fillForOtherSymbol(c.fillSym, c.engSym); got != c.want {
				t.Errorf("fillForOtherSymbol(%q,%q) = %v, want %v", c.fillSym, c.engSym, got, c.want)
			}
		})
	}
}
