package macross

import "testing"

// These tests target the exact real-world incident (2026-08-07): a SHORT
// opened @64334.4 reverse-crossed on a bar closing @64320 -- nominally
// "profitable" (0.0224%) so AsymmetricExit's old bare `> 0` check allowed the
// close, but by the time the market order actually filled (@64332.5) the tiny
// gross profit was wiped out by the round-trip taker fee, landing a realized
// loss of -$1.12. profitableEnoughToClose must require the floating profit to
// clear MinProfitToClosePct, not just clear zero.

func TestMACross_ProfitableEnoughToClose_BlocksBelowThreshold(t *testing.T) {
	m := New(Config{EnableShort: true, MinProfitToClosePct: 0.001}) // 0.1%
	m.hasShort = true
	m.posEntry = 90

	// (90 - 89.95) / 90 = 0.0556% -- positive, but below the 0.1% threshold.
	if m.profitableEnoughToClose(89.95) {
		t.Error("expected a barely-positive floating profit below MinProfitToClosePct to NOT be enough to close")
	}
}

func TestMACross_ProfitableEnoughToClose_AllowsAboveThreshold(t *testing.T) {
	m := New(Config{EnableShort: true, MinProfitToClosePct: 0.001})
	m.hasShort = true
	m.posEntry = 90

	// (90 - 89.5) / 90 = 0.556% -- clears the 0.1% threshold.
	if !m.profitableEnoughToClose(89.5) {
		t.Error("expected a floating profit above MinProfitToClosePct to be enough to close")
	}
}

func TestMACross_ProfitableEnoughToClose_ReproducesThe20260807Incident(t *testing.T) {
	// Default MinProfitToClosePct (registry default 0.001 = 0.1%) applied
	// directly via New(), matching what a fresh Config{} without an explicit
	// override would NOT have -- so this test sets it explicitly to the same
	// value the registry factory defaults to, documenting the intent.
	m := New(Config{EnableShort: true, MinProfitToClosePct: 0.001})
	m.hasShort = true
	m.posEntry = 64334.4

	if m.profitableEnoughToClose(64320) {
		t.Error("expected the 2026-08-07 incident's 0.0224% floating profit to be rejected by the 0.1% threshold")
	}
}

func TestMACross_MinProfitToClose_HoldsWhenProfitBelowThreshold(t *testing.T) {
	m, ctx, broker := newHedgeMACross(Config{
		AsymmetricExit:      true,
		StopLossPct:         0,
		TrendFilterMin:      0,
		MinProfitToClosePct: 0.50, // deliberately absurd -- proves the gate is wired, not just present
	})

	// Same price path as TestAsymmetricExit_ClosesProfitableShortOnReverseCross
	// (a genuinely profitable ~13.3% reverse cross) -- with the old bare `> 0`
	// check this would close; with a 50% threshold it must NOT.
	prices := []float64{
		100, 101, 102, 103, 104, 105,
		100, 90, 80, 70, 60, 50,
		52, 55, 60, 65, 70, 78,
	}
	feedBars(m, ctx, broker, "BTCUSDT", prices)

	if !m.hasShort {
		t.Fatal("expected the short to still be held -- 13.3% profit doesn't clear the 50% threshold")
	}
	for _, o := range broker.orders {
		if o.PositionSide == "SHORT" && o.Side == "BUY" {
			t.Fatalf("short should not have been closed below MinProfitToClosePct, got close order: %+v", o)
		}
	}
}
