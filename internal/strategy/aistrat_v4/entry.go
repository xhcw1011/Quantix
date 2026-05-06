package aistrat_v4

import "math"

// positionState is the internal record of an open position.
type positionState struct {
	Side         string // "LONG" or "SHORT"
	EntryPrice   float64
	EntryBar     int
	Qty          float64
	EntryZScore  float64
	StopLossPx   float64
	TakeProfitPx float64
	OrderID      string
}

// cooldown tracks per-side cooldown windows (last close bar index for LONG and SHORT).
type cooldown struct {
	LastLongCloseBar  int
	LastShortCloseBar int
}

type shouldEnterOption func(*shouldEnterCtx)

type shouldEnterCtx struct {
	cd cooldown
}

func withCooldown(cd cooldown) shouldEnterOption {
	return func(c *shouldEnterCtx) { c.cd = cd }
}

// shouldEnter decides whether to open a new position on the current bar.
// Returns (side, reason). side == "" means do not enter.
func shouldEnter(closes, highs, lows []float64, cfg Config, pos *positionState, currentBar int, opts ...shouldEnterOption) (side, reason string) {
	if pos != nil {
		return "", "position_exists"
	}

	c := shouldEnterCtx{}
	for _, opt := range opts {
		opt(&c)
	}

	z := zScore(closes, cfg.Lookback)
	if math.Abs(z) < cfg.EntryZScore {
		return "", "z_below_threshold"
	}

	a := atr(highs, lows, closes, 14)
	if a == 0 {
		return "", "atr_unavailable"
	}
	currentPrice := closes[len(closes)-1]
	if a/currentPrice < cfg.MinATRPct {
		return "", "atr_floor"
	}

	intent := "SHORT"
	if z < 0 {
		intent = "LONG"
	}

	if cfg.CooldownBars > 0 {
		var lastClose int
		if intent == "LONG" {
			lastClose = c.cd.LastLongCloseBar
		} else {
			lastClose = c.cd.LastShortCloseBar
		}
		if lastClose > 0 && currentBar-lastClose < cfg.CooldownBars {
			return "", "cooldown"
		}
	}

	return intent, "z_signal"
}

// calcQty returns the order quantity given equity, risk parameters, and price levels.
//
// equity         account equity (quote currency)
// riskPct        fraction of equity at risk per trade (e.g., 0.005)
// slDistance     absolute distance from entry to stop (quote price)
// price          current price (for max-position cap)
// maxPosPct      max position size as fraction of equity
// leverage       exchange leverage multiplier
//
// Returns qty = min(risk-based, max-position cap). If slDistance == 0,
// falls back to max-position cap.
func calcQty(equity, riskPct, slDistance, price, maxPosPct, leverage float64) float64 {
	if equity <= 0 || price <= 0 {
		return 0
	}
	maxCap := equity * maxPosPct * leverage / price
	if slDistance <= 0 {
		return maxCap
	}
	riskBased := equity * riskPct / slDistance
	if riskBased > maxCap {
		return maxCap
	}
	return riskBased
}
