// Package guardian implements a protective stop + trailing-stop + monitoring
// layer for positions the user opens themselves. It predicts nothing and
// generates no entry signals; it enforces risk discipline and surfaces alerts.
package guardian

import "math"

// ATR is a rolling Average True Range over a fixed window (simple mean of true
// range, matching the ATR used elsewhere in the codebase). Not safe for
// concurrent use; the guardian drives it from a single engine goroutine.
type ATR struct {
	window int
	buf    []float64 // last `window` true-range values (ring)
	i      int       // next write index
	n      int       // number of samples seen (capped display via Ready)
	sum    float64   // running sum of the buffer contents
}

// NewATR creates an ATR with the given window. A window <= 0 falls back to 1.
func NewATR(window int) *ATR {
	if window <= 0 {
		window = 1
	}
	return &ATR{window: window, buf: make([]float64, window)}
}

// Update feeds one bar's high/low and the previous bar's close, returns the
// current ATR (0 until Ready).
func (a *ATR) Update(high, low, prevClose float64) float64 {
	tr := math.Max(high-low, math.Max(math.Abs(high-prevClose), math.Abs(low-prevClose)))
	a.sum -= a.buf[a.i]
	a.buf[a.i] = tr
	a.sum += tr
	a.i = (a.i + 1) % a.window
	if a.n < a.window {
		a.n++
	}
	return a.Value()
}

// Ready reports whether at least `window` samples have been seen.
func (a *ATR) Ready() bool { return a.n >= a.window }

// Value returns the current ATR, or 0 before the window fills.
func (a *ATR) Value() float64 {
	if !a.Ready() {
		return 0
	}
	return a.sum / float64(a.window)
}
