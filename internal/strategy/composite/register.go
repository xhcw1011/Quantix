package composite

import (
	"github.com/Quantix/quantix/internal/alpha/baseline"
	"github.com/Quantix/quantix/internal/alpha/sentiment"
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

		alphas := buildAlphas(params, log)
		s := New(alphas, cfg)
		log.Info("composite strategy created",
			zap.String("symbol", cfg.Symbol),
			zap.Int("alphas", len(alphas)))
		return s, nil
	})
}

// buildAlphas constructs the alpha list from params. Each alpha is enabled
// via its `alpha_<name>` boolean flag. If no alphas are explicitly enabled
// AND the `alpha_explicit_empty` sentinel is not set, defaults to Breakout
// for backwards compatibility with prior tests + Phase 1 behavior.
func buildAlphas(params map[string]any, log *zap.Logger) []Alpha {
	alphas := []Alpha{}

	if v, _ := params["alpha_breakout"].(bool); v {
		alphas = append(alphas, baseline.NewBreakout())
		log.Info("alpha enabled", zap.String("name", "breakout"))
	}
	if v, _ := params["alpha_funding_extreme"].(bool); v {
		fetcher := sentiment.NewBinanceFundingFetcher()
		alphas = append(alphas, sentiment.NewFundingExtreme(fetcher, sentiment.FundingExtremeConfig{}))
		log.Info("alpha enabled", zap.String("name", "funding_extreme"))
	}
	if v, _ := params["alpha_cryptopanic"].(bool); v {
		fetcher := sentiment.NewCryptopanicFetcher()
		alphas = append(alphas, sentiment.NewCryptopanicNews(fetcher, sentiment.CryptopanicConfig{}))
		log.Info("alpha enabled", zap.String("name", "cryptopanic_news"))
	}
	if v, _ := params["alpha_basis_dispersion"].(bool); v {
		fetcher := sentiment.NewBinanceBasisFetcher()
		alphas = append(alphas, sentiment.NewBasisDispersion(fetcher, sentiment.BasisDispersionConfig{}))
		log.Info("alpha enabled", zap.String("name", "basis_dispersion"))
	}

	if len(alphas) == 0 {
		// Sentinel: explicit "no alphas" signal — fall through and let New() panic.
		// Without this sentinel, default to breakout for back-compat.
		if v, _ := params["alpha_explicit_empty"].(bool); v {
			return alphas
		}
		alphas = append(alphas, baseline.NewBreakout())
		log.Info("alpha defaulted to breakout (no flags set)")
	}
	return alphas
}

func stringParam(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}
