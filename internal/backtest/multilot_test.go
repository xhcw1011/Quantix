package backtest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// ─── Multi-lot FIFO attribution tests ───────────────────────────────────────
//
// Regression coverage for a data-integrity bug: the old single-lot-per-side
// Position/posExcursion model froze EntryTime/EntryMeta/StopDist to whichever
// opening fill came FIRST on a symbol+side, so a second overlapping open
// (e.g. a grid layer added while an earlier layer or leg was still open on
// the same side) had its entry context silently discarded — every later
// partial close on that side inherited the first lot's stale regime tag,
// entry stop, and entry time, and MFE/MAE accumulated across the WHOLE
// continuously-open streak instead of each lot's own true window. These
// tests pin the fixed behavior: each opening fill gets its own lot, and a
// close is attributed to the specific lot(s) it actually consumes.

func TestMultiLot_OverlappingOpens_EachLotKeepsOwnEntryMetaAndTime(t *testing.T) {
	p := NewPortfolio(10_000)
	b := NewSimBroker(0, 0, p, devLog())
	t0 := time.Now()

	// Layer 1 opens under regime=0 (range).
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETHUSDT", Side: strategy.SideBuy, Qty: 1.0,
		Meta: map[string]float64{"regime": 0, "entry_stop": 95},
	})
	b.Process(exchange.Kline{Symbol: "ETHUSDT", High: 101, Low: 99, Close: 100, CloseTime: t0})

	// Layer 2 opens LATER, same side, still no full close in between —
	// under a DIFFERENT regime (strong_trend). Old code would silently drop
	// this lot's meta since e.open[key] already existed.
	t1 := t0.Add(time.Hour)
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETHUSDT", Side: strategy.SideBuy, Qty: 1.0,
		Meta: map[string]float64{"regime": 2, "entry_stop": 108},
	})
	b.Process(exchange.Kline{Symbol: "ETHUSDT", High: 106, Low: 104, Close: 105, CloseTime: t1})

	// Close ONLY layer 1's quantity (1.0) — should consume the OLDEST lot
	// first (FIFO), attributing the close to layer 1's own regime/time/stop,
	// not layer 2's.
	t2 := t1.Add(time.Hour)
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETHUSDT", Side: strategy.SideSell, Qty: 1.0, Reason: "grid_tp",
	})
	b.Process(exchange.Kline{Symbol: "ETHUSDT", High: 111, Low: 109, Close: 110, CloseTime: t2})

	require.Len(t, p.Trades, 1)
	tr := p.Trades[0]
	assert.Equal(t, 0.0, tr.EntryMeta["regime"], "close must attribute to layer 1's regime, not layer 2's")
	assert.InDelta(t, 100.0, tr.EntryPrice, 1e-9, "entry price must be layer 1's own fill price")
	assert.True(t, tr.EntryTime.Equal(t0), "entry time must be layer 1's own open time, not frozen/stale")
	assert.Equal(t, "grid_tp", tr.ExitReason)

	// Layer 2 must still be open (1.0 qty remaining) with its own regime.
	require.Contains(t, p.longPositions, "ETHUSDT")
	assert.InDelta(t, 1.0, p.longPositions["ETHUSDT"].Qty(), 1e-9)
}

func TestMultiLot_SecondCloseAttributesToSecondLot(t *testing.T) {
	p := NewPortfolio(10_000)
	b := NewSimBroker(0, 0, p, devLog())
	t0 := time.Now()

	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETHUSDT", Side: strategy.SideBuy, Qty: 1.0,
		Meta: map[string]float64{"regime": 0},
	})
	b.Process(exchange.Kline{Symbol: "ETHUSDT", High: 101, Low: 99, Close: 100, CloseTime: t0})

	t1 := t0.Add(time.Hour)
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETHUSDT", Side: strategy.SideBuy, Qty: 1.0,
		Meta: map[string]float64{"regime": 2},
	})
	b.Process(exchange.Kline{Symbol: "ETHUSDT", High: 106, Low: 104, Close: 105, CloseTime: t1})

	// First close consumes layer 1 entirely.
	t2 := t1.Add(time.Hour)
	b.PlaceOrder(strategy.OrderRequest{Symbol: "ETHUSDT", Side: strategy.SideSell, Qty: 1.0, Reason: "grid_tp"})
	b.Process(exchange.Kline{Symbol: "ETHUSDT", High: 111, Low: 109, Close: 110, CloseTime: t2})

	// Second close consumes layer 2 — must report regime=2, not stale regime=0.
	t3 := t2.Add(time.Hour)
	b.PlaceOrder(strategy.OrderRequest{Symbol: "ETHUSDT", Side: strategy.SideSell, Qty: 1.0, Reason: "trailing"})
	b.Process(exchange.Kline{Symbol: "ETHUSDT", High: 116, Low: 114, Close: 115, CloseTime: t3})

	require.Len(t, p.Trades, 2)
	assert.Equal(t, 2.0, p.Trades[1].EntryMeta["regime"], "second close must attribute to layer 2's own regime")
	assert.True(t, p.Trades[1].EntryTime.Equal(t1), "second close must carry layer 2's own entry time")
	assert.NotContains(t, p.longPositions, "ETHUSDT", "both lots closed → symbol untracked")
}

func TestMultiLot_CloseSpanningTwoLots_ProducesTwoTrades(t *testing.T) {
	p := NewPortfolio(10_000)
	b := NewSimBroker(0, 0, p, devLog())
	t0 := time.Now()

	b.PlaceOrder(strategy.OrderRequest{Symbol: "ETH", Side: strategy.SideBuy, Qty: 1.0})
	b.Process(exchange.Kline{Symbol: "ETH", High: 101, Low: 99, Close: 100, CloseTime: t0})

	t1 := t0.Add(time.Hour)
	b.PlaceOrder(strategy.OrderRequest{Symbol: "ETH", Side: strategy.SideBuy, Qty: 1.0})
	b.Process(exchange.Kline{Symbol: "ETH", High: 111, Low: 109, Close: 110, CloseTime: t1})

	// Close 1.5 — spans all of lot 1 (1.0) plus half of lot 2 (0.5).
	t2 := t1.Add(time.Hour)
	b.PlaceOrder(strategy.OrderRequest{Symbol: "ETH", Side: strategy.SideSell, Qty: 1.5})
	b.Process(exchange.Kline{Symbol: "ETH", High: 121, Low: 119, Close: 120, CloseTime: t2})

	require.Len(t, p.Trades, 2, "a close spanning two lots must produce one Trade per lot fragment")
	assert.InDelta(t, 1.0, p.Trades[0].Qty, 1e-9)
	assert.InDelta(t, 100.0, p.Trades[0].EntryPrice, 1e-9)
	assert.InDelta(t, 0.5, p.Trades[1].Qty, 1e-9)
	assert.InDelta(t, 110.0, p.Trades[1].EntryPrice, 1e-9)

	// Remainder of lot 2 (0.5) still open.
	require.Contains(t, p.longPositions, "ETH")
	assert.InDelta(t, 0.5, p.longPositions["ETH"].Qty(), 1e-9)
}

func TestMultiLot_MFEMAE_PerLot_DoesNotBleedAcrossLots(t *testing.T) {
	p := NewPortfolio(10_000)
	b := NewSimBroker(0, 0, p, devLog())
	t0 := time.Now()

	// Lot 1 opens and sees a big favorable excursion (price to 200) before lot 2 opens.
	b.PlaceOrder(strategy.OrderRequest{Symbol: "ETH", Side: strategy.SideBuy, Qty: 1.0})
	b.Process(exchange.Kline{Symbol: "ETH", High: 101, Low: 99, Close: 100, CloseTime: t0})
	p.UpdateExcursions(exchange.Kline{Symbol: "ETH", High: 200, Low: 100, Close: 150, CloseTime: t0})

	// Lot 2 opens later, at 150 — must NOT inherit lot 1's already-seen high of 200.
	t1 := t0.Add(time.Hour)
	b.PlaceOrder(strategy.OrderRequest{Symbol: "ETH", Side: strategy.SideBuy, Qty: 1.0})
	b.Process(exchange.Kline{Symbol: "ETH", High: 151, Low: 149, Close: 150, CloseTime: t1})

	// Bar after lot 2 opens: price ticks up only slightly to 155.
	t2 := t1.Add(time.Hour)
	p.UpdateExcursions(exchange.Kline{Symbol: "ETH", High: 155, Low: 150, Close: 155, CloseTime: t2})

	// Close lot 2 only — its own MFE should reflect its own entry (150→155),
	// not lot 1's accumulated 100→200 range.
	t3 := t2.Add(time.Hour)
	b.PlaceOrder(strategy.OrderRequest{Symbol: "ETH", Side: strategy.SideSell, Qty: 1.0})
	b.Process(exchange.Kline{Symbol: "ETH", High: 156, Low: 154, Close: 155, CloseTime: t3})

	// Only lot 1 remains open, so this is the close of lot 1 (FIFO) — wait,
	// FIFO consumes oldest first, so this actually closes lot 1. Re-derive:
	// lot 1 entry=100, its own excursion by now includes bars through t3
	// (100→200 from the first UpdateExcursions call, then flat afterwards
	// since lot 1's tracking also folds in the later bars). Assert MFE is
	// large (reflecting the 100→200 move it actually lived through).
	require.Len(t, p.Trades, 1)
	assert.Greater(t, p.Trades[0].MFEPct, 50.0, "lot 1 lived through the 100->200 excursion")

	// Now close lot 2 (the remaining open lot) and confirm its MFE is small —
	// it never saw the 200 high because it opened after that bar.
	t4 := t3.Add(time.Hour)
	b.PlaceOrder(strategy.OrderRequest{Symbol: "ETH", Side: strategy.SideSell, Qty: 1.0})
	b.Process(exchange.Kline{Symbol: "ETH", High: 156, Low: 154, Close: 155, CloseTime: t4})

	require.Len(t, p.Trades, 2)
	assert.Less(t, p.Trades[1].MFEPct, 10.0, "lot 2 must not inherit lot 1's pre-existing 100->200 excursion")
}

func TestMultiLot_StopDist_PerLot(t *testing.T) {
	p := NewPortfolio(10_000)
	b := NewSimBroker(0, 0, p, devLog())
	t0 := time.Now()

	// Lot 1: tight stop (R=5).
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETH", Side: strategy.SideBuy, Qty: 1.0,
		Meta: map[string]float64{"entry_stop": 95},
	})
	b.Process(exchange.Kline{Symbol: "ETH", High: 101, Low: 99, Close: 100, CloseTime: t0})

	// Lot 2: much wider stop (R=50).
	t1 := t0.Add(time.Hour)
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETH", Side: strategy.SideBuy, Qty: 1.0,
		Meta: map[string]float64{"entry_stop": 50},
	})
	b.Process(exchange.Kline{Symbol: "ETH", High: 101, Low: 99, Close: 100, CloseTime: t1})

	// Close lot 1 (FIFO) — its R basis must be 5, not lot 2's 50.
	// UpdateExcursions mirrors what Engine.Run calls every bar before Process,
	// so lot 1's FavPrice reflects this bar's high (111) before it closes.
	t2 := t1.Add(time.Hour)
	closeBar := exchange.Kline{Symbol: "ETH", High: 111, Low: 109, Close: 110, CloseTime: t2}
	p.UpdateExcursions(closeBar)
	b.PlaceOrder(strategy.OrderRequest{Symbol: "ETH", Side: strategy.SideSell, Qty: 1.0})
	b.Process(closeBar)

	require.Len(t, p.Trades, 1)
	// MFE% = (111-100)/100*100 = 11% (bar high, folded in by UpdateExcursions),
	// R distance = 5% of entry (stop=95) -> MFER = 11/5 = 2.2
	assert.InDelta(t, 2.2, p.Trades[0].MFER, 0.01, "R basis must come from lot 1's own stop, not lot 2's")
}

func TestMultiLot_Short_OverlappingOpens_FIFO(t *testing.T) {
	p := NewPortfolio(10_000)
	b := NewSimBroker(0, 0, p, devLog())
	t0 := time.Now()

	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETH", Side: strategy.SideSell, PositionSide: strategy.PositionSideShort, Qty: 1.0,
		Meta: map[string]float64{"regime": 0},
	})
	b.Process(exchange.Kline{Symbol: "ETH", High: 101, Low: 99, Close: 100, CloseTime: t0})

	t1 := t0.Add(time.Hour)
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETH", Side: strategy.SideSell, PositionSide: strategy.PositionSideShort, Qty: 1.0,
		Meta: map[string]float64{"regime": 2},
	})
	b.Process(exchange.Kline{Symbol: "ETH", High: 96, Low: 94, Close: 95, CloseTime: t1})

	// Cover 1.0 — consumes lot 1 (FIFO), entry=100.
	t2 := t1.Add(time.Hour)
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "ETH", Side: strategy.SideBuy, PositionSide: strategy.PositionSideShort, Qty: 1.0,
	})
	b.Process(exchange.Kline{Symbol: "ETH", High: 91, Low: 89, Close: 90, CloseTime: t2})

	require.Len(t, p.Trades, 1)
	assert.Equal(t, 0.0, p.Trades[0].EntryMeta["regime"])
	assert.InDelta(t, 100.0, p.Trades[0].EntryPrice, 1e-9)
	assert.InDelta(t, 10.0, p.Trades[0].GrossPnL, 1e-9, "short covering 1.0 @ 90 vs entry 100 = 10 profit")

	require.Contains(t, p.shortPositions, "ETH")
	assert.InDelta(t, 1.0, p.shortPositions["ETH"].Qty(), 1e-9)
}
