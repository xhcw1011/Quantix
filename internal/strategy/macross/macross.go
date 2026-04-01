// Package macross implements a dual moving-average crossover strategy.
//
// Long-only mode (EnableShort=false, default):
//
//	Golden cross (fast > slow) → BUY all-in
//	Death cross  (fast < slow) → SELL all
//
// Hedge mode (EnableShort=true, for futures/swap contracts):
//
//	Golden cross → close any short → open LONG (PositionSide=LONG)
//	Death cross  → close any long  → open SHORT (PositionSide=SHORT)
//
// Optional stop-loss and take-profit orders are attached to opening fills
// when StopLossPct > 0 or TakeProfitPct > 0.
package macross

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// Config holds the tunable parameters for MACross.
type Config struct {
	Symbol        string
	FastPeriod    int     // default 10
	SlowPeriod    int     // default 30
	EnableShort   bool    // true = use hedge mode (LONG/SHORT PositionSide) for futures/swap
	StopLossPct   float64 // 0 = no stop loss; e.g. 0.02 = 2% from entry
	TakeProfitPct float64 // 0 = no take profit; e.g. 0.04 = 4% from entry
}

// MACross is a dual-SMA crossover strategy with optional short-selling support.
type MACross struct {
	cfg    Config
	closes []float64

	// Internal position state (used in hedge mode to track LONG/SHORT legs).
	// Updated via OnFill so we don't rely on PortfolioView for hedge positions.
	hasLong  bool
	hasShort bool
}

// New creates a new MACross strategy with the given configuration.
func New(cfg Config) *MACross {
	return &MACross{cfg: cfg}
}

// Name implements strategy.Strategy.
func (m *MACross) Name() string {
	if m.cfg.EnableShort {
		return fmt.Sprintf("MACross(%d,%d,hedge)", m.cfg.FastPeriod, m.cfg.SlowPeriod)
	}
	return fmt.Sprintf("MACross(%d,%d)", m.cfg.FastPeriod, m.cfg.SlowPeriod)
}

// OnBar implements strategy.Strategy.
func (m *MACross) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if bar.Symbol != m.cfg.Symbol {
		return
	}

	m.closes = append(m.closes, bar.Close)

	if len(m.closes) < m.cfg.SlowPeriod {
		return
	}

	fast := indicator.SMA(m.closes, m.cfg.FastPeriod)
	slow := indicator.SMA(m.closes, m.cfg.SlowPeriod)

	if m.cfg.EnableShort {
		m.onBarHedge(ctx, bar, fast, slow)
	} else {
		m.onBarSimple(ctx, bar, fast, slow)
	}
}

// onBarSimple handles the long-only (spot/net) mode.
func (m *MACross) onBarSimple(ctx *strategy.Context, bar exchange.Kline, fast, slow []float64) {
	_, _, hasPosition := ctx.Portfolio.Position(bar.Symbol)

	switch {
	case indicator.CrossOver(fast, slow) && !hasPosition:
		ctx.Log.Info("golden cross — BUY",
			zap.String("symbol", bar.Symbol),
			zap.Float64("fast", indicator.Last(fast)),
			zap.Float64("slow", indicator.Last(slow)),
			zap.Float64("close", bar.Close),
		)
		req := strategy.OrderRequest{
			Symbol: bar.Symbol,
			Side:   strategy.SideBuy,
			Type:   strategy.OrderMarket,
			Qty:    0, // all-in
		}
		if m.cfg.StopLossPct > 0 {
			req.StopLoss = bar.Close * (1 - m.cfg.StopLossPct)
		}
		if m.cfg.TakeProfitPct > 0 {
			req.TakeProfit = bar.Close * (1 + m.cfg.TakeProfitPct)
		}
		ctx.PlaceOrder(req)

	case indicator.CrossUnder(fast, slow) && hasPosition:
		ctx.Log.Info("death cross — SELL",
			zap.String("symbol", bar.Symbol),
			zap.Float64("fast", indicator.Last(fast)),
			zap.Float64("slow", indicator.Last(slow)),
			zap.Float64("close", bar.Close),
		)
		ctx.PlaceOrder(strategy.OrderRequest{
			Symbol: bar.Symbol,
			Side:   strategy.SideSell,
			Type:   strategy.OrderMarket,
			Qty:    0, // close all
		})
	}
}

// onBarHedge handles the hedge mode (simultaneous LONG/SHORT for futures/swap).
func (m *MACross) onBarHedge(ctx *strategy.Context, bar exchange.Kline, fast, slow []float64) {
	switch {
	case indicator.CrossOver(fast, slow):
		// Golden cross: close short (if open), then open long
		ctx.Log.Info("golden cross — close SHORT, open LONG",
			zap.String("symbol", bar.Symbol),
			zap.Float64("fast", indicator.Last(fast)),
			zap.Float64("slow", indicator.Last(slow)),
			zap.Float64("close", bar.Close),
		)
		if m.hasShort {
			ctx.PlaceOrder(strategy.CloseShort(bar.Symbol, 0))
		}
		if !m.hasLong {
			req := strategy.OpenLong(bar.Symbol, 0)
			if m.cfg.StopLossPct > 0 {
				req.StopLoss = bar.Close * (1 - m.cfg.StopLossPct)
			}
			if m.cfg.TakeProfitPct > 0 {
				req.TakeProfit = bar.Close * (1 + m.cfg.TakeProfitPct)
			}
			ctx.PlaceOrder(req)
		}

	case indicator.CrossUnder(fast, slow):
		// Death cross: close long (if open), then open short
		ctx.Log.Info("death cross — close LONG, open SHORT",
			zap.String("symbol", bar.Symbol),
			zap.Float64("fast", indicator.Last(fast)),
			zap.Float64("slow", indicator.Last(slow)),
			zap.Float64("close", bar.Close),
		)
		if m.hasLong {
			ctx.PlaceOrder(strategy.CloseLong(bar.Symbol, 0))
		}
		if !m.hasShort {
			req := strategy.OpenShort(bar.Symbol, 0)
			if m.cfg.StopLossPct > 0 {
				req.StopLoss = bar.Close * (1 + m.cfg.StopLossPct)
			}
			if m.cfg.TakeProfitPct > 0 {
				req.TakeProfit = bar.Close * (1 - m.cfg.TakeProfitPct)
			}
			ctx.PlaceOrder(req)
		}
	}
}

// OnFill implements strategy.Strategy.
// Updates internal position state for hedge mode tracking.
func (m *MACross) OnFill(ctx *strategy.Context, fill strategy.Fill) {
	ctx.Log.Debug("fill received",
		zap.String("id", fill.ID),
		zap.String("side", string(fill.Side)),
		zap.String("position_side", string(fill.PositionSide)),
		zap.Float64("qty", fill.Qty),
		zap.Float64("price", fill.Price),
		zap.Float64("fee", fill.Fee),
	)

	if !m.cfg.EnableShort {
		return
	}

	// Update hedge position state
	switch {
	case fill.PositionSide == strategy.PositionSideLong && fill.Side == strategy.SideBuy:
		m.hasLong = true
	case fill.PositionSide == strategy.PositionSideLong && fill.Side == strategy.SideSell:
		m.hasLong = false
	case fill.PositionSide == strategy.PositionSideShort && fill.Side == strategy.SideSell:
		m.hasShort = true
	case fill.PositionSide == strategy.PositionSideShort && fill.Side == strategy.SideBuy:
		m.hasShort = false
	}
}

func init() {
	registry.Register("macross", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := Config{}
		if v, ok := params["Symbol"].(string); ok {
			cfg.Symbol = v
		}
		if v, ok := params["FastPeriod"]; ok {
			cfg.FastPeriod = toInt(v)
		}
		if v, ok := params["SlowPeriod"]; ok {
			cfg.SlowPeriod = toInt(v)
		}
		if v, ok := params["EnableShort"].(bool); ok {
			cfg.EnableShort = v
		}
		if v, ok := params["StopLossPct"]; ok {
			cfg.StopLossPct = toFloat(v)
		}
		if v, ok := params["TakeProfitPct"]; ok {
			cfg.TakeProfitPct = toFloat(v)
		}
		if cfg.FastPeriod == 0 {
			cfg.FastPeriod = 10
		}
		if cfg.SlowPeriod == 0 {
			cfg.SlowPeriod = 30
		}
		if cfg.FastPeriod >= cfg.SlowPeriod {
			return nil, fmt.Errorf("FastPeriod (%d) must be less than SlowPeriod (%d)",
				cfg.FastPeriod, cfg.SlowPeriod)
		}
		if cfg.StopLossPct < 0 {
			return nil, fmt.Errorf("StopLossPct must be >= 0 (got %.4f)", cfg.StopLossPct)
		}
		if cfg.TakeProfitPct < 0 {
			return nil, fmt.Errorf("TakeProfitPct must be >= 0 (got %.4f)", cfg.TakeProfitPct)
		}
		return New(cfg), nil
	})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func toInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
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
