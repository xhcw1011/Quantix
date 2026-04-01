package backtest

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMaxDrawdown_FlatCurve(t *testing.T) {
	curve := []EquityPoint{
		{Time: time.Now(), Equity: 10_000},
		{Time: time.Now().Add(time.Hour), Equity: 10_000},
	}
	pct, abs := calcMaxDrawdown(curve)
	assert.Equal(t, 0.0, pct)
	assert.Equal(t, 0.0, abs)
}

func TestMaxDrawdown_DownThenUp(t *testing.T) {
	t0 := time.Now()
	curve := []EquityPoint{
		{Time: t0, Equity: 10_000},
		{Time: t0.Add(time.Hour), Equity: 8_000},  // -20%
		{Time: t0.Add(2 * time.Hour), Equity: 12_000},
	}
	pct, abs := calcMaxDrawdown(curve)
	assert.InDelta(t, 20.0, pct, 0.01)
	assert.InDelta(t, 2000.0, abs, 0.01)
}

func TestSharpe_PositiveReturns(t *testing.T) {
	t0 := time.Now()
	curve := make([]EquityPoint, 365)
	equity := 10_000.0
	for i := range curve {
		equity *= 1.001 // 0.1% daily gain
		curve[i] = EquityPoint{Time: t0.Add(time.Duration(i) * 24 * time.Hour), Equity: equity}
	}
	sharpe := calcSharpe(curve)
	// With constant positive returns and no variance, Sharpe should be very high
	assert.Greater(t, sharpe, 5.0, "constant positive returns → high Sharpe")
}

func TestSharpe_ZeroVolatility_ReturnsZero(t *testing.T) {
	t0 := time.Now()
	curve := []EquityPoint{
		{Time: t0, Equity: 10_000},
		{Time: t0.Add(24 * time.Hour), Equity: 10_000},
		{Time: t0.Add(48 * time.Hour), Equity: 10_000},
	}
	sharpe := calcSharpe(curve)
	assert.True(t, math.IsNaN(sharpe) || sharpe == 0,
		"zero variance should yield 0 (or NaN) Sharpe")
}

func TestCalcMetrics_NoTrades(t *testing.T) {
	p := NewPortfolio(10_000)
	t0 := time.Now()
	p.recordEquity(t0, map[string]float64{"X": 100})
	p.recordEquity(t0.Add(time.Hour), map[string]float64{"X": 100})

	r := CalcMetrics("test", "X", "1h", p, t0, t0.Add(time.Hour), 2)
	assert.Equal(t, 0, r.TotalTrades)
	assert.InDelta(t, 0.0, r.TotalReturn, 0.01)
}
