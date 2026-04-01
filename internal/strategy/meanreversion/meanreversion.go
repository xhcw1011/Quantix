// Package meanreversion implements a Bollinger Band + RSI mean-reversion strategy.
//
// Entry : close < lower BB AND RSI < OversoldRSI → BUY all-in
// Exit  : close > middle BB (mean reversion) → SELL
//         close > upper BB AND RSI > OverboughtRSI → SELL (overheated)
package meanreversion

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// Config holds the tunable parameters for MeanReversion.
type Config struct {
	Symbol        string
	BBPeriod      int     // default 20
	BBStdDev      float64 // default 2.0
	RSIPeriod     int     // default 14
	OverboughtRSI float64 // default 70
	OversoldRSI   float64 // default 30
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.BBPeriod == 0 {
		out.BBPeriod = 20
	}
	if out.BBStdDev == 0 {
		out.BBStdDev = 2.0
	}
	if out.RSIPeriod == 0 {
		out.RSIPeriod = 14
	}
	if out.OverboughtRSI == 0 {
		out.OverboughtRSI = 70
	}
	if out.OversoldRSI == 0 {
		out.OversoldRSI = 30
	}
	return out
}

// MeanReversion is a Bollinger Band + RSI mean-reversion strategy.
type MeanReversion struct {
	cfg    Config
	closes []float64
}

// New creates a new MeanReversion strategy.
func New(cfg Config) *MeanReversion {
	return &MeanReversion{cfg: cfg.withDefaults()}
}

// Name implements strategy.Strategy.
func (m *MeanReversion) Name() string {
	return fmt.Sprintf("MeanReversion(BB%d,RSI%d)", m.cfg.BBPeriod, m.cfg.RSIPeriod)
}

// OnBar implements strategy.Strategy.
func (m *MeanReversion) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if bar.Symbol != m.cfg.Symbol {
		return
	}

	m.closes = append(m.closes, bar.Close)

	// Need enough bars for both BB and RSI
	minBars := m.cfg.BBPeriod
	if m.cfg.RSIPeriod+1 > minBars {
		minBars = m.cfg.RSIPeriod + 1
	}
	if len(m.closes) < minBars {
		return
	}

	bb := indicator.BollingerBands(m.closes, m.cfg.BBPeriod, m.cfg.BBStdDev)
	rsi := indicator.RSI(m.closes, m.cfg.RSIPeriod)

	upper := indicator.Last(bb.Upper)
	middle := indicator.Last(bb.Middle)
	lower := indicator.Last(bb.Lower)
	currentRSI := indicator.Last(rsi)
	price := bar.Close

	_, _, hasPosition := ctx.Portfolio.Position(bar.Symbol)

	switch {
	case !hasPosition && price < lower && currentRSI < m.cfg.OversoldRSI:
		ctx.Log.Info("mean reversion — BUY (oversold)",
			zap.String("symbol", bar.Symbol),
			zap.Float64("close", price),
			zap.Float64("lowerBB", lower),
			zap.Float64("rsi", currentRSI),
		)
		ctx.PlaceOrder(strategy.OrderRequest{
			Symbol: bar.Symbol,
			Side:   strategy.SideBuy,
			Type:   strategy.OrderMarket,
			Qty:    0, // all-in
		})

	case hasPosition && price > upper && currentRSI > m.cfg.OverboughtRSI:
		ctx.Log.Info("mean reversion — SELL (overbought)",
			zap.String("symbol", bar.Symbol),
			zap.Float64("close", price),
			zap.Float64("upperBB", upper),
			zap.Float64("rsi", currentRSI),
		)
		ctx.PlaceOrder(strategy.OrderRequest{
			Symbol: bar.Symbol,
			Side:   strategy.SideSell,
			Type:   strategy.OrderMarket,
			Qty:    0,
		})

	case hasPosition && price > middle:
		ctx.Log.Info("mean reversion — SELL (mean reversion to middle)",
			zap.String("symbol", bar.Symbol),
			zap.Float64("close", price),
			zap.Float64("middleBB", middle),
		)
		ctx.PlaceOrder(strategy.OrderRequest{
			Symbol: bar.Symbol,
			Side:   strategy.SideSell,
			Type:   strategy.OrderMarket,
			Qty:    0,
		})
	}
}

// OnFill implements strategy.Strategy.
func (m *MeanReversion) OnFill(_ *strategy.Context, fill strategy.Fill) {
	_ = fill
}

func init() {
	registry.Register("meanreversion", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := Config{}
		if v, ok := params["Symbol"].(string); ok {
			cfg.Symbol = v
		}
		if v, ok := params["BBPeriod"]; ok {
			cfg.BBPeriod = toInt(v)
		}
		if v, ok := params["BBStdDev"]; ok {
			cfg.BBStdDev = toFloat(v)
		}
		if v, ok := params["RSIPeriod"]; ok {
			cfg.RSIPeriod = toInt(v)
		}
		if v, ok := params["OverboughtRSI"]; ok {
			cfg.OverboughtRSI = toFloat(v)
		}
		if v, ok := params["OversoldRSI"]; ok {
			cfg.OversoldRSI = toFloat(v)
		}
		return New(cfg), nil
	})
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	case float32:
		return int(x)
	}
	return 0
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	}
	return 0
}
