package risk

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
)

func newManager(maxPosPct, maxDDPct, maxLossPct float64) *Manager {
	log, _ := zap.NewDevelopment()
	return New(Config{
		MaxPositionPct:   maxPosPct,
		MaxDrawdownPct:   maxDDPct,
		MaxSingleLossPct: maxLossPct,
	}, 10_000, log)
}

func buyReq(qty float64) strategy.OrderRequest {
	return strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy, Qty: qty}
}

// ─── Position size rule ───────────────────────────────────────────────────────

func TestRisk_AllowsValidBuy(t *testing.T) {
	m := newManager(0.10, 0.15, 0.02)
	// Buying 0.003 BTC at $50k = $150 = 1.5% of equity
	// Position check: 1.5% < 10% limit ✓
	// Single-loss check: 1.5% < 2% limit ✓
	err := m.Check(buyReq(0.003), 10_000, 0, 50_000)
	assert.NoError(t, err)
}

func TestRisk_BlocksOversizedPosition(t *testing.T) {
	m := newManager(0.10, 0.15, 0.02)
	// Buying 0.1 BTC at $50k = $5000 = 50% of equity → over 10% limit
	err := m.Check(buyReq(0.1), 10_000, 0, 50_000)
	assert.Error(t, err, "should block position > 10% of equity")
}

// ─── Circuit breaker ──────────────────────────────────────────────────────────

func TestRisk_CircuitBreakerFires(t *testing.T) {
	m := newManager(0.10, 0.15, 0.02)
	// Equity drops 16% from peak of $10k
	err := m.UpdateEquity(8_400)
	require.ErrorIs(t, err, ErrCircuitBreaker)
	assert.True(t, m.Halted())
}

func TestRisk_CircuitBreakerBlocksOrders(t *testing.T) {
	m := newManager(0.10, 0.15, 0.02)
	m.UpdateEquity(8_000) //nolint:errcheck // triggers circuit breaker

	err := m.Check(buyReq(0.01), 8_000, 0, 50_000)
	assert.ErrorIs(t, err, ErrCircuitBreaker)
}

func TestRisk_CircuitBreakerNotFiredBelowThreshold(t *testing.T) {
	m := newManager(0.10, 0.15, 0.02)
	// 10% drawdown is below 15% threshold
	err := m.UpdateEquity(9_000)
	assert.NoError(t, err)
	assert.False(t, m.Halted())
}

func TestRisk_PeakEquityUpdates(t *testing.T) {
	m := newManager(0.10, 0.20, 0.02)
	// Equity rises to new peak
	m.UpdateEquity(11_000) //nolint:errcheck
	// Then drops 10% from $11k peak → still under 20% threshold
	err := m.UpdateEquity(9_900)
	assert.NoError(t, err, "9.1%% drawdown from $11k peak should not trigger 20%% limit")
}

func TestRisk_Reset(t *testing.T) {
	m := newManager(0.10, 0.15, 0.02)
	m.UpdateEquity(8_000) //nolint:errcheck
	require.True(t, m.Halted())

	m.Reset(8_000)
	assert.False(t, m.Halted())
	assert.NoError(t, m.Check(buyReq(0.001), 8_000, 0, 50_000))
}

// ─── Kelly Criterion tests ────────────────────────────────────────────────────

func TestKelly_PositiveEdge(t *testing.T) {
	// WinRate=60%, avgWin=5%, avgLoss=3%
	f := Kelly(0.60, 0.05, 0.03)
	// f* = (0.6*0.05 - 0.4*0.03) / 0.05 = (0.03 - 0.012) / 0.05 = 0.36
	assert.InDelta(t, 0.36, f, 0.001)
}

func TestKelly_NegativeEdge_ClampedToZero(t *testing.T) {
	// WinRate=40%, avgWin=3%, avgLoss=5% → negative edge
	f := Kelly(0.40, 0.03, 0.05)
	assert.Equal(t, 0.0, f)
}

func TestHalfKelly_IsHalfOfFull(t *testing.T) {
	full := Kelly(0.60, 0.05, 0.03)
	half := HalfKelly(0.60, 0.05, 0.03)
	assert.InDelta(t, full*0.5, half, 1e-9)
}

func TestPositionSize_CappedByMaxPct(t *testing.T) {
	// Kelly says 50% but cap is 10%
	size := PositionSize(10_000, 0.50, 0.10)
	assert.Equal(t, 1_000.0, size)
}

func TestPositionSize_BelowCap(t *testing.T) {
	// Kelly says 5% which is under 10% cap
	size := PositionSize(10_000, 0.05, 0.10)
	assert.InDelta(t, 500.0, size, 0.01)
}

// ─── Short-opening order checks ──────────────────────────────────────────────

func TestRisk_BlocksOversizedShort(t *testing.T) {
	// MaxPositionPct=10%, equity=10000
	// Qty=0.04 BTC at $50k = $2000 = 20% of equity → over 10% limit
	m := newManager(0.10, 1.0, 1.0)
	req := strategy.OrderRequest{
		Symbol:       "BTCUSDT",
		Side:         strategy.SideSell,
		PositionSide: strategy.PositionSideShort,
		Qty:          0.04,
	}
	err := m.Check(req, 10_000, 0, 50_000)
	assert.Error(t, err, "should block short that exceeds max position size")
	assert.Contains(t, err.Error(), "position size")
}

func TestRisk_AllowsValidShort(t *testing.T) {
	// MaxPositionPct=50%, equity=10000
	// Qty=0.02 BTC at $50k = $1000 = 10% of equity → under 50% limit
	m := newManager(0.50, 1.0, 1.0)
	req := strategy.OrderRequest{
		Symbol:       "BTCUSDT",
		Side:         strategy.SideSell,
		PositionSide: strategy.PositionSideShort,
		Qty:          0.02,
	}
	err := m.Check(req, 10_000, 0, 50_000)
	assert.NoError(t, err, "valid short within position limit should be allowed")
}

func TestRisk_ClosingSellBypassesCheck(t *testing.T) {
	// MaxPositionPct=1% (very tight), MaxSingleLossPct=1%
	// SELL to close a LONG: PositionSide=LONG means closing, not opening a short.
	// Even a huge qty should pass because closing trades are not subject to position-size checks.
	m := newManager(0.01, 1.0, 0.01)
	req := strategy.OrderRequest{
		Symbol:       "BTCUSDT",
		Side:         strategy.SideSell,
		PositionSide: strategy.PositionSideLong,
		Qty:          1.0,
	}
	err := m.Check(req, 10_000, 5_000, 50_000)
	assert.NoError(t, err, "closing a long position should bypass position-size check")
}
