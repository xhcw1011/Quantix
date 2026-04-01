// Package risk implements position sizing and risk management rules.
package risk

import "math"

// Kelly computes the full Kelly Criterion fraction:
//
//	f* = (winRate * avgWin - lossRate * avgLoss) / avgWin
//
// winRate is the probability of winning (0–1).
// avgWin is the average gain per winning trade (positive, e.g. 0.05 = 5%).
// avgLoss is the average loss per losing trade (positive, e.g. 0.03 = 3%).
//
// Returns a value in [0, 1], clamped to 0 when the edge is negative.
// Callers should typically use a fraction of the full Kelly (e.g. half-Kelly)
// to reduce variance: use the result multiplied by 0.5.
func Kelly(winRate, avgWin, avgLoss float64) float64 {
	if avgWin <= 0 || avgLoss <= 0 {
		return 0
	}
	lossRate := 1 - winRate
	f := (winRate*avgWin - lossRate*avgLoss) / avgWin
	return math.Max(0, f)
}

// HalfKelly returns the half-Kelly position fraction.
// This is the standard risk-conservative recommendation.
func HalfKelly(winRate, avgWin, avgLoss float64) float64 {
	return Kelly(winRate, avgWin, avgLoss) * 0.5
}

// PositionSize returns the position size in quote currency given:
//   - totalEquity: total portfolio value
//   - kellyFraction: from Kelly() or HalfKelly()
//   - cap: maximum fraction of equity per trade (e.g. 0.10 = 10%)
func PositionSize(totalEquity, kellyFraction, cap float64) float64 {
	fraction := math.Min(kellyFraction, cap)
	return totalEquity * fraction
}
