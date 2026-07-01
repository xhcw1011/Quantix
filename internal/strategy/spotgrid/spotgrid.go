// Package spotgrid implements a long-only spot "低买高卖" step-grid strategy:
// it accumulates base asset by placing LIMIT buys on price dips and takes
// profits on rallies by selling individual tranches at the grid spread above
// their entry price.
package spotgrid

import (
	"fmt"
	"math"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// tranche represents one LIMIT buy unit placed by the grid.
type tranche struct {
	price float64 // buy price of this tranche (used to derive the sell target)
	qty   float64 // base-asset quantity bought
}

// Config holds the spotgrid strategy parameters.
type Config struct {
	Symbol       string  // e.g. "BTCUSDT"
	StepPct      float64 // grid spacing as a fraction, e.g. 0.02 = 2%
	QuotePerBuy  float64 // USDT committed per grid buy order
	MaxHoldQuote float64 // cap on total USDT invested; 0 = no cap
}

// SpotGrid is the spot step-grid strategy.
type SpotGrid struct {
	cfg          Config
	log          *zap.Logger
	tranches     []tranche // open (unfilled-sell) buy tranches, FIFO
	lastBuyPrice float64   // price at which the most recent grid buy was placed
	invested     float64   // total USDT committed to open tranches
}

// New creates a SpotGrid strategy.
func New(cfg Config, log *zap.Logger) *SpotGrid {
	return &SpotGrid{cfg: cfg, log: log}
}

// Name implements strategy.Strategy.
func (s *SpotGrid) Name() string {
	return fmt.Sprintf("SpotGrid(%s,%.1f%%)", s.cfg.Symbol, s.cfg.StepPct*100)
}

// ─────────────────────────────────────────────
// Pure decision functions (TDD targets)
// ─────────────────────────────────────────────

// gridShouldBuy reports whether to place a grid buy at the current price.
// Returns true on the very first buy (lastBuyPrice<=0) or when price has
// fallen at least stepPct below the last buy price.
func gridShouldBuy(price, lastBuyPrice, stepPct float64) bool {
	if lastBuyPrice <= 0 {
		return true
	}
	// Must have fallen by at least stepPct: price <= lastBuyPrice * (1 - stepPct).
	return price <= lastBuyPrice*(1-stepPct)
}

// gridTrancheToSell returns the index of the lowest-priced held tranche for
// which the current price has risen at least stepPct above its entry
// (tranche.price*(1+stepPct) <= price). Returns -1 if no such tranche exists.
// When multiple tranches are eligible the one with the lowest entry price is
// chosen (most profitable sell first / FIFO by price).
func gridTrancheToSell(tranches []tranche, price, stepPct float64) int {
	bestIdx := -1
	bestPrice := math.MaxFloat64
	for i, tr := range tranches {
		target := tr.price * (1 + stepPct)
		if price >= target && tr.price < bestPrice {
			bestIdx = i
			bestPrice = tr.price
		}
	}
	return bestIdx
}

// ─────────────────────────────────────────────
// Strategy callbacks
// ─────────────────────────────────────────────

// OnBar implements strategy.Strategy.
// At most one sell and one buy are placed per bar.
func (s *SpotGrid) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if bar.Symbol != s.cfg.Symbol || bar.Close <= 0 {
		return
	}
	price := bar.Close

	// ── SELL first: release the lowest-priced tranche if eligible ──
	idx := gridTrancheToSell(s.tranches, price, s.cfg.StepPct)
	if idx >= 0 {
		tr := s.tranches[idx]
		sellPx := math.Round(price*100) / 100
		id := ctx.PlaceOrder(strategy.OrderRequest{
			Symbol: s.cfg.Symbol,
			Side:   strategy.SideSell,
			Type:   strategy.OrderLimit,
			Qty:    tr.qty,
			Price:  sellPx,
		})
		s.log.Info("spotgrid: LIMIT sell placed",
			zap.Float64("entry_price", tr.price),
			zap.Float64("sell_price", sellPx),
			zap.Float64("qty", tr.qty),
			zap.String("id", id))
		// Remove tranche and reduce invested capital.
		s.invested -= tr.qty * tr.price
		s.tranches = append(s.tranches[:idx], s.tranches[idx+1:]...)
	}

	// ── BUY: place a new grid buy if price has dipped enough ──
	if gridShouldBuy(price, s.lastBuyPrice, s.cfg.StepPct) {
		if s.cfg.MaxHoldQuote > 0 && s.invested+s.cfg.QuotePerBuy > s.cfg.MaxHoldQuote {
			s.log.Info("spotgrid: MaxHoldQuote reached — skipping buy",
				zap.Float64("invested", s.invested),
				zap.Float64("max", s.cfg.MaxHoldQuote))
			return
		}
		buyPx := math.Round(price*100) / 100
		qty := s.cfg.QuotePerBuy / buyPx
		id := ctx.PlaceOrder(strategy.OrderRequest{
			Symbol: s.cfg.Symbol,
			Side:   strategy.SideBuy,
			Type:   strategy.OrderLimit,
			Qty:    qty,
			Price:  buyPx,
		})
		s.tranches = append(s.tranches, tranche{price: buyPx, qty: qty})
		s.lastBuyPrice = buyPx
		s.invested += s.cfg.QuotePerBuy
		s.log.Info("spotgrid: LIMIT buy placed",
			zap.Float64("price", buyPx),
			zap.Float64("qty", qty),
			zap.Float64("quote", s.cfg.QuotePerBuy),
			zap.Float64("invested", s.invested),
			zap.String("id", id))
	}
}

// OnFill implements strategy.Strategy — no-op for v1 (tranches tracked on placement).
func (s *SpotGrid) OnFill(_ *strategy.Context, _ strategy.Fill) {}

// ─────────────────────────────────────────────
// Registry wiring
// ─────────────────────────────────────────────

func init() {
	registry.Register("spotgrid", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := Config{}
		if v, ok := params["Symbol"].(string); ok {
			cfg.Symbol = v
		}
		if v, ok := params["StepPct"]; ok {
			cfg.StepPct = toFloat(v)
		}
		if v, ok := params["QuotePerBuy"]; ok {
			cfg.QuotePerBuy = toFloat(v)
		}
		if v, ok := params["MaxHoldQuote"]; ok {
			cfg.MaxHoldQuote = toFloat(v)
		}
		if cfg.Symbol == "" {
			return nil, fmt.Errorf("spotgrid: Symbol is required")
		}
		if cfg.StepPct <= 0 {
			return nil, fmt.Errorf("spotgrid: StepPct must be > 0")
		}
		if cfg.QuotePerBuy <= 0 {
			return nil, fmt.Errorf("spotgrid: QuotePerBuy must be > 0")
		}
		return New(cfg, log), nil
	})
}

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
