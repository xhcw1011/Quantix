package alpha

import "time"

// Signal is an alpha's output for a single bar.
// Direction: -1 (short), 0 (no signal), +1 (long).
// Strength: confidence in [0, 1]. Used by combiner.
// TargetR: take-profit target in R-multiples (0 = use default).
// Reason: human-readable explanation for logging/analysis.
type Signal struct {
	Direction int
	Strength  float64
	TargetR   float64
	Reason    string
}

// Features is the precomputed snapshot an alpha sees on each bar.
// Computed by the composite strategy from raw bars + indicator package
// once per bar, then handed to every registered alpha.
type Features struct {
	Now      time.Time
	Symbol   string
	Close    float64
	High     float64
	Low      float64
	ATR      float64
	High10   float64 // 10-bar rolling high (exclusive of current bar)
	Low10    float64 // 10-bar rolling low
	BBUpper  float64
	BBMiddle float64
	BBLower  float64
	RSI      float64
	LastBars []float64 // last N closes (chronological), N implementation-defined
}

// Alpha is the interface every signal source implements.
// Predict is called on each bar with the current Features snapshot.
// Implementations MUST be deterministic given identical Features
// (no clock reads, no random) so that backtest=live invariant holds.
type Alpha interface {
	Name() string
	Predict(f Features) Signal
}
