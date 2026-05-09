package composite

import (
	"github.com/Quantix/quantix/internal/alpha/baseline"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
	"go.uber.org/zap"
)

func init() {
	registry.Register("composite", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := Config{Symbol: stringParam(params, "Symbol", "ETHUSDT")}
		if v, ok := params["RiskPerTrade"].(float64); ok {
			cfg.RiskPerTrade = v
		}
		if v, ok := params["SLATR"].(float64); ok {
			cfg.SLATR = v
		}
		if v, ok := params["MinSignalScore"].(float64); ok {
			cfg.MinSignalScore = v
		}
		if v, ok := params["WarmupBars"].(float64); ok {
			cfg.WarmupBars = int(v)
		}

		alphas := []Alpha{baseline.NewBreakout()}
		s := New(alphas, cfg)
		log.Info("composite strategy created",
			zap.String("symbol", cfg.Symbol),
			zap.Int("alphas", len(alphas)))
		return s, nil
	})
}

func stringParam(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}
