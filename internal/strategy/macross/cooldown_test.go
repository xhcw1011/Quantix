package macross

import "testing"

func TestCooldownActive(t *testing.T) {
	cases := []struct {
		name                     string
		everClosed               bool
		barsSinceClose, cooldown int
		want                     bool
	}{
		{"disabled (cooldown<=0) -> never blocks", true, 0, 0, false},
		{"never closed yet -> never blocks (nothing to cool down from)", false, 0, 5, false},
		{"just closed, cooldown 3 -> blocks", true, 0, 3, true},
		{"1 of 3 bars elapsed -> still blocks", true, 1, 3, true},
		{"exactly at cooldown boundary -> no longer blocks", true, 3, 3, false},
		{"well past cooldown -> no longer blocks", true, 10, 3, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cooldownActive(c.everClosed, c.barsSinceClose, c.cooldown)
			if got != c.want {
				t.Errorf("cooldownActive(%v,%d,%d) = %v, want %v", c.everClosed, c.barsSinceClose, c.cooldown, got, c.want)
			}
		})
	}
}
