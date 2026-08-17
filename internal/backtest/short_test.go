package backtest

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Quantix/quantix/internal/strategy"
)

// ─── SHORT portfolio / broker tests ─────────────────────────────────────────
//
// Validates hedge-mode SHORT support added after discovering the original
// backtest silently dropped SHORT fills (SideSell with no long position →
// "no position to sell" error). Without these tests the regression would be
// invisible: the backtest returns a positive number of "trades" even if all
// SHORT attempts get rejected, masking the bug.

func TestShort_OpenAndClose_ProfitableOnPriceDrop(t *testing.T) {
	p := NewPortfolio(10_000)
	b := NewSimBroker(0.0002, 0, p, devLog())

	// Open SHORT at 50000
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideSell,
		PositionSide: strategy.PositionSideShort,
		Qty:          0.1,
	})
	openBar := makeBar("BTCUSDT", 50_000, time.Now())
	fills := b.Process(openBar)
	require.Len(t, fills, 1, "short open should produce a fill (previously dropped)")
	assert.Equal(t, strategy.PositionSideShort, fills[0].PositionSide)
	assert.Equal(t, strategy.SideSell, fills[0].Side)

	// Only fee debits cash — notional is implicit margin, not cash cost.
	expectedFee := 0.1 * 50_000 * 0.0002
	assert.InDelta(t, 10_000-expectedFee, p.Cash(), 1e-6)

	// Position registered on SHORT side, not LONG side.
	require.Contains(t, p.shortPositions, "BTCUSDT")
	assert.NotContains(t, p.longPositions, "BTCUSDT")

	// Close SHORT at 49000 (drop = profit). PnL = 0.1 × (50000 - 49000) = $100.
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideBuy,
		PositionSide: strategy.PositionSideShort,
		Qty:          0.1,
	})
	closeBar := makeBar("BTCUSDT", 49_000, time.Now().Add(time.Hour))
	fills = b.Process(closeBar)
	require.Len(t, fills, 1)

	require.Len(t, p.Trades, 1)
	tr := p.Trades[0]
	assert.Equal(t, strategy.PositionSideShort, tr.PositionSide)
	assert.InDelta(t, 100.0, tr.GrossPnL, 1e-6, "SHORT pnl = qty × (entry - exit)")
	assert.Less(t, tr.NetPnL, tr.GrossPnL, "fee must reduce net pnl")

	// Cash now reflects initial - fees + realized PnL, and SHORT position cleared.
	assert.NotContains(t, p.shortPositions, "BTCUSDT")
	netPnL := tr.NetPnL - expectedFee // subtract open-fee already debited
	assert.InDelta(t, 10_000+netPnL, p.Cash(), 1e-6)
}

func TestShort_LossOnPriceRise(t *testing.T) {
	p := NewPortfolio(10_000)
	b := NewSimBroker(0.0002, 0, p, devLog())

	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideSell,
		PositionSide: strategy.PositionSideShort,
		Qty:          0.1,
	})
	b.Process(makeBar("BTCUSDT", 50_000, time.Now()))

	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideBuy,
		PositionSide: strategy.PositionSideShort,
		Qty:          0.1,
	})
	b.Process(makeBar("BTCUSDT", 51_000, time.Now().Add(time.Hour)))

	require.Len(t, p.Trades, 1)
	tr := p.Trades[0]
	assert.InDelta(t, -100.0, tr.GrossPnL, 1e-6, "SHORT loses when price rises")
	assert.Less(t, tr.NetPnL, 0.0)
}

func TestShort_MarkToMarket_UnrealizedPnLInEquity(t *testing.T) {
	p := NewPortfolio(10_000)
	b := NewSimBroker(0, 0, p, devLog())

	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideSell,
		PositionSide: strategy.PositionSideShort,
		Qty:          0.1,
	})
	b.Process(makeBar("BTCUSDT", 50_000, time.Now()))

	// Price drops $500 → unrealized profit of $50 should show in equity.
	eq := p.Equity(map[string]float64{"BTCUSDT": 49_500})
	assert.InDelta(t, 10_050.0, eq, 1e-6, "unrealized SHORT pnl credited to equity")
}

func TestLongAndShort_CoexistInHedgeMode(t *testing.T) {
	// Hedge mode: both LONG and SHORT can be open simultaneously on the same
	// symbol. Previously the single-map portfolio couldn't represent this;
	// opening a SHORT on top of a LONG either got rejected or mangled the LONG.
	p := NewPortfolio(10_000)
	b := NewSimBroker(0, 0, p, devLog())

	// Open LONG 0.1 @ 50000 (spot-style, debits cash)
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideBuy,
		PositionSide: strategy.PositionSideLong,
		Qty:          0.1,
	})
	b.Process(makeBar("BTCUSDT", 50_000, time.Now()))
	require.Contains(t, p.longPositions, "BTCUSDT")

	// Open SHORT 0.05 @ 50000 (futures-style, no cash debit)
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideSell,
		PositionSide: strategy.PositionSideShort,
		Qty:          0.05,
	})
	b.Process(makeBar("BTCUSDT", 50_000, time.Now().Add(time.Hour)))

	// Both positions now exist.
	require.Contains(t, p.longPositions, "BTCUSDT")
	require.Contains(t, p.shortPositions, "BTCUSDT")
	assert.Equal(t, 0.1, p.longPositions["BTCUSDT"].Qty())
	assert.Equal(t, 0.05, p.shortPositions["BTCUSDT"].Qty())

	// Closing the SHORT must not touch the LONG.
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideBuy,
		PositionSide: strategy.PositionSideShort,
		Qty:          0.05,
	})
	b.Process(makeBar("BTCUSDT", 49_000, time.Now().Add(2*time.Hour)))

	assert.NotContains(t, p.shortPositions, "BTCUSDT")
	require.Contains(t, p.longPositions, "BTCUSDT", "LONG must remain after closing SHORT")
	assert.Equal(t, 0.1, p.longPositions["BTCUSDT"].Qty())
}

func TestShort_OpenWithoutExistingLong_NoLongerRejected(t *testing.T) {
	// Regression: pre-fix, SideSell with no long position returned
	// "no position to sell" and dropped the order. Fill must succeed now.
	p := NewPortfolio(10_000)
	b := NewSimBroker(0, 0, p, devLog())

	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideSell,
		PositionSide: strategy.PositionSideShort,
		Qty:          0.1,
	})
	fills := b.Process(makeBar("BTCUSDT", 50_000, time.Now()))
	require.Len(t, fills, 1, "short open must not be rejected for missing long")
}

func TestShort_AveragingDown(t *testing.T) {
	// Adding to a SHORT at a higher price (adverse move) should raise avg entry
	// via qty-weighted average. Mirrors the long-averaging math in applyLongFill.
	p := NewPortfolio(10_000)
	b := NewSimBroker(0, 0, p, devLog())

	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideSell,
		PositionSide: strategy.PositionSideShort, Qty: 0.1,
	})
	b.Process(makeBar("BTCUSDT", 50_000, time.Now()))

	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideSell,
		PositionSide: strategy.PositionSideShort, Qty: 0.1,
	})
	b.Process(makeBar("BTCUSDT", 52_000, time.Now().Add(time.Hour)))

	pos := p.shortPositions["BTCUSDT"]
	require.NotNil(t, pos)
	assert.Equal(t, 0.2, pos.Qty())
	// weighted avg: (50000*0.1 + 52000*0.1) / 0.2 = 51000
	assert.InDelta(t, 51_000.0, pos.AvgPrice(), 1e-6)
}

func TestShort_CoverAll_WhenQtyZero(t *testing.T) {
	p := NewPortfolio(10_000)
	b := NewSimBroker(0, 0, p, devLog())

	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideSell,
		PositionSide: strategy.PositionSideShort, Qty: 0.1,
	})
	b.Process(makeBar("BTCUSDT", 50_000, time.Now()))

	// Qty=0 means "close all" for shorts too.
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideBuy,
		PositionSide: strategy.PositionSideShort, Qty: 0,
	})
	b.Process(makeBar("BTCUSDT", 49_000, time.Now().Add(time.Hour)))

	assert.NotContains(t, p.shortPositions, "BTCUSDT")
	require.Len(t, p.Trades, 1)
	assert.InDelta(t, 100.0, p.Trades[0].GrossPnL, 1e-6)
}

// TestShort_OpenAutoSizesWhenQtyZero reproduces a 2026-08-14 finding: executeLong
// auto-sizes a Qty:0 open using available cash, but executeShort's Qty:0 open
// hard-errors ("short open requires explicit qty") instead of mirroring that.
// Every strategy that opens shorts via the Qty:0 convention (e.g. macross's
// openShort, matching its own openLong) silently never opens a real short in
// a backtest — the order is rejected and logged as a WARN, but the JSON/CSV
// report shows no error, just fewer (all long-only) trades, which is easy to
// miss without specifically checking logs.
func TestShort_OpenAutoSizesWhenQtyZero(t *testing.T) {
	p := NewPortfolio(10_000)
	b := NewSimBroker(0, 0, p, devLog())

	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideSell,
		PositionSide: strategy.PositionSideShort, Qty: 0,
	})
	b.Process(makeBar("BTCUSDT", 50_000, time.Now()))

	pos, ok := p.shortPositions["BTCUSDT"]
	require.True(t, ok, "auto-sized short open must actually open a position, not get silently rejected")
	assert.InDelta(t, 10_000*0.99/50_000, pos.Qty(), 1e-9)
}

func TestShort_CoverWithNoPosition_Rejected(t *testing.T) {
	// Buying to cover with no SHORT position should be rejected, not silently
	// create a spurious LONG (the old behavior routed it into the LONG path).
	p := NewPortfolio(10_000)
	b := NewSimBroker(0, 0, p, devLog())

	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideBuy,
		PositionSide: strategy.PositionSideShort, Qty: 0.1,
	})
	fills := b.Process(makeBar("BTCUSDT", 50_000, time.Now()))
	assert.Empty(t, fills, "cover without short must be rejected, not create long")
	assert.NotContains(t, p.longPositions, "BTCUSDT")
	assert.NotContains(t, p.shortPositions, "BTCUSDT")
}

func TestShort_RoundTripCashReconciliation(t *testing.T) {
	// After full round trip with no slippage and matched fees, cash change
	// equals gross PnL minus total fees. Guards against accounting drift.
	p := NewPortfolio(10_000)
	feeRate := 0.0002
	b := NewSimBroker(feeRate, 0, p, devLog())

	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideSell,
		PositionSide: strategy.PositionSideShort, Qty: 0.1,
	})
	b.Process(makeBar("BTCUSDT", 50_000, time.Now()))

	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideBuy,
		PositionSide: strategy.PositionSideShort, Qty: 0.1,
	})
	b.Process(makeBar("BTCUSDT", 48_000, time.Now().Add(time.Hour)))

	// $200 gross profit on 0.1 qty × $2000 drop.
	openFee := 0.1 * 50_000 * feeRate  // $1.00
	closeFee := 0.1 * 48_000 * feeRate // $0.96
	expected := 10_000 + 200 - openFee - closeFee
	assert.InDelta(t, expected, p.Cash(), 1e-6)
	// Sanity: trade record matches.
	require.Len(t, p.Trades, 1)
	assert.InDelta(t, 200-closeFee, p.Trades[0].NetPnL, 1e-6)
	_ = math.Abs // silence import if unused elsewhere
}
