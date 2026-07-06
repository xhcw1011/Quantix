package macross

import "testing"

func TestCrossBuffered(t *testing.T) {
	cases := []struct {
		name                               string
		fastPrev, slowPrev, fastNow, slowNow, buf float64
		want                               int
	}{
		// buffer 0 == raw cross behaviour
		{"raw golden (buf 0)", 99, 100, 101, 100, 0, 1},
		{"raw death (buf 0)", 101, 100, 99, 100, 0, -1},
		{"no cross", 101, 100, 102, 100, 0, 0},
		// 07-05 real numbers: fast 62739.46 vs slow 62740.04 — a 0.0009% touch.
		// With a 0.15% buffer this marginal death cross must be FILTERED (0).
		{"07-05 marginal death filtered", 62740.0, 62740.0, 62739.46, 62740.04, 0.0015, 0},
		// genuine death: fast decisively below the lower band → -1
		{"genuine death past buffer", 62800, 62740, 62550, 62740, 0.0015, -1},
		// genuine golden: fast decisively above the upper band → +1
		{"genuine golden past buffer", 62680, 62740, 62900, 62740, 0.0015, 1},
		// fast dips below but stays within buffer band → not a cross
		{"death within band ignored", 62740, 62740, 62700, 62740, 0.0015, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := crossBuffered(c.fastPrev, c.slowPrev, c.fastNow, c.slowNow, c.buf)
			if got != c.want {
				t.Errorf("crossBuffered(%.2f,%.2f,%.2f,%.2f,%.4f) = %d, want %d",
					c.fastPrev, c.slowPrev, c.fastNow, c.slowNow, c.buf, got, c.want)
			}
		})
	}
}

func TestCrossDir(t *testing.T) {
	// too few points → 0
	if got := crossDir([]float64{1}, []float64{1}, 0.0015); got != 0 {
		t.Errorf("crossDir short series = %d, want 0", got)
	}
	// uses last two points: fast crosses decisively above → golden
	if got := crossDir([]float64{100, 102}, []float64{101, 101}, 0.001); got != 1 {
		t.Errorf("crossDir golden = %d, want 1", got)
	}
}
