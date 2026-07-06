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
