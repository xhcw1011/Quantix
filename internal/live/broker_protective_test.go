package live

import (
	"testing"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/strategy"
)

// submitAndAcceptOrder registers a real order in the OMS (Submit then
// Accept, matching what PlaceOrder does before applyMarketFill ever runs),
// so Fill() calls in these tests exercise the real duplicate-detection path
// instead of failing immediately with "order not found".
func submitAndAcceptOrder(t *testing.T, o *oms.OMS, req strategy.OrderRequest) string {
	t.Helper()
	ord, err := o.Submit(req, "test-strategy")
	if err != nil {
		t.Fatalf("submit order: %v", err)
	}
	if err := o.Accept(ord.ID); err != nil {
		t.Fatalf("accept order: %v", err)
	}
	return ord.ID
}

func TestClosesEntirePosition(t *testing.T) {
	cases := []struct {
		name                string
		preFillQty, filled  float64
		want                bool
	}{
		{"full close: filled matches remaining exactly", 0.01, 0.01, true},
		{"full close: tiny float rounding still counts as full", 0.01, 0.0099999999, true},
		{"partial reduce: filled is half the remaining", 0.01, 0.005, false},
		{"partial reduce: filled is most but not all", 0.01, 0.009, false},
		{"filled exceeds remaining (shouldn't happen, but treat as full)", 0.01, 0.02, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := closesEntirePosition(c.preFillQty, c.filled)
			if got != c.want {
				t.Errorf("closesEntirePosition(%v,%v) = %v, want %v", c.preFillQty, c.filled, got, c.want)
			}
		})
	}
}

// TestApplyMarketFill_PartialReduceKeepsProtectiveOrders reproduces the
// 2026-08-17 finding: a partial reduce (e.g. macross's AsymmetricExit
// confirmed-loss reduce, live-configured at 50%) must NOT cancel the
// remaining position's stop-loss — only a full close should.
func TestApplyMarketFill_PartialReduceKeepsProtectiveOrders(t *testing.T) {
	mock := &mockOrderClient{}
	b, o := newTestLiveBroker(mock)
	b.positions.SeedPosition("BTCUSDT", "SHORT", 0.01, 62974.1)
	key := brokerPosKey("BTCUSDT", "SHORT")
	b.protectiveOrders[key] = protectiveIDs{stopID: "stop-1"}

	// BUY SHORT for only half the position — a partial reduce, not a full close.
	req := strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideShort, Qty: 0.005}
	ordID := submitAndAcceptOrder(t, o, req)
	b.applyMarketFill(ordID, req, "SHORT", exchange.OrderFill{FilledQty: 0.005, AvgPrice: 62900.1})

	if mock.cancelCalls != 0 {
		t.Fatalf("partial reduce must not cancel the stop-loss, but CancelOrder was called %d time(s)", mock.cancelCalls)
	}
	b.protMu.Lock()
	_, stillTracked := b.protectiveOrders[key]
	b.protMu.Unlock()
	if !stillTracked {
		t.Fatal("expected the stop-loss to remain tracked after a partial reduce")
	}
}

// TestApplyMarketFill_FullCloseCancelsProtectiveOrders confirms the existing,
// correct behavior is preserved: a fill that covers the ENTIRE remaining
// position still cancels its protective orders.
func TestApplyMarketFill_FullCloseCancelsProtectiveOrders(t *testing.T) {
	mock := &mockOrderClient{}
	b, o := newTestLiveBroker(mock)
	b.positions.SeedPosition("BTCUSDT", "SHORT", 0.01, 62974.1)
	key := brokerPosKey("BTCUSDT", "SHORT")
	b.protectiveOrders[key] = protectiveIDs{stopID: "stop-1"}

	req := strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideShort, Qty: 0.01}
	ordID := submitAndAcceptOrder(t, o, req)
	b.applyMarketFill(ordID, req, "SHORT", exchange.OrderFill{FilledQty: 0.01, AvgPrice: 62900.1})

	if mock.cancelCalls != 1 {
		t.Fatalf("expected exactly 1 CancelOrder call on a full close, got %d", mock.cancelCalls)
	}
	b.protMu.Lock()
	_, stillTracked := b.protectiveOrders[key]
	b.protMu.Unlock()
	if stillTracked {
		t.Fatal("expected the stop-loss tracking to be removed after a full close")
	}
}

// TestApplyMarketFill_LosingFillRaceDoesNotCancelProtectiveOrders reproduces
// the real 2026-08-17/18 incident directly: the SAME fill gets applied to
// the OMS via TWO independent paths that can race — the WS user-data-stream
// (engine_run.go, calling omsInst.Fill directly) and the broker's own
// REST-poll path (pollMarketOrderFill -> applyMarketFill). The OMS itself
// correctly rejects the second, duplicate Fill() call as an "over-fill" (see
// oms.OMS.Fill's own comment) — but applyMarketFill used to ignore that
// error and barrel ahead anyway, reading preFillQty AFTER the winning path
// had already reduced the position, making a genuine partial reduce (this
// test: 0.004 position, 0.002 filled — half) look like a full close.
func TestApplyMarketFill_LosingFillRaceDoesNotCancelProtectiveOrders(t *testing.T) {
	mock := &mockOrderClient{}
	b, o := newTestLiveBroker(mock)
	b.positions.SeedPosition("BTCUSDT", "SHORT", 0.004, 63511.3)
	key := brokerPosKey("BTCUSDT", "SHORT")
	b.protectiveOrders[key] = protectiveIDs{stopID: "stop-1"}

	req := strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideShort, Qty: 0.002}
	ordID := submitAndAcceptOrder(t, o, req)

	// Simulate the WS user-data-stream path winning the race: it applies the
	// fill (and, in production, reduces b.positions via processFills) before
	// the broker's own poller gets to it.
	if err := o.Fill(ordID, strategy.Fill{
		Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideShort,
		Qty: 0.002, Price: 64169.9,
	}); err != nil {
		t.Fatalf("simulated winning WS fill: %v", err)
	}
	b.positions.ApplyFill(strategy.Fill{
		Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideShort,
		Qty: 0.002, Price: 64169.9,
	}) // mirrors what processFills would have done for the winning fill

	// The broker's own poller now catches up and calls applyMarketFill for
	// the SAME fill — this must be a no-op for protective orders, since it
	// lost the race (the OMS will reject this Fill() as a duplicate/over-fill).
	b.applyMarketFill(ordID, req, "SHORT", exchange.OrderFill{FilledQty: 0.002, AvgPrice: 64169.9})

	if mock.cancelCalls != 0 {
		t.Fatalf("the LOSING side of a fill race must not cancel protective orders, but CancelOrder was called %d time(s)", mock.cancelCalls)
	}
	b.protMu.Lock()
	_, stillTracked := b.protectiveOrders[key]
	b.protMu.Unlock()
	if !stillTracked {
		t.Fatal("expected the stop-loss to remain tracked — the losing path must not touch it")
	}
}

// TestMaybeCancelProtectiveOrdersOnClose_FullClose covers the shared helper
// that engine_run.go's WS user-data-stream handler calls directly (it only
// has an *oms.Order, not a strategy.OrderRequest, and doesn't go through
// applyMarketFill at all) — a full close must still cancel the stop-loss.
func TestMaybeCancelProtectiveOrdersOnClose_FullClose(t *testing.T) {
	mock := &mockOrderClient{}
	b, _ := newTestLiveBroker(mock)
	key := brokerPosKey("BTCUSDT", "SHORT")
	b.protectiveOrders[key] = protectiveIDs{stopID: "stop-1"}

	b.maybeCancelProtectiveOrdersOnClose("BTCUSDT", "SHORT", strategy.SideBuy, 0.01, 0.01)

	if mock.cancelCalls != 1 {
		t.Fatalf("expected exactly 1 CancelOrder call on a full close, got %d", mock.cancelCalls)
	}
	b.protMu.Lock()
	_, stillTracked := b.protectiveOrders[key]
	b.protMu.Unlock()
	if stillTracked {
		t.Fatal("expected the stop-loss tracking to be removed after a full close")
	}
}

// TestMaybeCancelProtectiveOrdersOnClose_PartialReduce covers the same
// shared helper for a partial reduce — must NOT cancel.
func TestMaybeCancelProtectiveOrdersOnClose_PartialReduce(t *testing.T) {
	mock := &mockOrderClient{}
	b, _ := newTestLiveBroker(mock)
	key := brokerPosKey("BTCUSDT", "SHORT")
	b.protectiveOrders[key] = protectiveIDs{stopID: "stop-1"}

	b.maybeCancelProtectiveOrdersOnClose("BTCUSDT", "SHORT", strategy.SideBuy, 0.01, 0.005)

	if mock.cancelCalls != 0 {
		t.Fatalf("partial reduce must not cancel the stop-loss, but CancelOrder was called %d time(s)", mock.cancelCalls)
	}
	b.protMu.Lock()
	_, stillTracked := b.protectiveOrders[key]
	b.protMu.Unlock()
	if !stillTracked {
		t.Fatal("expected the stop-loss to remain tracked after a partial reduce")
	}
}

// TestMaybeCancelProtectiveOrdersOnClose_OpeningSideIgnored covers that an
// opening-direction fill (e.g. BUY on a LONG) is never treated as a close,
// regardless of qty values passed in.
func TestMaybeCancelProtectiveOrdersOnClose_OpeningSideIgnored(t *testing.T) {
	mock := &mockOrderClient{}
	b, _ := newTestLiveBroker(mock)
	key := brokerPosKey("BTCUSDT", "LONG")
	b.protectiveOrders[key] = protectiveIDs{stopID: "stop-1"}

	b.maybeCancelProtectiveOrdersOnClose("BTCUSDT", "LONG", strategy.SideBuy, 0.01, 0.01)

	if mock.cancelCalls != 0 {
		t.Fatalf("an opening-side fill must never cancel protective orders, but CancelOrder was called %d time(s)", mock.cancelCalls)
	}
}
