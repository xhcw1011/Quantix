package aistrat_v4

import "math"

// shouldExit decides whether to close the open position on the current bar.
// Returns (close, reason). close == false means hold; reason is "" then.
// Reasons: "tp" (z returned to 0), "sl" (|z| >= StopZScore), "time" (held too long).
//
// highs, lows are accepted for signature symmetry with shouldEnter; currently
// unused but reserved for future use (e.g., intra-bar SL trigger in v4.1).
//
//nolint:unparam
func shouldExit(closes, highs, lows []float64, cfg Config, pos *positionState, currentBar int) (close bool, reason string) {
	_ = highs
	_ = lows
	if pos == nil {
		return false, ""
	}
	z := zScore(closes, cfg.Lookback)
	// TP: z returned to / past mean
	if pos.Side == "SHORT" && z <= 0 {
		return true, "tp"
	}
	if pos.Side == "LONG" && z >= 0 {
		return true, "tp"
	}
	// SL: |z| crosses StopZScore
	if math.Abs(z) >= cfg.StopZScore {
		return true, "sl"
	}
	// Time stop: held >= TimeStopBars
	if currentBar-pos.EntryBar >= cfg.TimeStopBars {
		return true, "time"
	}
	return false, ""
}
