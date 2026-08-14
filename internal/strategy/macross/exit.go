package macross

// Pure decision functions for asymmetric exit management (see Config.AsymmetricExit
// in macross.go for the full rationale). Kept separate from the position-state
// bookkeeping in macross.go so the actual decision logic can be tested with plain
// numbers instead of synthetic kline series.

// updateLossStreak returns the new consecutive-bar count of a confirmed adverse
// move: floatingPnLPct <= -triggerPct extends the streak, anything else (including
// recovery back above the trigger) resets it to zero.
func updateLossStreak(prevStreak int, floatingPnLPct, triggerPct float64) int {
	if floatingPnLPct <= -triggerPct {
		return prevStreak + 1
	}
	return 0
}

// shouldReduce reports whether the confirmed-adverse-move streak has reached
// confirmBars and the one-time reduce hasn't already fired for this position.
func shouldReduce(lossStreak, confirmBars int, alreadyReduced bool) bool {
	if alreadyReduced {
		return false
	}
	return lossStreak >= confirmBars
}

// shouldTrailClose reports whether a large-winner trailing exit should fire:
// the position's peak floating profit must have reached activatePct, and the
// current floating profit must have retraced by at least givebackFrac of that
// peak. Positions that never reach activatePct are never touched, regardless
// of how much of their (smaller) profit they give back.
func shouldTrailClose(peakPct, currentPct, activatePct, givebackFrac float64) bool {
	if peakPct < activatePct {
		return false
	}
	return currentPct <= peakPct*(1-givebackFrac)
}
