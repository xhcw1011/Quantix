package aistrat

import "math"

// updateTrendScore advances the signed 5m trend-accumulation score by one primary
// bar. The bar body strength (body/atr) is clamped to ±perBarCap so one spike bar
// can't dominate; the running score decays by `decay` each bar and is clamped to
// ±scoreMax. In chop, up/down bodies cancel toward 0; in a sustained trend the
// score accumulates one-directionally. atr<=0 (warmup) contributes no delta.
func updateTrendScore(prev, body, atr, decay, perBarCap, scoreMax float64) float64 {
	delta := 0.0
	if atr > 0 {
		delta = body / atr
		if delta > perBarCap {
			delta = perBarCap
		} else if delta < -perBarCap {
			delta = -perBarCap
		}
	}
	s := prev*decay + delta
	if s > scoreMax {
		s = scoreMax
	} else if s < -scoreMax {
		s = -scoreMax
	}
	return s
}

// trendEntryDir returns the direction of a trend-following entry justified by the
// score plus same-direction higher-timeframe confirmation, or 0 for none.
// threshold<=0 disables trend entry. Long fires when score>=+threshold AND
// htfDir==+1; short when score<=-threshold AND htfDir==-1.
func trendEntryDir(trendScore, threshold float64, htfDir int) int {
	if threshold <= 0 {
		return 0
	}
	if trendScore >= threshold && htfDir == 1 {
		return 1
	}
	if trendScore <= -threshold && htfDir == -1 {
		return -1
	}
	return 0
}

// trendAlignPenalty scales a fade entry's confidence down when it is counter to the
// accumulated trend, proportional to |trendScore|, reaching full (conf→0) at
// fullPenaltyScore. With-trend or flat confidence is unchanged. fullPenaltyScore<=0
// disables it. sideSign is +1 for a long entry, -1 for a short entry.
func trendAlignPenalty(rawConf float64, sideSign int, trendScore, fullPenaltyScore float64) float64 {
	if fullPenaltyScore <= 0 {
		return rawConf
	}
	counter := (sideSign > 0 && trendScore < 0) || (sideSign < 0 && trendScore > 0)
	if !counter {
		return rawConf
	}
	factor := 1.0 - math.Abs(trendScore)/fullPenaltyScore
	if factor < 0 {
		factor = 0
	}
	return rawConf * factor
}
