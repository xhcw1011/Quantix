package aistrat

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/strategymock"
)

// ─── Demo: MockBroker with testify/mock ─────────────────────────────────────
//
// Shows the pattern for programming specific expected calls and verifying
// them after the strategy runs. Good for test cases that need to assert
// exact order fields (side, qty, price, type) were submitted.

func TestMockBroker_ContextPlaceOrder(t *testing.T) {
	broker := &strategymock.MockBroker{}
	portfolio := &strategymock.MockPortfolioView{}

	// Program: PlaceOrder with Buy LONG should return "OMS-1"
	broker.On("PlaceOrder", mock.MatchedBy(func(req strategy.OrderRequest) bool {
		return req.Side == strategy.SideBuy &&
			req.PositionSide == strategy.PositionSideLong &&
			req.Qty == 0.1
	})).Return("OMS-1")

	ctx := strategy.NewContext(portfolio, broker, zap.NewNop())
	req := strategy.OrderRequest{
		Symbol:       "ETHUSDT",
		Side:         strategy.SideBuy,
		PositionSide: strategy.PositionSideLong,
		Type:         strategy.OrderMarket,
		Qty:          0.1,
	}

	id := ctx.PlaceOrder(req)
	assert.Equal(t, "OMS-1", id)
	broker.AssertExpectations(t) // verify the order was actually placed
}

func TestMockBroker_UnexpectedCallFails(t *testing.T) {
	broker := &strategymock.MockBroker{}
	broker.On("PlaceOrder", mock.Anything).Return("OMS-1")

	// NOTE: this test intentionally does NOT program CancelOrder, so calling
	// it would fail. Demonstrates that mocks require explicit programming.
	// We don't call CancelOrder here, so this test passes, but if you did,
	// the test would panic with an "unexpected call" message.
}

// ─── Demo: FakeBroker for simple accounting tests ───────────────────────────
//
// Shows the alternative pattern when you just need to count/verify orders
// without asserting specific return values.

func TestFakeBroker_RecordsPlacedOrders(t *testing.T) {
	broker := &strategymock.FakeBroker{}
	portfolio := &strategymock.MockPortfolioView{}
	ctx := strategy.NewContext(portfolio, broker, zap.NewNop())

	// Place 3 orders
	id1 := ctx.PlaceOrder(strategy.OrderRequest{Symbol: "ETHUSDT", Side: strategy.SideBuy, Qty: 0.1})
	id2 := ctx.PlaceOrder(strategy.OrderRequest{Symbol: "ETHUSDT", Side: strategy.SideSell, Qty: 0.05})
	id3 := ctx.PlaceOrder(strategy.OrderRequest{Symbol: "ETHUSDT", Side: strategy.SideBuy, Qty: 0.03})

	// Verify: 3 orders placed, sequential MOCK IDs
	assert.Len(t, broker.Placed, 3)
	assert.Equal(t, "MOCK-0001", id1)
	assert.Equal(t, "MOCK-0002", id2)
	assert.Equal(t, "MOCK-0003", id3)
	assert.Equal(t, 0.1, broker.Placed[0].Qty)
	assert.Equal(t, strategy.SideSell, broker.Placed[1].Side)
}

func TestFakeBroker_CancelOrder(t *testing.T) {
	broker := &strategymock.FakeBroker{}
	portfolio := &strategymock.MockPortfolioView{}
	ctx := strategy.NewContext(portfolio, broker, zap.NewNop())

	err := ctx.CancelOrder("OMS-001")
	assert.NoError(t, err)
	assert.Equal(t, []string{"OMS-001"}, broker.Cancelled)
}

// ─── Demo: MockStagedExitPlacer for TP/SL flow ──────────────────────────────
//
// Exercises the protective-order placement path via Extra["staged_exit"].

func TestMockStagedExit_PlaceStagedTPOrders(t *testing.T) {
	staged := &strategymock.MockStagedExitPlacer{}
	staged.On("PlaceStagedTPOrders",
		"ETHUSDT", "LONG", "SELL",
		2300.0, 0.1,
		mock.AnythingOfType("[]strategy.StagedTP"),
	).Return(true)

	ok := staged.PlaceStagedTPOrders("ETHUSDT", "LONG", "SELL", 2300.0, 0.1, []strategy.StagedTP{
		{Price: 2340, Qty: 0.05},
		{Price: 2360, Qty: 0.05},
	})

	assert.True(t, ok)
	staged.AssertExpectations(t)
}

func TestMockStagedExit_CancelPath(t *testing.T) {
	staged := &strategymock.MockStagedExitPlacer{}
	staged.On("CancelAllProtective", "ETHUSDT", "LONG").Return()

	staged.CancelAllProtective("ETHUSDT", "LONG")
	staged.AssertExpectations(t)
}
