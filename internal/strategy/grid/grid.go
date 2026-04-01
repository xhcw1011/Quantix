// Package grid implements a grid trading strategy.
//
// The strategy divides the price range into evenly-spaced levels.
// It places buy orders as price drops through each grid level and
// sell orders as price recovers through those levels.
package grid

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// Config holds the tunable parameters for the Grid strategy.
type Config struct {
	Symbol      string
	GridLevels  int     // number of grid levels per side, default 5
	GridSpacing float64 // fractional spacing between levels, default 0.01 (1%)
	BaseQty     float64 // fixed quantity per grid level (0 = use equal cash split)
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.GridLevels == 0 {
		out.GridLevels = 5
	}
	if out.GridSpacing == 0 {
		out.GridSpacing = 0.01
	}
	return out
}

// Grid is a simple grid trading strategy.
type Grid struct {
	cfg       Config
	basePrice float64
	// levelPrice[i] is the buy price for level i (below basePrice).
	levelPrice []float64
	// gridBuys[i] tracks whether we have a position at level i.
	gridBuys []bool
}

// New creates a new Grid strategy.
func New(cfg Config) *Grid {
	cfg = cfg.withDefaults()
	return &Grid{
		cfg:      cfg,
		gridBuys: make([]bool, cfg.GridLevels),
	}
}

// Name implements strategy.Strategy.
func (g *Grid) Name() string {
	return fmt.Sprintf("Grid(levels=%d,spacing=%.2f%%)", g.cfg.GridLevels, g.cfg.GridSpacing*100)
}

// OnBar implements strategy.Strategy.
func (g *Grid) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if bar.Symbol != g.cfg.Symbol {
		return
	}

	price := bar.Close

	// Initialise grid on first bar
	if g.basePrice == 0 {
		g.basePrice = price
		g.levelPrice = make([]float64, g.cfg.GridLevels)
		for i := 0; i < g.cfg.GridLevels; i++ {
			// Level i is (i+1) spacings below base
			g.levelPrice[i] = g.basePrice * (1 - float64(i+1)*g.cfg.GridSpacing)
		}
		ctx.Log.Info("grid initialised",
			zap.String("symbol", bar.Symbol),
			zap.Float64("basePrice", g.basePrice),
			zap.Int("levels", g.cfg.GridLevels),
			zap.Float64("spacing", g.cfg.GridSpacing),
		)
		return
	}

	// Check each grid level
	for i := 0; i < g.cfg.GridLevels; i++ {
		buyPrice := g.levelPrice[i]
		sellPrice := buyPrice * (1 + g.cfg.GridSpacing)

		// Buy signal: price dropped to or below level and we don't have a position here
		if price <= buyPrice && !g.gridBuys[i] {
			qty := g.cfg.BaseQty
			if qty == 0 {
				// Split cash equally across levels
				cash := ctx.Portfolio.Cash()
				qty = cash / float64(g.cfg.GridLevels) / price
			}
			if qty > 0 {
				ctx.Log.Info("grid BUY",
					zap.String("symbol", bar.Symbol),
					zap.Int("level", i),
					zap.Float64("levelPrice", buyPrice),
					zap.Float64("close", price),
					zap.Float64("qty", qty),
				)
				ctx.PlaceOrder(strategy.OrderRequest{
					Symbol: bar.Symbol,
					Side:   strategy.SideBuy,
					Type:   strategy.OrderMarket,
					Qty:    qty,
				})
				g.gridBuys[i] = true
			}
		}

		// Sell signal: price recovered above sell threshold and we have a position at this level
		if price >= sellPrice && g.gridBuys[i] {
			qty := g.cfg.BaseQty
			if qty == 0 {
				// Best-effort: use all position qty / levels held
				posQty, _, ok := ctx.Portfolio.Position(bar.Symbol)
				if ok {
					held := 0
					for _, b := range g.gridBuys {
						if b {
							held++
						}
					}
					if held > 0 {
						qty = posQty / float64(held)
					}
				}
			}
			if qty > 0 {
				ctx.Log.Info("grid SELL",
					zap.String("symbol", bar.Symbol),
					zap.Int("level", i),
					zap.Float64("sellPrice", sellPrice),
					zap.Float64("close", price),
					zap.Float64("qty", qty),
				)
				ctx.PlaceOrder(strategy.OrderRequest{
					Symbol: bar.Symbol,
					Side:   strategy.SideSell,
					Type:   strategy.OrderMarket,
					Qty:    qty,
				})
				g.gridBuys[i] = false
			}
		}
	}
}

// OnFill implements strategy.Strategy.
func (g *Grid) OnFill(_ *strategy.Context, _ strategy.Fill) {}

func init() {
	registry.Register("grid", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := Config{}
		if v, ok := params["Symbol"].(string); ok {
			cfg.Symbol = v
		}
		if v, ok := params["GridLevels"]; ok {
			cfg.GridLevels = toInt(v)
		}
		if v, ok := params["GridSpacing"]; ok {
			cfg.GridSpacing = toFloat(v)
		}
		if v, ok := params["BaseQty"]; ok {
			cfg.BaseQty = toFloat(v)
		}
		if cfg.GridLevels < 0 {
			return nil, fmt.Errorf("GridLevels must be >= 0 (got %d)", cfg.GridLevels)
		}
		if cfg.GridSpacing < 0 {
			return nil, fmt.Errorf("GridSpacing must be >= 0 (got %f)", cfg.GridSpacing)
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
