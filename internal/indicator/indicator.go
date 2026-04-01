// Package indicator provides wrappers around go-talib technical indicators.
// All functions operate on a slice of float64 values (typically Close prices)
// and return a slice of the same length, with leading NaN values where the
// indicator is not yet defined.
package indicator

import "github.com/markcheno/go-talib"

// SMA computes the Simple Moving Average over `period` bars.
// Returns a slice of length len(values); the first period-1 values are 0.
func SMA(values []float64, period int) []float64 {
	return talib.Sma(values, period)
}

// EMA computes the Exponential Moving Average over `period` bars.
func EMA(values []float64, period int) []float64 {
	return talib.Ema(values, period)
}

// RSI computes the Relative Strength Index over `period` bars.
// Returns values in [0, 100].
func RSI(values []float64, period int) []float64 {
	return talib.Rsi(values, period)
}

// MACDResult holds the three MACD output series.
type MACDResult struct {
	MACD      []float64 // MACD line
	Signal    []float64 // Signal line
	Histogram []float64 // Histogram (MACD - Signal)
}

// MACD computes the Moving Average Convergence Divergence indicator.
// Typical parameters: fast=12, slow=26, signal=9.
func MACD(values []float64, fast, slow, signal int) MACDResult {
	macd, sig, hist := talib.Macd(values, fast, slow, signal)
	return MACDResult{MACD: macd, Signal: sig, Histogram: hist}
}

// BBResult holds the three Bollinger Band output series.
type BBResult struct {
	Upper  []float64
	Middle []float64
	Lower  []float64
}

// BollingerBands computes Bollinger Bands with the given period and std-dev multiplier.
// Typical parameters: period=20, multiplier=2.0.
func BollingerBands(values []float64, period int, multiplier float64) BBResult {
	upper, mid, lower := talib.BBands(values, period, multiplier, multiplier, talib.SMA)
	return BBResult{Upper: upper, Middle: mid, Lower: lower}
}

// CrossOver returns true if series a crossed above series b at the last index.
// Requires at least 2 data points.
func CrossOver(a, b []float64) bool {
	n := len(a)
	if n < 2 || len(b) < 2 {
		return false
	}
	// Previous bar: a was below or equal; current bar: a is above
	return a[n-2] <= b[n-2] && a[n-1] > b[n-1]
}

// CrossUnder returns true if series a crossed below series b at the last index.
func CrossUnder(a, b []float64) bool {
	n := len(a)
	if n < 2 || len(b) < 2 {
		return false
	}
	return a[n-2] >= b[n-2] && a[n-1] < b[n-1]
}

// Last returns the last element of a slice, or 0 if empty.
func Last(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1]
}

// Prev returns the second-to-last element of a slice, or 0 if len < 2.
func Prev(s []float64) float64 {
	if len(s) < 2 {
		return 0
	}
	return s[len(s)-2]
}
