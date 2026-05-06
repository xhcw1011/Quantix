// Package aistrat_v4 implements a single-shot z-score mean-reversion strategy.
//
// Edge thesis: ETH 5m bars exhibit statistical reversion when |z| >= 2.5
// from SMA(Lookback). Entry on extreme z, exit on z=0 (TP) / |z|>=3.5 (SL) /
// time-stop after TimeStopBars bars. No tick events, no grid layers, no
// regime detection — see docs/superpowers/specs/2026-05-06-aistrat-v4-zscore-fade-design.md.
package aistrat_v4

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// Config holds tunable parameters. Defaults are starting points; real values
// come from grid-search backtest over historical data.
type Config struct {
	Symbol       string
	Lookback     int
	EntryZScore  float64
	StopZScore   float64
	TimeStopBars int
	CooldownBars int
	MinATRPct    float64
	RiskPerTrade float64
	Leverage     float64
}

// DefaultConfig returns the starting parameter values from the design spec.
func DefaultConfig() Config {
	return Config{
		Lookback:     20,
		EntryZScore:  2.5,
		StopZScore:   3.5,
		TimeStopBars: 12,
		CooldownBars: 3,
		MinATRPct:    0.003,
		RiskPerTrade: 0.005,
		Leverage:     2,
	}
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	}
	return 0
}

func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	}
	return 0
}

func init() {
	registry.Register("ai_v4", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := DefaultConfig()
		if v, ok := params["Symbol"].(string); ok {
			cfg.Symbol = v
		}
		if cfg.Symbol == "" {
			return nil, fmt.Errorf("ai_v4: Symbol required")
		}
		if v, ok := params["Lookback"]; ok {
			cfg.Lookback = toInt(v)
		}
		if v, ok := params["EntryZScore"]; ok {
			cfg.EntryZScore = toFloat(v)
		}
		if v, ok := params["StopZScore"]; ok {
			cfg.StopZScore = toFloat(v)
		}
		if v, ok := params["TimeStopBars"]; ok {
			cfg.TimeStopBars = toInt(v)
		}
		if v, ok := params["CooldownBars"]; ok {
			cfg.CooldownBars = toInt(v)
		}
		if v, ok := params["MinATRPct"]; ok {
			cfg.MinATRPct = toFloat(v)
		}
		if v, ok := params["RiskPerTrade"]; ok {
			cfg.RiskPerTrade = toFloat(v)
		}
		if v, ok := params["Leverage"]; ok {
			cfg.Leverage = toFloat(v)
		}
		return New(cfg, log), nil
	})
}
