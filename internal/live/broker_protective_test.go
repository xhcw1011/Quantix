package live

import (
	"testing"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

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
	b, _ := newTestLiveBroker(mock)
	b.positions.SeedPosition("BTCUSDT", "SHORT", 0.01, 62974.1)
	key := brokerPosKey("BTCUSDT", "SHORT")
	b.protectiveOrders[key] = protectiveIDs{stopID: "stop-1"}

	// BUY SHORT for only half the position — a partial reduce, not a full close.
	req := strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideShort}
	b.applyMarketFill("ord-1", req, "SHORT", exchange.OrderFill{FilledQty: 0.005, AvgPrice: 62900.1})

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
	b, _ := newTestLiveBroker(mock)
	b.positions.SeedPosition("BTCUSDT", "SHORT", 0.01, 62974.1)
	key := brokerPosKey("BTCUSDT", "SHORT")
	b.protectiveOrders[key] = protectiveIDs{stopID: "stop-1"}

	req := strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideShort}
	b.applyMarketFill("ord-1", req, "SHORT", exchange.OrderFill{FilledQty: 0.01, AvgPrice: 62900.1})

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
