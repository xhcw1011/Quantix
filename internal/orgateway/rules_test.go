package orgateway

import (
	"testing"

	"github.com/Quantix/quantix/internal/strategy"
)

func openLong(qty float64) strategy.OrderRequest {
	return strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: qty}
}
func closeLong(qty float64) strategy.OrderRequest {
	return strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideSell, PositionSide: strategy.PositionSideLong, Qty: qty}
}
func openShort(qty float64) strategy.OrderRequest {
	return strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideSell, PositionSide: strategy.PositionSideShort, Qty: qty}
}

func TestMaxPositionPctRule(t *testing.T) {
	r := MaxPositionPctRule{Max: 0.10}
	st := OrderState{Equity: 10000, PositionValue: 0, Price: 100}

	if d := r.Eval(openLong(50), st); d.Allow || d.Reason != ReasonMaxPositionPct { // 5000 = 50% > 10%
		t.Fatalf("over-cap open should DENY MAX_POSITION_PCT, got %+v", d)
	}
	if d := r.Eval(openLong(5), st); !d.Allow { // 500 = 5% <= 10%
		t.Fatalf("within-cap open should ALLOW, got %+v", d)
	}
	if d := r.Eval(closeLong(9999), st); !d.Allow { // reducing risk must never be blocked
		t.Fatalf("closing order must always ALLOW, got %+v", d)
	}
}

func TestMaxSingleTradePctRule(t *testing.T) {
	r := MaxSingleTradePctRule{Max: 0.02}
	st := OrderState{Equity: 10000, Price: 100}

	if d := r.Eval(openLong(5), st); d.Allow || d.Reason != ReasonMaxSingleTradePct { // 500 = 5% > 2%
		t.Fatalf("over single-trade should DENY, got %+v", d)
	}
	if d := r.Eval(openLong(1), st); !d.Allow { // 100 = 1% <= 2%
		t.Fatalf("within single-trade should ALLOW, got %+v", d)
	}
}

func TestMaxGrossLeverageRule(t *testing.T) {
	r := MaxGrossLeverageRule{Frac: 0.8}
	// equity 10000, lev 10 -> limit 10000*10*0.8 = 80000
	st := OrderState{Equity: 10000, Leverage: 10, GrossNotional: 78000, Price: 100}

	if d := r.Eval(openLong(50), st); d.Allow || d.Reason != ReasonMaxGrossLeverage { // +5000 -> 83000 > 80000
		t.Fatalf("over gross-leverage should DENY, got %+v", d)
	}
	if d := r.Eval(openLong(10), st); !d.Allow { // +1000 -> 79000 <= 80000
		t.Fatalf("within gross-leverage should ALLOW, got %+v", d)
	}
	if d := r.Eval(openShort(50), st); d.Allow { // opening a short also adds gross notional
		t.Fatalf("opening short over gross-leverage should DENY, got %+v", d)
	}
}

func TestDailyLossRule(t *testing.T) {
	r := DailyLossRule{Max: 0.03}
	// day started at 10000; now 9600 = -4% > 3% limit
	breached := OrderState{Equity: 9600, DayStartEquity: 10000, Price: 100}
	if d := r.Eval(openLong(1), breached); d.Allow || d.Reason != ReasonDailyLoss {
		t.Fatalf("over daily-loss should DENY opening, got %+v", d)
	}
	if d := r.Eval(closeLong(1), breached); !d.Allow { // can still exit during a bad day
		t.Fatalf("closing must ALLOW even past daily-loss, got %+v", d)
	}
	within := OrderState{Equity: 9800, DayStartEquity: 10000, Price: 100} // -2% <= 3%
	if d := r.Eval(openLong(1), within); !d.Allow {
		t.Fatalf("within daily-loss should ALLOW, got %+v", d)
	}
}

func TestAccountDrawdownRule(t *testing.T) {
	r := AccountDrawdownRule{Max: 0.10}
	breached := OrderState{Equity: 8800, PeakEquity: 10000, Price: 100} // -12% > 10%
	if d := r.Eval(openLong(1), breached); d.Allow || d.Reason != ReasonAccountDrawdown {
		t.Fatalf("over account-DD should DENY opening, got %+v", d)
	}
	if d := r.Eval(closeLong(1), breached); !d.Allow {
		t.Fatalf("closing must ALLOW even past account-DD, got %+v", d)
	}
	within := OrderState{Equity: 9200, PeakEquity: 10000, Price: 100} // -8% <= 10%
	if d := r.Eval(openLong(1), within); !d.Allow {
		t.Fatalf("within account-DD should ALLOW, got %+v", d)
	}
}

// Qty==0 is the strategy "all-in" signal (live broker resolves it to ~all cash);
// ORG must treat it as a full-size order, not a free pass.
func TestQtyZeroTreatedAsAllIn(t *testing.T) {
	r := MaxSingleTradePctRule{Max: 0.02}
	st := OrderState{Equity: 10000, Price: 100}
	if d := r.Eval(openLong(0), st); d.Allow {
		t.Fatalf("Qty=0 (all-in) must be treated as full-size and DENY, got %+v", d)
	}
}
