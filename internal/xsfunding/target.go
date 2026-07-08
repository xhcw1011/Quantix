package xsfunding

// BuildTargetsRP makes a dollar-neutral target book with INVERSE-VOLATILITY sizing and
// a per-coin cap — the risk-managed variant. Within each leg, each coin's notional is
// proportional to 1/vol (so hotter/riskier coins get smaller shorts), the leg summing
// to (capital×grossFrac)/2. Coins with vol ≤ 0 get the leg's average weight; if a whole
// leg has no vol data it degenerates to equal weight. Each |notional| is capped at
// maxPerFrac×gross (0 = no cap), the excess redistributed to uncapped coins in the leg.
// Legs are then scaled to equal totals so the book stays dollar-neutral.
func BuildTargetsRP(longs, shorts []string, capital, grossFrac float64, vol map[string]float64, maxPerFrac float64) []Target {
	if len(longs)+len(shorts) == 0 {
		return nil
	}
	gross := capital * grossFrac
	legGross := gross / 2
	cap := 0.0
	if maxPerFrac > 0 {
		cap = maxPerFrac * gross
	}

	legNotionals := func(syms []string) map[string]float64 {
		if len(syms) == 0 {
			return nil
		}
		inv := make([]float64, len(syms))
		var sumPos float64
		var cntPos int
		for i, s := range syms {
			if v := vol[s]; v > 0 {
				inv[i] = 1.0 / v
				sumPos += inv[i]
				cntPos++
			}
		}
		avg := 1.0
		if cntPos > 0 {
			avg = sumPos / float64(cntPos)
		}
		var sumInv float64
		for i := range inv {
			if inv[i] == 0 {
				inv[i] = avg
			}
			sumInv += inv[i]
		}
		notl := make(map[string]float64, len(syms))
		for i, s := range syms {
			notl[s] = legGross * inv[i] / sumInv
		}
		if cap > 0 {
			capRedistribute(notl, cap)
		}
		return notl
	}

	L := legNotionals(longs)
	S := legNotionals(shorts)
	var lt, st float64
	for _, v := range L {
		lt += v
	}
	for _, v := range S {
		st += v
	}
	if lt > 0 && st > 0 { // keep dollar-neutral: scale the larger leg down to the smaller
		if lt > st {
			for s := range L {
				L[s] *= st / lt
			}
		} else if st > lt {
			for s := range S {
				S[s] *= lt / st
			}
		}
	}

	out := make([]Target, 0, len(longs)+len(shorts))
	for _, s := range longs {
		out = append(out, Target{Symbol: s, Notional: L[s]})
	}
	for _, s := range shorts {
		out = append(out, Target{Symbol: s, Notional: -S[s]})
	}
	return out
}

// capRedistribute clamps each notional to cap and spreads the excess proportionally
// over the uncapped coins in the leg, iterating until stable (or all coins are capped).
func capRedistribute(notl map[string]float64, cap float64) {
	for iter := 0; iter < 20; iter++ {
		var excess, uncapped float64
		over := false
		for s, v := range notl {
			if v > cap+1e-9 {
				excess += v - cap
				notl[s] = cap
				over = true
			}
		}
		if !over {
			return
		}
		for _, v := range notl {
			if v < cap-1e-9 {
				uncapped += v
			}
		}
		if uncapped <= 0 {
			return // everything is at the cap; leg is left under gross (conservative)
		}
		for s, v := range notl {
			if v < cap-1e-9 {
				notl[s] = v + excess*v/uncapped
			}
		}
	}
}

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
