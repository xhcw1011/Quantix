// Package rebalance implements a spot portfolio rebalancing strategy.
// It maintains a target percentage of equity in the base asset; when the
// actual allocation drifts beyond a configurable band, it places a LIMIT
// order to restore the target (sells high, buys low).
package rebalance

import (
	"fmt"
	"math"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// Config holds the parameters for the rebalancing strategy.
type Config struct {
	// Symbol is the base/quote trading pair, e.g. "BTCUSDT".
	Symbol string
	// TargetBasePct is the desired fraction of total equity held in the base
	// asset (0..1 exclusive), e.g. 0.5 for 50% BTC / 50% USDT.
	TargetBasePct float64
	// BandPct is the drift threshold that triggers a rebalance.  When
	// |currentPct - targetPct| > BandPct a trade is placed.  Defaults to 0.05.
	BandPct float64
	// MinTradeQuote skips any rebalance whose notional size (in quote/USDT) is
	// below this value, avoiding dust orders on tiny portfolios.
	MinTradeQuote float64
}

// Rebalance is a long-only spot rebalancing strategy.
type Rebalance struct {
	cfg Config
	log *zap.Logger
}

// New creates a Rebalance strategy.
func New(cfg Config, log *zap.Logger) *Rebalance {
	if cfg.BandPct <= 0 {
		cfg.BandPct = 0.05
	}
	return &Rebalance{cfg: cfg, log: log}
}

// Name implements strategy.Strategy.
func (r *Rebalance) Name() string {
	return fmt.Sprintf("Rebalance(%s,%.0f%%)", r.cfg.Symbol, r.cfg.TargetBasePct*100)
}

// rebalanceTrade computes the rebalancing trade toward the target allocation.
//
// Parameters:
//   - baseQty   current base-asset holdings (e.g. BTC)
//   - cash      current quote-asset cash (e.g. USDT)
//   - price     current base asset price (quote per base)
//   - targetPct desired base fraction of total equity (0..1)
//   - bandPct   drift trigger; no trade when |current-target| <= bandPct
//   - minTradeQuote minimum trade size in quote; skip if below
//
// Returns tradeQuote (signed: >0 = BUY that many quote-units of base,
// <0 = SELL |tradeQuote| quote-units worth of base) and do=true only when
// the drift exceeds bandPct AND |tradeQuote| >= minTradeQuote.
// If total equity <= 0, returns (0, false).
func rebalanceTrade(baseQty, cash, price, targetPct, bandPct, minTradeQuote float64) (tradeQuote float64, do bool) {
	baseValue := baseQty * price
	total := cash + baseValue
	if total <= 0 {
		return 0, false
	}
	currentPct := baseValue / total
	drift := currentPct - targetPct
	if math.Abs(drift) <= bandPct {
		return 0, false
	}
	tq := (targetPct - currentPct) * total
	if math.Abs(tq) < minTradeQuote {
		return 0, false
	}
	return tq, true
}

// OnBar implements strategy.Strategy.  On each closed bar it checks whether
// the base-asset allocation has drifted beyond the band and, if so, places a
// LIMIT order to restore the target weight.
func (r *Rebalance) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if bar.Symbol != r.cfg.Symbol || bar.Close <= 0 {
		return
	}

	baseQty, _, _ := ctx.Portfolio.Position(r.cfg.Symbol)
	cash := ctx.Portfolio.Cash()
	price := bar.Close

	tq, do := rebalanceTrade(baseQty, cash, price, r.cfg.TargetBasePct, r.cfg.BandPct, r.cfg.MinTradeQuote)
	if !do {
		return
	}

	// Compute current allocation for logging.
	baseValue := baseQty * price
	total := cash + baseValue
	currentPct := baseValue / total

	qty := math.Abs(tq) / price
	limitPx := math.Round(price*100) / 100

	if tq > 0 {
		// Need more base: BUY.
		ctx.PlaceOrder(strategy.OrderRequest{
			Symbol: r.cfg.Symbol,
			Side:   strategy.SideBuy,
			Type:   strategy.OrderLimit,
			Qty:    qty,
			Price:  limitPx,
		})
		r.log.Info("rebalance: BUY",
			zap.String("symbol", r.cfg.Symbol),
			zap.Float64("qty", qty),
			zap.Float64("limitPx", limitPx),
			zap.Float64("currentPct", currentPct),
			zap.Float64("targetPct", r.cfg.TargetBasePct),
		)
	} else {
		// Need less base: SELL (never exceed what we hold).
		sellQty := math.Min(qty, baseQty)
		ctx.PlaceOrder(strategy.OrderRequest{
			Symbol: r.cfg.Symbol,
			Side:   strategy.SideSell,
			Type:   strategy.OrderLimit,
			Qty:    sellQty,
			Price:  limitPx,
		})
		r.log.Info("rebalance: SELL",
			zap.String("symbol", r.cfg.Symbol),
			zap.Float64("qty", sellQty),
			zap.Float64("limitPx", limitPx),
			zap.Float64("currentPct", currentPct),
			zap.Float64("targetPct", r.cfg.TargetBasePct),
		)
	}
}

// OnFill implements strategy.Strategy.  Rebalancing is fully stateless between
// bars (the portfolio view always reflects the up-to-date holdings), so fills
// require no additional bookkeeping.
func (r *Rebalance) OnFill(_ *strategy.Context, _ strategy.Fill) {}

// toFloat coerces a params value (float64/float32/int/int64) to float64.
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

func init() {
	registry.Register("rebalance", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := Config{BandPct: 0.05}
		if v, ok := params["Symbol"].(string); ok {
			cfg.Symbol = v
		}
		if v, ok := params["TargetBasePct"]; ok {
			cfg.TargetBasePct = toFloat(v)
		}
		if v, ok := params["BandPct"]; ok {
			if f := toFloat(v); f > 0 {
				cfg.BandPct = f
			}
		}
		if v, ok := params["MinTradeQuote"]; ok {
			cfg.MinTradeQuote = toFloat(v)
		}
		if cfg.Symbol == "" {
			return nil, fmt.Errorf("rebalance: Symbol is required")
		}
		if cfg.TargetBasePct <= 0 || cfg.TargetBasePct >= 1 {
			return nil, fmt.Errorf("rebalance: TargetBasePct must be in (0,1), got %v", cfg.TargetBasePct)
		}
		return New(cfg, log), nil
	})
}
