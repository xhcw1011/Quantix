package macross

import "testing"

func TestPrimeDirection(t *testing.T) {
	cases := []struct {
		name                            string
		sawWarmup, isWarmup, primed, flat bool
		fast, slow                      float64
		want                            int
	}{
		{"first live bar after warmup, uptrend -> long", true, false, false, true, 60501, 60060, 1},
		{"first live bar after warmup, downtrend -> short", true, false, false, true, 59000, 60000, -1},
		{"no warmup seen (backtest) -> none", false, false, false, true, 60501, 60060, 0},
		{"still in warmup -> none", true, true, false, true, 60501, 60060, 0},
		{"already primed -> none", true, false, true, true, 60501, 60060, 0},
		{"not flat -> none (don't stack)", true, false, false, false, 60501, 60060, 0},
		{"fast==slow -> none", true, false, false, true, 60000, 60000, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := primeDirection(c.sawWarmup, c.isWarmup, c.primed, c.flat, c.fast, c.slow)
			if got != c.want {
				t.Errorf("primeDirection(%v,%v,%v,%v,%.0f,%.0f) = %d, want %d",
					c.sawWarmup, c.isWarmup, c.primed, c.flat, c.fast, c.slow, got, c.want)
			}
		})
	}
}
