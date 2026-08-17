package macross

// cooldownActive reports whether a new entry should be blocked because too
// few bars have passed since the position last fully closed. barsSinceClose
// is meaningless (and ignored) until everClosed is true — the very first
// entry of a run has nothing to cool down from, so it's never blocked.
func cooldownActive(everClosed bool, barsSinceClose, cooldownBars int) bool {
	if cooldownBars <= 0 || !everClosed {
		return false
	}
	return barsSinceClose < cooldownBars
}
