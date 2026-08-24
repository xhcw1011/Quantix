package live

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
)

// TestFlatten_CancelsOrdersThenClosesBothSides is the regression test for the
// 2026-08-21 circuit-breaker finding: once risk.Manager halts, it rejects
// every order the strategy tries to place forever, so a position left open at
// the moment of the trip can never be re-protected. Flatten is what the
// circuit breaker now calls immediately on trip (see engine_run.go onBar) so
// a halt always means "flat", not "exposed with no stop and no way to place
// one again". This verifies Flatten cancels resting orders before closing
// (so a protective stop can't fill mid-close and cause a double-close), and
// closes every open side (hedge-mode LONG+SHORT, not just one).
func TestFlatten_CancelsOrdersThenClosesBothSides(t *testing.T) {
	mock := &mockOrderClient{
		positions: []exchange.PositionInfo{
			{Symbol: "BTCUSDT", PositionSide: "LONG", Amt: 0.02, EntryPrice: 60000},
			{Symbol: "BTCUSDT", PositionSide: "SHORT", Amt: -0.01, EntryPrice: 61000},
		},
		marketFill: exchange.OrderFill{ExchangeID: "flatten-1", FilledQty: 0.02, AvgPrice: 60500, Status: "filled"},
	}
	broker, _ := newTestLiveBroker(mock)
	e := &Engine{
		cfg:    EngineConfig{StrategyID: "test-flatten"},
		broker: broker,
		log:    zap.NewNop(),
	}

	if err := e.Flatten(context.Background(), "BTCUSDT"); err != nil {
		t.Fatalf("Flatten: %v", err)
	}

	if mock.cancelAllCalls != 1 {
		t.Fatalf("expected exactly 1 CancelAllOpenOrders call, got %d", mock.cancelAllCalls)
	}
	if mock.cancelAllSymbol != "BTCUSDT" {
		t.Fatalf("cancelled orders for %q, want BTCUSDT", mock.cancelAllSymbol)
	}
	if mock.marketCalls != 2 {
		t.Fatalf("expected 2 market close orders (LONG+SHORT), got %d", mock.marketCalls)
	}
}

// TestFlatten_NoOpenPositionsStillCancelsOrders covers the case a resting
// stop/TP is left dangling with no position behind it (e.g. after a manual
// close raced the bot) -- Flatten must still wipe it even though there is
// nothing to close.
func TestFlatten_NoOpenPositionsStillCancelsOrders(t *testing.T) {
	mock := &mockOrderClient{}
	broker, _ := newTestLiveBroker(mock)
	e := &Engine{
		cfg:    EngineConfig{StrategyID: "test-flatten-flat"},
		broker: broker,
		log:    zap.NewNop(),
	}

	if err := e.Flatten(context.Background(), "BTCUSDT"); err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	if mock.cancelAllCalls != 1 {
		t.Fatalf("expected cancel-all even when flat, got %d calls", mock.cancelAllCalls)
	}
	if mock.marketCalls != 0 {
		t.Fatalf("expected no close orders when flat, got %d", mock.marketCalls)
	}
}

// TestFlatten_ClosePositionFailureDoesNotBlockCancel confirms the "best
// effort, first error wins" contract: a failure closing the position is
// still reported, but cancelling resting orders already happened (order
// matters: cancel first, so nothing filled underneath the close attempt).
func TestFlatten_ClosePositionFailureDoesNotBlockCancel(t *testing.T) {
	mock := &mockOrderClient{
		positions: []exchange.PositionInfo{
			{Symbol: "BTCUSDT", PositionSide: "LONG", Amt: 0.02, EntryPrice: 60000},
		},
		marketErr: context.DeadlineExceeded,
	}
	broker, _ := newTestLiveBroker(mock)
	e := &Engine{
		cfg:    EngineConfig{StrategyID: "test-flatten-fail"},
		broker: broker,
		log:    zap.NewNop(),
	}

	err := e.Flatten(context.Background(), "BTCUSDT")
	if err == nil {
		t.Fatal("expected Flatten to surface the close-position failure")
	}
	if mock.cancelAllCalls != 1 {
		t.Fatalf("cancel-all must still run even though close fails, got %d calls", mock.cancelAllCalls)
	}
}
