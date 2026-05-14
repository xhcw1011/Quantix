package composite

import (
	"math"

	"github.com/Quantix/quantix/internal/alpha"
	"github.com/Quantix/quantix/internal/strategy"
)

// defaultStepSize is the fallback qty granularity (ETHUSDT futures = 0.001).
// Override via Config.StepSize for other symbols.
const defaultStepSize = 0.001

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
	step := cfg.StepSize
	if step <= 0 {
		step = defaultStepSize
	}
	raw := riskUSD / slDist
	return math.Floor(raw/step) * step
}
