package aistrat_v4

import "math"

// zScore returns (current - SMA) / std using the last `lookback` bars
// (excluding the current bar from the mean/std window so the z score
// measures how far the current close is from its recent baseline).
//
// Returns 0 if there are fewer than lookback+1 bars or if std == 0.
func zScore(closes []float64, lookback int) float64 {
	n := len(closes)
	if n < lookback+1 || lookback < 2 {
		return 0
	}
	current := closes[n-1]
	window := closes[n-1-lookback : n-1] // exclude current
	mean := 0.0
	for _, v := range window {
		mean += v
	}
	mean /= float64(lookback)
	sumSq := 0.0
	for _, v := range window {
		d := v - mean
		sumSq += d * d
	}
	variance := sumSq / float64(lookback)
	std := math.Sqrt(variance)
	if std == 0 {
		return 0
	}
	return (current - mean) / std
}

// atr computes Average True Range over the last `period` bars from highs/lows/closes.
// Returns 0 if there are fewer than period+1 bars.
//
// True range for bar i: max(high - low, |high - prevClose|, |low - prevClose|)
// ATR = simple average of true ranges over period.
func atr(highs, lows, closes []float64, period int) float64 {
	n := len(closes)
	if n < period+1 || period < 1 {
		return 0
	}
	sum := 0.0
	for i := n - period; i < n; i++ {
		hl := highs[i] - lows[i]
		hc := math.Abs(highs[i] - closes[i-1])
		lc := math.Abs(lows[i] - closes[i-1])
		tr := math.Max(hl, math.Max(hc, lc))
		sum += tr
	}
	return sum / float64(period)
}
