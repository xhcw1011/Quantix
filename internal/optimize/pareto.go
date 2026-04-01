package optimize

// NonDominated returns the Pareto-optimal subset of results.
// Two objectives: maximize SharpeRatio, minimize MaxDrawdown.
// Solution a dominates b if: a.Sharpe >= b.Sharpe AND a.MaxDD <= b.MaxDD
// with at least one strict inequality.
func NonDominated(results []RunResult) []RunResult {
	n := len(results)
	dominated := make([]bool, n)

	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			if dominates(results[j], results[i]) {
				dominated[i] = true
				break
			}
		}
	}

	front := make([]RunResult, 0, n)
	for i, r := range results {
		if !dominated[i] {
			front = append(front, r)
		}
	}
	return front
}

// dominates returns true if a dominates b in the two-objective space.
func dominates(a, b RunResult) bool {
	aSharpe := a.ISReport.SharpeRatio
	bSharpe := b.ISReport.SharpeRatio
	aDD := a.ISReport.MaxDrawdown
	bDD := b.ISReport.MaxDrawdown

	// a >= b on both objectives
	if aSharpe < bSharpe || aDD > bDD {
		return false
	}
	// at least one strict improvement
	return aSharpe > bSharpe || aDD < bDD
}
