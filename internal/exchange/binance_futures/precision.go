package binance_futures

import (
	"math"
	"strconv"
	"strings"
)

// symbolFilter holds the LOT_SIZE / PRICE_FILTER precision for one symbol,
// parsed from Binance Futures exchangeInfo. A zero value (step/tick == 0) means
// "unknown" and callers fall back to a default format.
type symbolFilter struct {
	stepSize      float64 // LOT_SIZE stepSize, e.g. BTCUSDT 0.001
	tickSize      float64 // PRICE_FILTER tickSize, e.g. BTCUSDT 0.1
	qtyDecimals   int     // decimals implied by stepSize
	priceDecimals int     // decimals implied by tickSize
}

// stepDecimals counts significant decimal places in a Binance size string,
// ignoring trailing zeros: "0.00100000"->3, "0.10"->1, "1.0"->0, "1"->0.
func stepDecimals(s string) int {
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return 0
	}
	return len(strings.TrimRight(s[dot+1:], "0"))
}

// parseStep parses a Binance filter size string into (step, decimals). Returns
// (0,0) when unparseable or non-positive.
func parseStep(s string) (float64, int) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f <= 0 {
		return 0, 0
	}
	return f, stepDecimals(s)
}

// floorToStep floors v DOWN to the nearest multiple of step — used for order
// quantities so a sized order never exceeds available margin. The +1e-9 nudge
// keeps exact multiples that land just below from float error (2.116/0.001 =
// 2115.9999999998) from flooring one step short. step<=0 or v<=0 → v unchanged.
func floorToStep(v, step float64) float64 {
	if step <= 0 || v <= 0 {
		return v
	}
	return math.Floor(v/step+1e-9) * step
}

// roundToStep rounds v to the NEAREST multiple of step — used for prices, where
// the closest valid tick is wanted. step<=0 or v<=0 → v unchanged.
func roundToStep(v, step float64) float64 {
	if step <= 0 || v <= 0 {
		return v
	}
	return math.Round(v/step) * step
}

// qtyStr formats a quantity to the symbol's stepSize precision (floored). When
// the step is unknown (0), falls back to 3 decimals (Binance ETHUSDT/BTCUSDT
// step is 0.001) so behaviour degrades to the pre-fix default rather than 8dp.
func (f symbolFilter) qtyStr(qty float64) string {
	if f.stepSize <= 0 {
		return strconv.FormatFloat(qty, 'f', 3, 64)
	}
	return strconv.FormatFloat(floorToStep(qty, f.stepSize), 'f', f.qtyDecimals, 64)
}

// priceStr formats a price to the symbol's tickSize precision (rounded). When
// the tick is unknown (0), falls back to 2 decimals.
func (f symbolFilter) priceStr(price float64) string {
	if f.tickSize <= 0 {
		return strconv.FormatFloat(price, 'f', 2, 64)
	}
	return strconv.FormatFloat(roundToStep(price, f.tickSize), 'f', f.priceDecimals, 64)
}
