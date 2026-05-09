// Package composite is a multi-alpha trading strategy. Each alpha
// produces a Signal independently from a shared Features snapshot;
// the strategy picks the strongest signal and turns it into orders.
package composite

import (
	"github.com/Quantix/quantix/internal/alpha"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// Alpha is re-exported so users don't need to import internal/alpha when
// constructing a Composite.
type Alpha = alpha.Alpha

// Config holds runtime parameters of the composite strategy.
type Config struct {
	Symbol         string
	RiskPerTrade   float64 // fraction of equity at risk per trade (default 0.005)
	SLATR          float64 // SL distance in ATR multiples (default 1.5)
	MinSignalScore float64 // skip signals below this strength (default 0.3)
	WarmupBars     int     // bars to accumulate before first prediction (default 60)
}

func (c Config) withDefaults() Config {
	if c.RiskPerTrade == 0 {
		c.RiskPerTrade = 0.005
	}
	if c.SLATR == 0 {
		c.SLATR = 1.5
	}
	if c.MinSignalScore == 0 {
		c.MinSignalScore = 0.3
	}
	if c.WarmupBars == 0 {
		c.WarmupBars = 60
	}
	return c
}

// Strategy is a strategy.Strategy implementation that composes N alphas.
type Strategy struct {
	cfg    Config
	alphas []Alpha
	bars   []exchange.Kline
	posQty float64 // current position size (signed: + = long, - = short)
}

// New returns a Composite strategy. Panics if alphas is empty.
func New(alphas []Alpha, cfg Config) *Strategy {
	if len(alphas) == 0 {
		panic("composite: at least one alpha required")
	}
	return &Strategy{cfg: cfg.withDefaults(), alphas: alphas}
}

func (s *Strategy) Name() string { return "composite" }

func (s *Strategy) OnBar(ctx *strategy.Context, bar exchange.Kline)  {}
func (s *Strategy) OnFill(ctx *strategy.Context, fill strategy.Fill) {}
