package alpha

import (
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
)

// BuildFeatures computes the Features snapshot from a chronological slice of
// bars. The latest bar (bars[len-1]) is the "current" bar. Returns a zero-ATR
// Features when bars is too short to compute indicators reliably.
func BuildFeatures(symbol string, bars []exchange.Kline) Features {
	if len(bars) < 20 {
		return Features{}
	}
	last := bars[len(bars)-1]
	closes := make([]float64, len(bars))
	highs := make([]float64, len(bars))
	lows := make([]float64, len(bars))
	for i, b := range bars {
		closes[i] = b.Close
		highs[i] = b.High
		lows[i] = b.Low
	}
	bb := indicator.BollingerBands(closes, 20, 2.0)
	rsi := indicator.RSI(closes, 14)
	atr := indicator.ATR(highs, lows, closes, 14)

	hi10, lo10 := closes[len(closes)-1], closes[len(closes)-1]
	start := len(closes) - 11
	if start < 0 {
		start = 0
	}
	for i := start; i < len(closes)-1; i++ {
		if highs[i] > hi10 {
			hi10 = highs[i]
		}
		if lows[i] < lo10 {
			lo10 = lows[i]
		}
	}

	return Features{
		Now:      last.OpenTime,
		Symbol:   symbol,
		Close:    last.Close,
		High:     last.High,
		Low:      last.Low,
		ATR:      lastVal(atr),
		High10:   hi10,
		Low10:    lo10,
		BBUpper:  lastVal(bb.Upper),
		BBMiddle: lastVal(bb.Middle),
		BBLower:  lastVal(bb.Lower),
		RSI:      lastVal(rsi),
	}
}

func lastVal(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1]
}
