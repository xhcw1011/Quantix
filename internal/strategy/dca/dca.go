// Package dca implements a spot dollar-cost-averaging strategy: on a calendar
// cadence (daily/weekly/monthly) it places a single LIMIT buy for a fixed quote
// amount, at a configurable offset from the market price. Buy-only (spot accumulate).
package dca

import (
	"fmt"
	"math"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// dcaPeriodKey returns the calendar bucket for time t under the given interval, so
// a DCA buy fires once when the key changes. daily→"2006-01-02", weekly→ISO
// "2006-W02", monthly→"2006-01". Unknown interval defaults to daily.
func dcaPeriodKey(interval string, t time.Time) string {
	switch strings.ToLower(interval) {
	case "weekly":
		y, w := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	case "monthly":
		return t.Format("2006-01")
	default: // daily
		return t.Format("2006-01-02")
	}
}

// dcaLimitPrice returns the limit price for a DCA buy: marketPrice adjusted by
// offsetPct (negative = below market / patient maker; positive = above / crosses).
func dcaLimitPrice(marketPrice, offsetPct float64) float64 {
	return marketPrice * (1 + offsetPct)
}

// Config parameters for the DCA strategy.
type Config struct {
	Symbol         string  // e.g. "BTCUSDT"
	BuyQuoteAmount float64 // quote (USDT) spent per buy
	Interval       string  // "daily" | "weekly" | "monthly"
	LimitOffsetPct float64 // signed offset from market for the limit price
	MaxTotalQuote  float64 // cap on total invested; 0 = no cap
}

// DCA is a spot dollar-cost-averaging strategy.
type DCA struct {
	cfg           Config
	log           *zap.Logger
	lastKey       string  // last period key that triggered a buy
	pendingOrder  string  // resting DCA limit order id (cancelled on next period)
	totalInvested float64 // sum of filled buy notional (quote)
}

// New creates a DCA strategy.
func New(cfg Config, log *zap.Logger) *DCA { return &DCA{cfg: cfg, log: log} }

// Name implements strategy.Strategy.
func (d *DCA) Name() string {
	return fmt.Sprintf("DCA(%s,%s,$%.0f)", d.cfg.Symbol, strings.ToLower(d.cfg.Interval), d.cfg.BuyQuoteAmount)
}

// OnBar implements strategy.Strategy. On each new calendar period it cancels any
// unfilled prior DCA limit and places a fresh limit buy.
func (d *DCA) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if bar.Symbol != d.cfg.Symbol || bar.Close <= 0 {
		return
	}
	key := dcaPeriodKey(d.cfg.Interval, bar.CloseTime)
	if key == d.lastKey {
		return // same period — one buy per period
	}
	d.lastKey = key

	if d.cfg.MaxTotalQuote > 0 && d.totalInvested+d.cfg.BuyQuoteAmount > d.cfg.MaxTotalQuote {
		d.log.Info("DCA: MaxTotalQuote reached — skipping buy",
			zap.String("period", key), zap.Float64("total_invested", d.totalInvested))
		return
	}

	// Cancel a still-resting limit from the previous period (avoid stacking).
	if d.pendingOrder != "" {
		if err := ctx.CancelOrder(d.pendingOrder); err != nil {
			d.log.Warn("DCA: cancel stale order failed", zap.String("id", d.pendingOrder), zap.Error(err))
		}
		d.pendingOrder = ""
	}

	limitPx := math.Round(dcaLimitPrice(bar.Close, d.cfg.LimitOffsetPct)*100) / 100
	if limitPx <= 0 {
		return
	}
	qty := d.cfg.BuyQuoteAmount / limitPx
	id := ctx.PlaceOrder(strategy.OrderRequest{
		Symbol: d.cfg.Symbol,
		Side:   strategy.SideBuy,
		Type:   strategy.OrderLimit,
		Qty:    qty,
		Price:  limitPx,
	})
	if id != "" {
		d.pendingOrder = id
		d.log.Info("DCA: limit buy placed",
			zap.String("period", key), zap.Float64("price", limitPx),
			zap.Float64("qty", qty), zap.Float64("quote", d.cfg.BuyQuoteAmount), zap.String("id", id))
	}
}

// OnFill implements strategy.Strategy — tracks invested total and clears the pending order.
func (d *DCA) OnFill(ctx *strategy.Context, fill strategy.Fill) {
	if fill.Symbol != d.cfg.Symbol || fill.Side != strategy.SideBuy {
		return
	}
	d.totalInvested += fill.Qty * fill.Price
	d.pendingOrder = ""
	d.log.Info("DCA: buy filled",
		zap.Float64("qty", fill.Qty), zap.Float64("price", fill.Price),
		zap.Float64("total_invested", d.totalInvested))
}

func init() {
	registry.Register("dca", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := Config{Interval: "daily"}
		if v, ok := params["Symbol"].(string); ok {
			cfg.Symbol = v
		}
		if v, ok := params["BuyQuoteAmount"]; ok {
			cfg.BuyQuoteAmount = toFloat(v)
		}
		if v, ok := params["Interval"].(string); ok && v != "" {
			cfg.Interval = v
		}
		if v, ok := params["LimitOffsetPct"]; ok {
			cfg.LimitOffsetPct = toFloat(v)
		}
		if v, ok := params["MaxTotalQuote"]; ok {
			cfg.MaxTotalQuote = toFloat(v)
		}
		if cfg.Symbol == "" {
			return nil, fmt.Errorf("dca: Symbol is required")
		}
		if cfg.BuyQuoteAmount <= 0 {
			return nil, fmt.Errorf("dca: BuyQuoteAmount must be > 0")
		}
		return New(cfg, log), nil
	})
}

// toFloat coerces a params value (float64/int/int64) to float64.
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
