package guardian

// SMA is a simple rolling mean over a fixed window, used as the reference moving
// average for the MAState fact-alert.
type SMA struct {
	window int
	buf    []float64
	i, n   int
	sum    float64
}

// NewSMA creates an SMA with the given window (<=0 falls back to 1).
func NewSMA(window int) *SMA {
	if window <= 0 {
		window = 1
	}
	return &SMA{window: window, buf: make([]float64, window)}
}

// Add feeds one value and returns the current mean (0 until Ready).
func (s *SMA) Add(v float64) float64 {
	s.sum -= s.buf[s.i]
	s.buf[s.i] = v
	s.sum += v
	s.i = (s.i + 1) % s.window
	if s.n < s.window {
		s.n++
	}
	return s.Value()
}

// Ready reports whether the window has filled.
func (s *SMA) Ready() bool { return s.n >= s.window }

// Value returns the current mean, or 0 before the window fills.
func (s *SMA) Value() float64 {
	if !s.Ready() {
		return 0
	}
	return s.sum / float64(s.window)
}
