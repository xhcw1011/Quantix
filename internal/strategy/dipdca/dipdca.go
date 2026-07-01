// Package dipdca implements a spot "dip-weighted DCA" (逢跌加码定投) strategy:
// on a calendar cadence (daily/weekly/monthly) it places a single MARKET buy,
// scaling the quote amount UP when price is below its recent SMA — buying more
// on larger dips ("跌得多买得多"). Buy-only (spot accumulate).
package dipdca

import (
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// ─── Pure helpers ────────────────────────────────────────────────────────────

// dcaPeriodKey returns the calendar bucket for time t under the given interval,
// so a DCA buy fires once when the key changes.
// daily→"2006-01-02", weekly→ISO "2006-W02", monthly→"2006-01".
// Unknown interval defaults to daily (case-insensitive).
func dcaPeriodKey(interval string, t time.Time) string {
	switch strings.ToLower(interval) {
	case "weekly":
		y, w := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	case "monthly":
		return t.Format("2006-01")
	default: // daily (and any unknown)
		return t.Format("2006-01-02")
	}
}

// dipBuyAmount returns the quote amount to spend on a DCA buy, scaled up when
// price is below the SMA (value-averaging flavor).
//
// dip = (sma - price) / sma, clamped to [0,1] after dividing by dipRefPct.
// amount = base × (1 + clamp(dip/dipRefPct, 0, 1) × (dipMultiplier - 1))
//
// Guard rails:
//   - sma <= 0 or price >= sma  → return base (no dip, no scaling)
//   - dipMultiplier < 1         → treated as 1  (multiplier never shrinks buy)
//   - dipRefPct <= 0            → any positive dip triggers full multiplier
func dipBuyAmount(base, price, sma, dipRefPct, dipMultiplier float64) float64 {
	// Guard: no SMA or price not below SMA
	if sma <= 0 || price >= sma {
		return base
	}

	// Effective multiplier: never below 1
	eff := dipMultiplier
	if eff < 1 {
		eff = 1
	}

	// Compute dip fraction and clamp to [0,1]
	dip := (sma - price) / sma // > 0 since price < sma and sma > 0
	var ratio float64
	if dipRefPct <= 0 {
		ratio = 1 // any positive dip → full scale
	} else {
		ratio = dip / dipRefPct
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
	}

	return base * (1 + ratio*(eff-1))
}

// ─── Config ──────────────────────────────────────────────────────────────────

// Config holds all parameters for the DipDCA strategy.
type Config struct {
	Symbol          string  // e.g. "BTCUSDT"
	BaseQuoteAmount float64 // base quote (USDT) spent per buy
	Interval        string  // "daily" | "weekly" | "monthly"
	RefPeriod       int     // SMA look-back in bars (e.g. 20)
	DipRefPct       float64 // dip depth for full multiplier (e.g. 0.10 = 10%)
	DipMultiplier   float64 // max multiplier at/below DipRefPct (e.g. 2.0)
	MaxTotalQuote   float64 // cumulative cap in quote; 0 = no cap
}

// ─── Strategy ────────────────────────────────────────────────────────────────

// DipDCA is the dip-weighted dollar-cost-averaging strategy.
type DipDCA struct {
	cfg           Config
	log           *zap.Logger
	lastKey       string    // last period key that triggered a buy
	closes        []float64 // rolling close history, length <= RefPeriod
	totalInvested float64   // sum of filled buy notional (quote)
}

// New creates a DipDCA strategy instance.
func New(cfg Config, log *zap.Logger) *DipDCA {
	return &DipDCA{cfg: cfg, log: log}
}

// Name implements strategy.Strategy.
func (d *DipDCA) Name() string {
	return fmt.Sprintf("DipDCA(%s,%s)", d.cfg.Symbol, strings.ToLower(d.cfg.Interval))
}

// OnBar implements strategy.Strategy. Accumulates close history, fires one MARKET
// buy per calendar period, scaled by how far price is below the rolling SMA.
func (d *DipDCA) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if bar.Symbol != d.cfg.Symbol || bar.Close <= 0 {
		return
	}

	// Maintain rolling close history (trim to RefPeriod).
	d.closes = append(d.closes, bar.Close)
	if d.cfg.RefPeriod > 0 && len(d.closes) > d.cfg.RefPeriod {
		d.closes = d.closes[len(d.closes)-d.cfg.RefPeriod:]
	}

	key := dcaPeriodKey(d.cfg.Interval, bar.CloseTime)
	if key == d.lastKey {
		return // same period — one buy per period
	}
	d.lastKey = key

	// Cap check: even the minimum (base) buy must fit within the remaining quota.
	if d.cfg.MaxTotalQuote > 0 && d.totalInvested+d.cfg.BaseQuoteAmount > d.cfg.MaxTotalQuote {
		d.log.Info("DipDCA: MaxTotalQuote reached — skipping buy",
			zap.String("period", key),
			zap.Float64("total_invested", d.totalInvested),
			zap.Float64("max", d.cfg.MaxTotalQuote))
		return
	}

	// Compute SMA over available history.
	n := len(d.closes)
	var sma float64
	if n > 0 {
		sum := 0.0
		for _, c := range d.closes {
			sum += c
		}
		sma = sum / float64(n)
	}

	price := bar.Close
	amt := dipBuyAmount(d.cfg.BaseQuoteAmount, price, sma, d.cfg.DipRefPct, d.cfg.DipMultiplier)
	qty := amt / price

	id := ctx.PlaceOrder(strategy.OrderRequest{
		Symbol: d.cfg.Symbol,
		Side:   strategy.SideBuy,
		Type:   strategy.OrderMarket,
		Qty:    qty,
		// PositionSide intentionally empty (spot / net mode)
	})
	d.log.Info("DipDCA: market buy placed",
		zap.String("period", key),
		zap.Float64("sma", sma),
		zap.Float64("price", price),
		zap.Float64("amt_quote", amt),
		zap.Float64("qty", qty),
		zap.String("id", id))
}

// OnFill implements strategy.Strategy — tracks total invested.
func (d *DipDCA) OnFill(ctx *strategy.Context, fill strategy.Fill) {
	if fill.Symbol != d.cfg.Symbol || fill.Side != strategy.SideBuy {
		return
	}
	d.totalInvested += fill.Qty * fill.Price
	d.log.Info("DipDCA: buy filled",
		zap.Float64("qty", fill.Qty),
		zap.Float64("price", fill.Price),
		zap.Float64("total_invested", d.totalInvested))
}

// ─── Registration ────────────────────────────────────────────────────────────

func init() {
	registry.Register("dipdca", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := Config{
			Interval:      "daily",
			RefPeriod:     20,
			DipRefPct:     0.10,
			DipMultiplier: 2.0,
		}
		if v, ok := params["Symbol"].(string); ok {
			cfg.Symbol = v
		}
		if v, ok := params["BaseQuoteAmount"]; ok {
			cfg.BaseQuoteAmount = toFloat(v)
		}
		if v, ok := params["Interval"].(string); ok && v != "" {
			cfg.Interval = v
		}
		if v, ok := params["RefPeriod"]; ok {
			cfg.RefPeriod = int(toFloat(v))
		}
		if v, ok := params["DipRefPct"]; ok {
			cfg.DipRefPct = toFloat(v)
		}
		if v, ok := params["DipMultiplier"]; ok {
			cfg.DipMultiplier = toFloat(v)
		}
		if v, ok := params["MaxTotalQuote"]; ok {
			cfg.MaxTotalQuote = toFloat(v)
		}
		if cfg.Symbol == "" {
			return nil, fmt.Errorf("dipdca: Symbol is required")
		}
		if cfg.BaseQuoteAmount <= 0 {
			return nil, fmt.Errorf("dipdca: BaseQuoteAmount must be > 0")
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
