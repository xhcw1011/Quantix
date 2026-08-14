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

// ─── State persistence (2026-08-06: circuit breaker must survive a restart) ──

// fakeRiskStore is an in-memory risk.StateStore for tests.
type fakeRiskStore struct {
	halted     bool
	peakEquity float64
	ok         bool
	saves      int
}

func (f *fakeRiskStore) Save(halted bool, peakEquity float64) error {
	f.halted, f.peakEquity, f.ok = halted, peakEquity, true
	f.saves++
	return nil
}
func (f *fakeRiskStore) Load() (bool, float64, bool) { return f.halted, f.peakEquity, f.ok }

// TestRisk_SetStateStoreRestoresPriorHalt reproduces the 2026-08-06 finding:
// risk.Manager is recreated fresh on every engine start (restart or
// first-ever), so a real, still-in-force halt from before a crash/restart
// was silently cleared -- the account could resume trading past its own
// drawdown cap. A persisted halt must survive being wired into a brand new
// Manager instance.
func TestRisk_SetStateStoreRestoresPriorHalt(t *testing.T) {
	m := newManager(0.10, 0.15, 0.02) // freshly constructed, as if just after a restart
	store := &fakeRiskStore{halted: true, peakEquity: 10_000, ok: true}

	m.SetStateStore(store)

	if !m.Halted() {
		t.Fatal("a persisted halt must survive being restored into a new Manager instance")
	}
	err := m.Check(buyReq(0.001), 8_000, 0, 50_000)
	if err != ErrCircuitBreaker {
		t.Fatalf("orders must still be blocked after restoring a prior halt, got: %v", err)
	}
}

// TestRisk_SetStateStoreNeverLowersPeakEquity: the peak baseline must never
// regress to a lower, already-drawn-down value just because the process
// restarted -- that would let another full MaxDrawdownPct of loss happen on
// top of a loss that already occurred before the restart.
func TestRisk_SetStateStoreNeverLowersPeakEquity(t *testing.T) {
	m := New(Config{MaxDrawdownPct: 0.15}, 8_000, zap.NewNop()) // constructed with CURRENT (already-reduced) equity, as manager.go does on restart
	store := &fakeRiskStore{halted: false, peakEquity: 10_000, ok: true}

	m.SetStateStore(store)

	if got := m.PeakEquity(); got != 10_000 {
		t.Fatalf("peak equity must restore to the persisted historical peak, got %v want 10000", got)
	}
}

// TestRisk_SetStateStoreNoPriorState covers the genuinely-first-ever-start
// case: nothing to restore, the manager keeps its constructor-seeded values.
func TestRisk_SetStateStoreNoPriorState(t *testing.T) {
	m := New(Config{MaxDrawdownPct: 0.15}, 10_000, zap.NewNop())
	store := &fakeRiskStore{} // ok=false: no row yet

	m.SetStateStore(store)

	if m.Halted() {
		t.Fatal("must not halt when there is no persisted state")
	}
	if got := m.PeakEquity(); got != 10_000 {
		t.Fatalf("peak equity must keep its constructor value, got %v want 10000", got)
	}
}

// TestRisk_PersistsOnCircuitBreakerTrip verifies the halt is written through
// to the store the moment it fires, not just held in memory (else the next
// restart still can't see it).
func TestRisk_PersistsOnCircuitBreakerTrip(t *testing.T) {
	m := newManager(0.10, 0.15, 0.02)
	store := &fakeRiskStore{}
	m.SetStateStore(store)

	m.UpdateEquity(8_400) //nolint:errcheck // 16% drawdown -> trips

	if !store.halted {
		t.Fatal("circuit breaker trip must be persisted immediately")
	}
	if store.saves == 0 {
		t.Fatal("expected at least one Save call")
	}
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
	assert.Contains(t, err.Error(), "仓位大小")
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
