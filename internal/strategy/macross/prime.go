package macross

// primeDirection returns the position direction to establish on the first live
// bar after a warmup replay: +1 long, -1 short, 0 none.
//
// The engine replays historical bars to warm up indicators but suppresses order
// execution for them (so a strategy doesn't trade on stale signals). A downside:
// the crossover that established the CURRENT trend fired during warmup and was
// suppressed, so a purely cross-triggered strategy sits out the trend until the
// next cross — after a restart mid-trend it misses the whole move.
//
// Priming enters the position the current SMA state implies, but only:
//   - once (primed guards re-entry),
//   - after a warmup actually happened (sawWarmup — so backtests / no-warmup runs
//     are byte-for-byte unchanged),
//   - on a live bar (not during warmup),
//   - while flat (never stack onto an existing position).
func primeDirection(sawWarmup, isWarmup, primed, flat bool, fast, slow float64) int {
	if !sawWarmup || isWarmup || primed || !flat {
		return 0
	}
	switch {
	case fast > slow:
		return 1
	case fast < slow:
		return -1
	default:
		return 0
	}
}
