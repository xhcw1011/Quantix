// Package spottrend registers a spot long-only trend-following strategy. It reuses
// the macross dual-SMA crossover with EnableShort forced false: golden cross → buy
// (into the base asset), death cross → sell (back to cash). No shorting, no
// leverage — safe for spot accounts.
package spottrend

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/macross"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

func init() {
	registry.Register("spottrend", func(params map[string]any, _ *zap.Logger) (strategy.Strategy, error) {
		cfg := macross.Config{FastPeriod: 10, SlowPeriod: 30}
		if v, ok := params["Symbol"].(string); ok {
			cfg.Symbol = v
		}
		if v, ok := params["FastPeriod"]; ok {
			cfg.FastPeriod = toInt(v)
		}
		if v, ok := params["SlowPeriod"]; ok {
			cfg.SlowPeriod = toInt(v)
		}
		if v, ok := params["StopLossPct"]; ok {
			cfg.StopLossPct = toFloat(v)
		}
		if v, ok := params["TakeProfitPct"]; ok {
			cfg.TakeProfitPct = toFloat(v)
		}
		if v, ok := params["TrendFilterN"]; ok {
			cfg.TrendFilterN = toInt(v)
		}
		if v, ok := params["TrendFilterMin"]; ok {
			cfg.TrendFilterMin = toFloat(v)
		}
		cfg.EnableShort = false // spot: long-only, never short
		if cfg.Symbol == "" {
			return nil, fmt.Errorf("spottrend: Symbol is required")
		}
		if cfg.FastPeriod <= 0 || cfg.SlowPeriod <= 0 || cfg.FastPeriod >= cfg.SlowPeriod {
			return nil, fmt.Errorf("spottrend: need 0 < FastPeriod < SlowPeriod (got %d, %d)", cfg.FastPeriod, cfg.SlowPeriod)
		}
		return macross.New(cfg), nil
	})
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}
