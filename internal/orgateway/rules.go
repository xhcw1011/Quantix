package orgateway

import (
	"fmt"

	"github.com/Quantix/quantix/internal/strategy"
)

// Risk Reason codes. Every DENY carries one so decisions are auditable and
// countable in shadow-mode review.
const (
	ReasonMaxPositionPct    = "MAX_POSITION_PCT"
	ReasonMaxSingleTradePct = "MAX_SINGLE_TRADE_PCT"
	ReasonMaxGrossLeverage  = "MAX_GROSS_LEVERAGE"
	ReasonDailyLoss         = "DAILY_LOSS_LIMIT"
	ReasonAccountDrawdown   = "ACCOUNT_DRAWDOWN"
)

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

// ─── Layer 3: account-risk rules ────────────────────────────────────────────────
// These block new risk during a bad day / drawdown. They never block closes, so a
// position can always be exited. This turns the risk manager's soft circuit breaker
// (live-side it only logs) into a real order gate.

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
