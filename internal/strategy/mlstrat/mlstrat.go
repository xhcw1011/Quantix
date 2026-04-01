// Package mlstrat implements a trading strategy driven by a logistic-regression model.
// Model weights are loaded from a JSON file produced by scripts/ml/train.py.
package mlstrat

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
	"github.com/Quantix/quantix/internal/ml"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// Config holds the parameters for the ML strategy.
type Config struct {
	Symbol        string
	ModelPath     string  // path to JSON weights file
	BuyThreshold  float64 // buy when P(up) >= threshold, default 0.6
	SellThreshold float64 // sell when P(up) <= threshold, default 0.4
	// Indicator parameters for feature computation
	RSIPeriod  int // default 14
	MACDFast   int // default 12
	MACDSlow   int // default 26
	MACDSignal int // default 9
	BBPeriod   int // default 20
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.BuyThreshold == 0 {
		out.BuyThreshold = 0.6
	}
	if out.SellThreshold == 0 {
		out.SellThreshold = 0.4
	}
	if out.RSIPeriod == 0 {
		out.RSIPeriod = 14
	}
	if out.MACDFast == 0 {
		out.MACDFast = 12
	}
	if out.MACDSlow == 0 {
		out.MACDSlow = 26
	}
	if out.MACDSignal == 0 {
		out.MACDSignal = 9
	}
	if out.BBPeriod == 0 {
		out.BBPeriod = 20
	}
	return out
}

// MLStrategy drives orders using a pre-trained logistic-regression model.
type MLStrategy struct {
	cfg     Config
	model   *ml.Model
	closes  []float64
	volumes []float64
}

// New creates an MLStrategy. Returns error if the model file cannot be loaded.
func New(cfg Config) (*MLStrategy, error) {
	cfg = cfg.withDefaults()
	m, err := ml.LoadModel(cfg.ModelPath)
	if err != nil {
		return nil, fmt.Errorf("mlstrat: load model: %w", err)
	}
	return &MLStrategy{cfg: cfg, model: m}, nil
}

// Name implements strategy.Strategy.
func (s *MLStrategy) Name() string {
	return "MLStrategy"
}

// OnBar implements strategy.Strategy.
func (s *MLStrategy) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if bar.Symbol != s.cfg.Symbol {
		return
	}
	s.closes = append(s.closes, bar.Close)
	s.volumes = append(s.volumes, bar.Volume)

	// Need enough bars for the slowest indicator
	minBars := s.cfg.MACDSlow + s.cfg.MACDSignal + 1
	if s.cfg.BBPeriod > minBars {
		minBars = s.cfg.BBPeriod
	}
	if len(s.closes) < minBars+20 { // +20 for rolling vol_sma20
		return
	}

	features, err := s.computeFeatures()
	if err != nil {
		ctx.Log.Warn("mlstrat: feature computation failed", zap.Error(err))
		return
	}

	p, err := s.model.Predict(features)
	if err != nil {
		ctx.Log.Warn("mlstrat: prediction failed", zap.Error(err))
		return
	}

	_, _, hasPosition := ctx.Portfolio.Position(bar.Symbol)

	switch {
	case !hasPosition && p >= s.cfg.BuyThreshold:
		ctx.Log.Info("ML signal — BUY",
			zap.String("symbol", bar.Symbol),
			zap.Float64("prob", p),
			zap.Float64("close", bar.Close),
		)
		ctx.PlaceOrder(strategy.OrderRequest{
			Symbol: bar.Symbol,
			Side:   strategy.SideBuy,
			Type:   strategy.OrderMarket,
			Qty:    0, // all-in
		})

	case hasPosition && p <= s.cfg.SellThreshold:
		ctx.Log.Info("ML signal — SELL",
			zap.String("symbol", bar.Symbol),
			zap.Float64("prob", p),
			zap.Float64("close", bar.Close),
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
func (s *MLStrategy) OnFill(_ *strategy.Context, _ strategy.Fill) {}

// computeFeatures calculates the same feature set used during training.
func (s *MLStrategy) computeFeatures() (map[string]float64, error) {
	closes := s.closes
	vols := s.volumes
	n := len(closes)

	rsiSeries := indicator.RSI(closes, s.cfg.RSIPeriod)
	macdRes := indicator.MACD(closes, s.cfg.MACDFast, s.cfg.MACDSlow, s.cfg.MACDSignal)
	bbRes := indicator.BollingerBands(closes, s.cfg.BBPeriod, 2.0)

	rsi := indicator.Last(rsiSeries)
	macdHist := indicator.Last(macdRes.Histogram)
	upper := indicator.Last(bbRes.Upper)
	lower := indicator.Last(bbRes.Lower)

	var bbPos float64
	if upper-lower > 0 {
		bbPos = (closes[n-1] - lower) / (upper - lower)
	}

	// Rolling returns
	var ret5, ret20 float64
	if n >= 6 {
		ret5 = (closes[n-1] - closes[n-6]) / closes[n-6]
	}
	if n >= 21 {
		ret20 = (closes[n-1] - closes[n-21]) / closes[n-21]
	}

	// 20-bar volatility (std dev of daily returns)
	var vol20 float64
	if n >= 21 {
		rets := make([]float64, 20)
		for i := 0; i < 20; i++ {
			rets[i] = (closes[n-20+i] - closes[n-21+i]) / closes[n-21+i]
		}
		mean := 0.0
		for _, r := range rets {
			mean += r
		}
		mean /= 20
		var variance float64
		for _, r := range rets {
			d := r - mean
			variance += d * d
		}
		vol20 = variance / 20
	}

	// Volume ratio = vol / vol_sma20
	var volRatio float64
	if n >= 20 {
		volSum := 0.0
		for i := n - 20; i < n; i++ {
			volSum += vols[i]
		}
		volSMA20 := volSum / 20
		if volSMA20 > 0 {
			volRatio = vols[n-1] / volSMA20
		}
	}

	return map[string]float64{
		"rsi":        rsi,
		"macd_hist":  macdHist,
		"bb_pos":     bbPos,
		"ret_5":      ret5,
		"ret_20":     ret20,
		"vol_20":     vol20,
		"vol_ratio":  volRatio,
	}, nil
}

func init() {
	registry.Register("ml", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := Config{}
		if v, ok := params["Symbol"].(string); ok {
			cfg.Symbol = v
		}
		if v, ok := params["ModelPath"].(string); ok {
			cfg.ModelPath = v
		}
		if v, ok := params["BuyThreshold"]; ok {
			cfg.BuyThreshold = toFloat(v)
		}
		if v, ok := params["SellThreshold"]; ok {
			cfg.SellThreshold = toFloat(v)
		}
		if v, ok := params["RSIPeriod"]; ok {
			cfg.RSIPeriod = toInt(v)
		}
		if v, ok := params["MACDFast"]; ok {
			cfg.MACDFast = toInt(v)
		}
		if v, ok := params["MACDSlow"]; ok {
			cfg.MACDSlow = toInt(v)
		}
		if v, ok := params["MACDSignal"]; ok {
			cfg.MACDSignal = toInt(v)
		}
		if v, ok := params["BBPeriod"]; ok {
			cfg.BBPeriod = toInt(v)
		}
		return New(cfg)
	})
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	}
	return 0
}
