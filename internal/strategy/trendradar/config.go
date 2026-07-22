package trendradar

import (
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

func init() { registry.Register("trendradar", Factory) }

// Factory builds a trend radar from a params map (registry FactoryFn).
// The engine must be started with both intervals in req.Intervals (e.g. 15m,4h)
// so the radar receives both the signal and confirmation bars.
func Factory(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
	return New(
		strOr(params, "Symbol", "BTCUSDT"),
		strOr(params, "SignalInterval", "15m"),
		strOr(params, "ConfirmInterval", "4h"),
		floatOr(params, "StrengthMult", 1.0),
		log,
	), nil
}

func strOr(p map[string]any, k, def string) string {
	if v, ok := p[k]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return def
}

func floatOr(p map[string]any, k string, def float64) float64 {
	if v, ok := p[k]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		}
	}
	return def
}
