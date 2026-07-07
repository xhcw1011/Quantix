package xsfunding

// BuildTargets makes a dollar-neutral, equal-weight target book: each of the 2K
// positions gets (capital × grossFrac)/(2K) notional, positive for longs, negative
// for shorts.
func BuildTargets(longs, shorts []string, capital, grossFrac float64) []Target {
	n := len(longs) + len(shorts)
	if n == 0 {
		return nil
	}
	per := capital * grossFrac / float64(n)
	out := make([]Target, 0, n)
	for _, s := range longs {
		out = append(out, Target{Symbol: s, Notional: per})
	}
	for _, s := range shorts {
		out = append(out, Target{Symbol: s, Notional: -per})
	}
	return out
}
