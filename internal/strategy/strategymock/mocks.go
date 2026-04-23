// Package strategymock provides testify-based mock implementations of
// the strategy package interfaces. Used across test files to simulate
// broker, portfolio, and exchange-side protective order placement.
//
// Usage:
//
//	broker := &strategymock.MockBroker{}
//	broker.On("PlaceOrder", mock.MatchedBy(func(req strategy.OrderRequest) bool {
//		return req.Side == strategy.SideBuy
//	})).Return("OMS-000001")
//	ctx := strategy.NewContext(portfolio, broker, zap.NewNop())
//	// ... exercise strategy ...
//	broker.AssertExpectations(t)
package strategymock

import (
	"github.com/stretchr/testify/mock"

	"github.com/Quantix/quantix/internal/strategy"
)

// ─── Broker ────────────────────────────────────────────────────────────────

// MockBroker implements strategy.Broker. Use `On(...).Return(...)` to program
// expected calls. Unexpected calls fail with mock assertion errors.
type MockBroker struct {
	mock.Mock
}

func (m *MockBroker) PlaceOrder(req strategy.OrderRequest) string {
	args := m.Called(req)
	return args.String(0)
}

func (m *MockBroker) CancelOrder(orderID string) error {
	args := m.Called(orderID)
	return args.Error(0)
}

// Ensure interface compliance at compile time.
var _ strategy.Broker = (*MockBroker)(nil)

// ─── PortfolioView ─────────────────────────────────────────────────────────

// MockPortfolioView implements strategy.PortfolioView.
type MockPortfolioView struct {
	mock.Mock
}

func (m *MockPortfolioView) Cash() float64 {
	args := m.Called()
	return args.Get(0).(float64)
}

func (m *MockPortfolioView) Position(symbol string) (qty float64, avgPrice float64, ok bool) {
	args := m.Called(symbol)
	return args.Get(0).(float64), args.Get(1).(float64), args.Bool(2)
}

func (m *MockPortfolioView) Equity(prices map[string]float64) float64 {
	args := m.Called(prices)
	return args.Get(0).(float64)
}

var _ strategy.PortfolioView = (*MockPortfolioView)(nil)

// ─── StagedExitPlacer ──────────────────────────────────────────────────────

// MockStagedExitPlacer implements strategy.StagedExitPlacer.
// Injected into Context.Extra["staged_exit"] for tests that exercise
// exchange-side TP/SL placement.
type MockStagedExitPlacer struct {
	mock.Mock
}

func (m *MockStagedExitPlacer) PlaceStagedTPOrders(symbol, posSide, closeSide string, stopPrice, totalQty float64, tps []strategy.StagedTP) bool {
	args := m.Called(symbol, posSide, closeSide, stopPrice, totalQty, tps)
	return args.Bool(0)
}

func (m *MockStagedExitPlacer) PlaceExchangeSL(symbol, posSide, closeSide string, qty, stopPrice float64) bool {
	args := m.Called(symbol, posSide, closeSide, qty, stopPrice)
	return args.Bool(0)
}

func (m *MockStagedExitPlacer) ReplaceSLOrder(symbol, posSide, closeSide string, remainQty, newStopPrice float64) bool {
	args := m.Called(symbol, posSide, closeSide, remainQty, newStopPrice)
	return args.Bool(0)
}

func (m *MockStagedExitPlacer) CancelExchangeSL(symbol, posSide string) {
	m.Called(symbol, posSide)
}

func (m *MockStagedExitPlacer) CancelAllProtective(symbol, posSide string) {
	m.Called(symbol, posSide)
}

var _ strategy.StagedExitPlacer = (*MockStagedExitPlacer)(nil)

// ─── Fake (simple counter-based alternatives) ──────────────────────────────

// FakeBroker is a simpler alternative to MockBroker for tests that only
// need to track calls without programming specific return values.
// Orders are recorded in the Placed slice; PlaceOrder returns sequential IDs.
type FakeBroker struct {
	Placed    []strategy.OrderRequest
	Cancelled []string
	NextID    int // incremented on each PlaceOrder; use this to generate IDs like "OMS-0001"
}

func (f *FakeBroker) PlaceOrder(req strategy.OrderRequest) string {
	f.Placed = append(f.Placed, req)
	f.NextID++
	return mockOrderID(f.NextID)
}

func (f *FakeBroker) CancelOrder(orderID string) error {
	f.Cancelled = append(f.Cancelled, orderID)
	return nil
}

func mockOrderID(n int) string {
	return "MOCK-" + pad4(n)
}

func pad4(n int) string {
	s := ""
	for i := 0; i < 4; i++ {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

var _ strategy.Broker = (*FakeBroker)(nil)
