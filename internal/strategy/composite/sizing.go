package composite

import (
	"math"

	"github.com/Quantix/quantix/internal/alpha"
	"github.com/Quantix/quantix/internal/strategy"
)

// stepSize is the qty granularity (ETHUSDT futures = 0.001).
// Symbol-specific metadata is introduced in Phase 5.
const stepSize = 0.001

// positionSize returns the quantity for a new position based on equity at
// risk and ATR-based stop distance. Returns 0 if any guard trips.
func positionSize(ctx *strategy.Context, f alpha.Features, cfg Config) float64 {
	if f.ATR <= 0 {
		return 0
	}
	equity := ctx.Portfolio.Cash() // v1: cash-only equity (no open positions)
	if equity <= 0 {
		return 0
	}
	riskUSD := equity * cfg.RiskPerTrade
	slDist := f.ATR * cfg.SLATR
	if slDist <= 0 {
		return 0
	}
	raw := riskUSD / slDist
	return math.Floor(raw/stepSize) * stepSize
}
