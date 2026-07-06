package orgateway

import (
	"fmt"
	"sync"
	"time"

	"github.com/Quantix/quantix/internal/strategy"
)

// Risk Reason codes. Every DENY carries one so decisions are auditable and
// countable in shadow-mode review.
const (
	// Layer 1 — order safety (belongs in the ORG order gateway).
	ReasonMaxGrossLeverage    = "MAX_GROSS_LEVERAGE"
	ReasonMaxNotionalPerOrder = "MAX_NOTIONAL_PER_ORDER"
	ReasonOrderRate           = "ORDER_RATE_LIMIT"
	// Portfolio/account layer — NOT the order gateway (see note below).
	ReasonMaxPositionPct    = "MAX_POSITION_PCT"
	ReasonMaxSingleTradePct = "MAX_SINGLE_TRADE_PCT"
	ReasonDailyLoss         = "DAILY_LOSS_LIMIT"
	ReasonAccountDrawdown   = "ACCOUNT_DRAWDOWN"
)

// ─── Layer 1: order-safety rules ────────────────────────────────────────────────
// These judge whether a SINGLE order is safe — strategy- and portfolio-agnostic.
// This is all the ORG order gateway should enforce: max leverage, max notional per
// order, order-rate limit (blacklist would go here too).

// MaxPositionPctRule caps a single symbol's position notional at Max fraction of
// equity. Mirrors the paper risk.Manager's Rule 1, now applied engine-wide.
type MaxPositionPctRule struct{ Max float64 }

func (r MaxPositionPctRule) Name() string { return ReasonMaxPositionPct }

func (r MaxPositionPctRule) Eval(req strategy.OrderRequest, st OrderState) Decision {
	if !opensOrIncreases(req) || st.Equity <= 0 || r.Max <= 0 {
		return allow
	}
	newPos := st.PositionValue + orderCost(req, st)
	if newPos/st.Equity > r.Max {
		return Decision{Reason: ReasonMaxPositionPct, Detail: fmt.Sprintf(
			"%s position %.2f = %.1f%% of equity %.2f > max %.0f%%",
			req.Symbol, newPos, newPos/st.Equity*100, st.Equity, r.Max*100)}
	}
	return allow
}

// MaxSingleTradePctRule caps one order's notional at Max fraction of equity.
// Mirrors the paper risk.Manager's Rule 2, now applied engine-wide.
type MaxSingleTradePctRule struct{ Max float64 }

func (r MaxSingleTradePctRule) Name() string { return ReasonMaxSingleTradePct }

func (r MaxSingleTradePctRule) Eval(req strategy.OrderRequest, st OrderState) Decision {
	if !opensOrIncreases(req) || st.Equity <= 0 || r.Max <= 0 {
		return allow
	}
	cost := orderCost(req, st)
	if cost/st.Equity > r.Max {
		return Decision{Reason: ReasonMaxSingleTradePct, Detail: fmt.Sprintf(
			"order %.2f = %.1f%% of equity %.2f > max single-trade %.0f%%",
			cost, cost/st.Equity*100, st.Equity, r.Max*100)}
	}
	return allow
}

// MaxGrossLeverageRule caps total gross notional at Frac × equity × leverage.
// This subsumes the live broker's ad-hoc gross-exposure guard (added after the
// 31-ETH runaway) into the unified gateway.
type MaxGrossLeverageRule struct{ Frac float64 }

func (r MaxGrossLeverageRule) Name() string { return ReasonMaxGrossLeverage }

func (r MaxGrossLeverageRule) Eval(req strategy.OrderRequest, st OrderState) Decision {
	if !opensOrIncreases(req) || st.Equity <= 0 || r.Frac <= 0 || st.Leverage <= 0 {
		return allow
	}
	newGross := st.GrossNotional + orderCost(req, st)
	limit := st.Equity * st.Leverage * r.Frac
	if newGross > limit {
		return Decision{Reason: ReasonMaxGrossLeverage, Detail: fmt.Sprintf(
			"gross %.2f > limit %.2f (equity %.2f × lev %.1f × %.0f%%)",
			newGross, limit, st.Equity, st.Leverage, r.Frac*100)}
	}
	return allow
}

// MaxNotionalPerOrderRule caps a single order's absolute notional (a fat-finger /
// runaway sanity limit, independent of account size). Max<=0 disables it.
type MaxNotionalPerOrderRule struct{ Max float64 }

func (r MaxNotionalPerOrderRule) Name() string { return ReasonMaxNotionalPerOrder }

func (r MaxNotionalPerOrderRule) Eval(req strategy.OrderRequest, st OrderState) Decision {
	if !opensOrIncreases(req) || r.Max <= 0 {
		return allow
	}
	cost := orderCost(req, st)
	if cost > r.Max {
		return Decision{Reason: ReasonMaxNotionalPerOrder, Detail: fmt.Sprintf(
			"order notional %.2f > per-order cap %.2f", cost, r.Max)}
	}
	return allow
}

// OrderRateRule limits opening-order frequency to Max per Window — a runaway-loop
// guard (the 31-ETH class). Stateful and concurrency-safe; uses st.Now. Bursts
// within Max are fine (a grid may place several orders on one bar); it only trips
// on sustained runaway. Max<=0 or a zero st.Now disables it.
type OrderRateRule struct {
	Max    int
	Window time.Duration

	mu    sync.Mutex
	times []time.Time
}

func (r *OrderRateRule) Name() string { return ReasonOrderRate }

func (r *OrderRateRule) Eval(req strategy.OrderRequest, st OrderState) Decision {
	if !opensOrIncreases(req) || r.Max <= 0 || st.Now.IsZero() {
		return allow
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cut := st.Now.Add(-r.Window)
	kept := r.times[:0]
	for _, t := range r.times {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	r.times = kept
	if len(r.times) >= r.Max {
		return Decision{Reason: ReasonOrderRate, Detail: fmt.Sprintf(
			"%d orders within %s ≥ max %d", len(r.times), r.Window, r.Max)}
	}
	r.times = append(r.times, st.Now)
	return allow
}

// ─── Portfolio / account layer — NOT the order gateway ───────────────────────────
// The rules below are relative to account/portfolio state (equity %, drawdown),
// not to a single order's intrinsic safety. Per the 3-layer split they belong in
// the Portfolio/account engine, which decides how much exposure each strategy gets
// — wiring them into the per-strategy order gateway wrongly kills single-symbol
// strategies (they concentrate capital into one position by design). Kept here as
// reusable Rule implementations for that layer to consume.
//
// These block new risk during a bad day / drawdown. They never block closes.

// DailyLossRule denies opening orders once the day's loss vs the day-start equity
// reaches Max (e.g. 0.03 = down 3% on the day → stop taking new risk).
type DailyLossRule struct{ Max float64 }

func (r DailyLossRule) Name() string { return ReasonDailyLoss }

func (r DailyLossRule) Eval(req strategy.OrderRequest, st OrderState) Decision {
	if !opensOrIncreases(req) || st.DayStartEquity <= 0 || r.Max <= 0 {
		return allow
	}
	loss := (st.DayStartEquity - st.Equity) / st.DayStartEquity
	if loss >= r.Max {
		return Decision{Reason: ReasonDailyLoss, Detail: fmt.Sprintf(
			"day loss %.1f%% (equity %.2f vs day-start %.2f) >= limit %.0f%%",
			loss*100, st.Equity, st.DayStartEquity, r.Max*100)}
	}
	return allow
}

// AccountDrawdownRule denies opening orders once peak-to-current drawdown reaches
// Max (e.g. 0.10 = 10% off the equity high → stop taking new risk).
type AccountDrawdownRule struct{ Max float64 }

func (r AccountDrawdownRule) Name() string { return ReasonAccountDrawdown }

func (r AccountDrawdownRule) Eval(req strategy.OrderRequest, st OrderState) Decision {
	if !opensOrIncreases(req) || st.PeakEquity <= 0 || r.Max <= 0 {
		return allow
	}
	dd := (st.PeakEquity - st.Equity) / st.PeakEquity
	if dd >= r.Max {
		return Decision{Reason: ReasonAccountDrawdown, Detail: fmt.Sprintf(
			"drawdown %.1f%% (equity %.2f vs peak %.2f) >= limit %.0f%%",
			dd*100, st.Equity, st.PeakEquity, r.Max*100)}
	}
	return allow
}
