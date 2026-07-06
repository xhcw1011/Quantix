package macross

// crossBuffered detects a fast/slow crossover that clears a buffer band around
// slow, filtering marginal "touch" crosses that cause whipsaw in chop.
//
// Returns +1 (golden — fast crossed decisively above slow*(1+buf)), -1 (death —
// fast crossed decisively below slow*(1-buf)), or 0 (none / cross too marginal).
// bufferPct is a fraction (0.0015 = 0.15%). bufferPct<=0 reduces to a raw cross.
//
// The prev bar must be on the near side of the band and the current bar past it,
// so a real trend reversal (fast pulls clear of slow) fires while a hairline
// touch (e.g. the 2026-07-05 fast 62739.46 vs slow 62740.04) is ignored.
func crossBuffered(fastPrev, slowPrev, fastNow, slowNow, bufferPct float64) int {
	upPrev, upNow := slowPrev*(1+bufferPct), slowNow*(1+bufferPct)
	dnPrev, dnNow := slowPrev*(1-bufferPct), slowNow*(1-bufferPct)
	if fastPrev <= upPrev && fastNow > upNow {
		return 1
	}
	if fastPrev >= dnPrev && fastNow < dnNow {
		return -1
	}
	return 0
}

// crossDir applies crossBuffered to the last two points of the fast/slow series.
// Returns 0 when there are fewer than two points.
func crossDir(fast, slow []float64, bufferPct float64) int {
	if len(fast) < 2 || len(slow) < 2 {
		return 0
	}
	return crossBuffered(fast[len(fast)-2], slow[len(slow)-2], fast[len(fast)-1], slow[len(slow)-1], bufferPct)
}
