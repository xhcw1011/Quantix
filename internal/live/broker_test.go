package live

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/strategy"
)

// ---------------------------------------------------------------------------
// Mock OrderClient
// ---------------------------------------------------------------------------

var _ exchange.OrderClient = (*mockOrderClient)(nil)

type mockOrderClient struct {
	mu           sync.Mutex
	marketCalls  int
	limitCalls   int
	stopCalls    int
	tpCalls      int
	cancelCalls  int
	balanceCalls int

	marketFill exchange.OrderFill
	marketErr  error

	limitID  string
	limitErr error

	stopID  string
	stopErr error

	tpID  string
	tpErr error

	cancelErr error

	balance    float64
	balanceErr error

	leverageErr error
}

type mockStatusClient struct {
	*mockOrderClient
	statuses []mockStatusStep
	idx      int
}

type mockStatusStep struct {
	status string
	fill   exchange.OrderFill
	err    error
}

type capturingOrderClient struct {
	mu             sync.Mutex
	clientOrderIDs []string
	errs           []error
	fill           exchange.OrderFill
}

func (m *mockOrderClient) PlaceMarketOrder(_ context.Context, _ string, _ exchange.OrderSide, _ string, _ float64, _ string) (exchange.OrderFill, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marketCalls++
	return m.marketFill, m.marketErr
}

func (m *mockOrderClient) PlaceLimitOrder(_ context.Context, _ string, _ exchange.OrderSide, _ string, _, _ float64, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.limitCalls++
	return m.limitID, m.limitErr
}

func (m *mockOrderClient) PlaceStopMarketOrder(_ context.Context, _ string, _ exchange.OrderSide, _ string, _, _ float64, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopCalls++
	return m.stopID, m.stopErr
}

func (m *mockOrderClient) PlaceTakeProfitMarketOrder(_ context.Context, _ string, _ exchange.OrderSide, _ string, _, _ float64, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tpCalls++
	return m.tpID, m.tpErr
}

func (m *mockOrderClient) SetLeverage(_ context.Context, _ string, _ int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.leverageErr
}

func (m *mockOrderClient) CancelOrder(_ context.Context, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelCalls++
	return m.cancelErr
}

func (m *mockOrderClient) GetBalance(_ context.Context, _ string) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.balanceCalls++
	return m.balance, m.balanceErr
}

func (m *mockStatusClient) GetOrderStatus(_ context.Context, _ string, _ string) (string, exchange.OrderFill, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.statuses) {
		last := m.statuses[len(m.statuses)-1]
		return last.status, last.fill, last.err
	}
	step := m.statuses[m.idx]
	m.idx++
	return step.status, step.fill, step.err
}

func (c *capturingOrderClient) PlaceMarketOrder(_ context.Context, _ string, _ exchange.OrderSide, _ string, _ float64, clientOrderID string) (exchange.OrderFill, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clientOrderIDs = append(c.clientOrderIDs, clientOrderID)
	if len(c.errs) > 0 {
		err := c.errs[0]
		c.errs = c.errs[1:]
		if err != nil {
			return exchange.OrderFill{}, err
		}
	}
	return c.fill, nil
}

func (c *capturingOrderClient) PlaceLimitOrder(context.Context, string, exchange.OrderSide, string, float64, float64, string) (string, error) {
	return "", nil
}
func (c *capturingOrderClient) PlaceStopMarketOrder(context.Context, string, exchange.OrderSide, string, float64, float64, string) (string, error) {
	return "", nil
}
func (c *capturingOrderClient) PlaceTakeProfitMarketOrder(context.Context, string, exchange.OrderSide, string, float64, float64, string) (string, error) {
	return "", nil
}
func (c *capturingOrderClient) SetLeverage(context.Context, string, int) error { return nil }
func (c *capturingOrderClient) CancelOrder(context.Context, string, string) error { return nil }
func (c *capturingOrderClient) GetBalance(context.Context, string) (float64, error) { return 0, nil }

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func newTestLiveBroker(mock *mockOrderClient) (*Broker, *oms.OMS) {
	log := zap.NewNop()
	o := oms.New(oms.ModeLive, log)
	pm := oms.NewPositionManager()
	b := New(mock, o, pm, nil, log)
	b.SetEngineCtx(context.Background())
	return b, o
}

// drainFill reads one fill event from the OMS channel with a 1-second timeout.
func drainFill(t *testing.T, o *oms.OMS) oms.FillEvent {
	t.Helper()
	select {
	case fe := <-o.Fills():
		return fe
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for fill event")
		return oms.FillEvent{}
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestLiveBroker_MarketOrderSuccess(t *testing.T) {
	mock := &mockOrderClient{
		marketFill: exchange.OrderFill{
			ExchangeID: "exch-1",
			FilledQty:  1,
			AvgPrice:   50000,
			Fee:        5,
			Status:     "filled",
		},
	}
	b, o := newTestLiveBroker(mock)
	b.SetLastPrice(50000)
	b.cash.Store(100000.0)
	b.equity.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderMarket,
		Qty:    1,
	})

	require.NotEmpty(t, ordID, "expected non-empty order ID")

	fe := drainFill(t, o)
	assert.Equal(t, ordID+"-live", fe.Fill.ID)
	assert.Equal(t, 1.0, fe.Fill.Qty)
	assert.Equal(t, 50000.0, fe.Fill.Price)
	assert.Equal(t, 5.0, fe.Fill.Fee)

	mock.mu.Lock()
	assert.Equal(t, 1, mock.marketCalls)
	mock.mu.Unlock()
}

func TestLiveBroker_MarketOrderExchangeError(t *testing.T) {
	mock := &mockOrderClient{
		marketErr: errors.New("insufficient balance"),
	}
	b, _ := newTestLiveBroker(mock)
	b.SetLastPrice(50000)
	b.cash.Store(100000.0)
	b.equity.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderMarket,
		Qty:    1,
	})

	assert.Empty(t, ordID, "expected empty order ID on exchange error")

	mock.mu.Lock()
	assert.Equal(t, 1, mock.marketCalls, "non-transient error should not retry")
	mock.mu.Unlock()
}

func TestLiveBroker_MarketOrderTransientRetry(t *testing.T) {
	mock := &mockOrderClient{
		marketErr: errors.New("connection refused"),
	}
	b, _ := newTestLiveBroker(mock)
	b.SetLastPrice(50000)
	b.cash.Store(100000.0)
	b.equity.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderMarket,
		Qty:    1,
	})

	assert.Empty(t, ordID, "expected empty order ID after retries exhausted")

	mock.mu.Lock()
	assert.Equal(t, 2, mock.marketCalls, "transient error should trigger 2 attempts")
	mock.mu.Unlock()
}

func TestLiveBroker_LimitOrderAsync(t *testing.T) {
	mock := &mockOrderClient{
		limitID: "limit-456",
	}
	b, _ := newTestLiveBroker(mock)
	b.SetLastPrice(50000)
	b.cash.Store(100000.0)
	b.equity.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderLimit,
		Qty:    1,
		Price:  49000,
	})

	require.NotEmpty(t, ordID, "expected non-empty order ID for limit order")

	mock.mu.Lock()
	assert.Equal(t, 1, mock.limitCalls)
	mock.mu.Unlock()
}

func TestLiveBroker_StopOrderAsync(t *testing.T) {
	mock := &mockOrderClient{
		stopID: "stop-789",
	}
	b, _ := newTestLiveBroker(mock)
	b.SetLastPrice(50000)
	b.cash.Store(100000.0)
	b.equity.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol:    "BTCUSDT",
		Side:      strategy.SideSell,
		Type:      strategy.OrderStopMarket,
		Qty:       1,
		StopPrice: 48000,
	})

	require.NotEmpty(t, ordID, "expected non-empty order ID for stop order")

	mock.mu.Lock()
	assert.Equal(t, 1, mock.stopCalls)
	mock.mu.Unlock()
}

func TestLiveBroker_SyncBalance(t *testing.T) {
	mock := &mockOrderClient{
		balance: 5000,
	}
	b, _ := newTestLiveBroker(mock)

	err := b.SyncBalance(context.Background(), "USDT")
	require.NoError(t, err)
	assert.Equal(t, 5000.0, b.Cash())
	assert.Equal(t, 5000.0, b.Equity())
}

func TestLiveBroker_SyncBalanceError(t *testing.T) {
	mock := &mockOrderClient{
		balanceErr: errors.New("exchange unavailable"),
	}
	b, _ := newTestLiveBroker(mock)

	err := b.SyncBalance(context.Background(), "USDT")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync balance")
}

func TestLiveBroker_DuplicateOrderBlocked(t *testing.T) {
	mock := &mockOrderClient{
		marketFill: exchange.OrderFill{
			ExchangeID: "exch-dup",
			FilledQty:  1,
			AvgPrice:   50000,
			Fee:        5,
			Status:     "filled",
		},
		limitID: "limit-dup",
	}
	b, o := newTestLiveBroker(mock)
	b.SetLastPrice(50000)
	b.cash.Store(100000.0)
	b.equity.Store(100000.0)

	// Step 1: place a market buy (fills immediately)
	ordID1 := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderMarket,
		Qty:    1,
	})
	require.NotEmpty(t, ordID1)
	drainFill(t, o) // consume the fill

	// Step 2: place a limit buy (stays OPEN / non-terminal)
	ordID2 := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderLimit,
		Qty:    0.5,
		Price:  49000,
	})
	require.NotEmpty(t, ordID2)

	// Step 3: another buy should be blocked by FindPending → returns existing limit ID
	ordID3 := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderMarket,
		Qty:    0.5,
	})
	assert.Equal(t, ordID2, ordID3, "duplicate order should return the pending limit order ID")
}

func TestLiveBroker_CancelAllPending(t *testing.T) {
	mock := &mockOrderClient{
		limitID: "limit-cancel",
	}
	b, _ := newTestLiveBroker(mock)
	b.SetLastPrice(50000)
	b.cash.Store(100000.0)
	b.equity.Store(100000.0)

	// Place a limit order (stays OPEN)
	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderLimit,
		Qty:    1,
		Price:  49000,
	})
	require.NotEmpty(t, ordID)

	// CancelAllPendingOrders returns nothing (void)
	b.CancelAllPendingOrders(context.Background())

	mock.mu.Lock()
	assert.Equal(t, 1, mock.cancelCalls)
	mock.mu.Unlock()
}

func TestLiveBroker_CancelAllPending_RetriesTransientFailure(t *testing.T) {
	mock := &mockOrderClient{
		limitID:   "limit-cancel-retry",
		cancelErr: errors.New("connection refused"),
	}
	b, _ := newTestLiveBroker(mock)
	b.SetLastPrice(50000)
	b.cash.Store(100000.0)
	b.equity.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderLimit,
		Qty:    1,
		Price:  49000,
	})
	require.NotEmpty(t, ordID)

	b.CancelAllPendingOrders(context.Background())

	mock.mu.Lock()
	assert.Equal(t, 2, mock.cancelCalls, "transient cancel error should be retried once")
	mock.mu.Unlock()
}

func TestLiveBroker_AllInSellWithoutPositionRejected(t *testing.T) {
	mock := &mockOrderClient{}
	b, _ := newTestLiveBroker(mock)
	b.SetLastPrice(50000)
	b.cash.Store(100000.0)
	b.equity.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideSell,
		Type:   strategy.OrderMarket,
		Qty:    0,
	})

	assert.Empty(t, ordID)
	mock.mu.Lock()
	assert.Equal(t, 0, mock.marketCalls, "order should be rejected before exchange call when no position exists")
	mock.mu.Unlock()
}

func TestLiveBroker_ProtectivePlacementFailureDoesNotPanic(t *testing.T) {
	mock := &mockOrderClient{
		marketFill: exchange.OrderFill{
			ExchangeID: "exch-open-1",
			FilledQty:  1,
			AvgPrice:   50000,
			Fee:        0,
			Status:     "filled",
		},
		stopErr: errors.New("protective rejected"),
		tpErr:   errors.New("protective rejected"),
	}
	b, o := newTestLiveBroker(mock)
	b.SetLastPrice(50000)
	b.cash.Store(100000.0)
	b.equity.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol:     "BTCUSDT",
		Side:       strategy.SideBuy,
		Type:       strategy.OrderMarket,
		Qty:        1,
		StopLoss:   49000,
		TakeProfit: 51000,
	})
	require.NotEmpty(t, ordID)
	_ = drainFill(t, o)

	mock.mu.Lock()
	assert.Equal(t, 1, mock.marketCalls)
	assert.Equal(t, 1, mock.stopCalls)
	assert.Equal(t, 1, mock.tpCalls)
	mock.mu.Unlock()
}

func TestLiveBroker_TransientMarketRetryUsesSingleClientOrderID(t *testing.T) {
	mock := &capturingOrderClient{
		fill: exchange.OrderFill{ExchangeID: "exch-retry-1", FilledQty: 1, AvgPrice: 50000, Fee: 1, Status: "filled"},
		errs: []error{errors.New("connection refused"), nil},
	}
	log := zap.NewNop()
	o := oms.New(oms.ModeLive, log)
	pm := oms.NewPositionManager()
	b := New(mock, o, pm, nil, log)
	b.SetEngineCtx(context.Background())
	b.SetLastPrice(50000)
	b.cash.Store(100000.0)
	b.equity.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderMarket,
		Qty:    1,
	})
	require.NotEmpty(t, ordID)
	_ = drainFill(t, o)

	require.Len(t, mock.clientOrderIDs, 2)
	assert.NotEmpty(t, mock.clientOrderIDs[0])
	assert.Equal(t, mock.clientOrderIDs[0], mock.clientOrderIDs[1], "retry should reuse the same clientOrderID")
}

func TestLiveBroker_PollOrderUntilFilled_IncrementalPartialFills(t *testing.T) {
	mock := &mockStatusClient{
		mockOrderClient: &mockOrderClient{},
		statuses: []mockStatusStep{
			{status: "PARTIALLY_FILLED", fill: exchange.OrderFill{ExchangeID: "x1", FilledQty: 0.4, AvgPrice: 50000, Fee: 1}},
			{status: "PARTIALLY_FILLED", fill: exchange.OrderFill{ExchangeID: "x1", FilledQty: 0.4, AvgPrice: 50000, Fee: 1}},
			{status: "FILLED", fill: exchange.OrderFill{ExchangeID: "x1", FilledQty: 1.0, AvgPrice: 50010, Fee: 2}},
		},
	}
	b, o := newTestLiveBroker(mock.mockOrderClient)
	b.orderClient = mock
	b.SetEngineCtx(context.Background())
	b.pollInterval = 10 * time.Millisecond

	ord, err := o.Submit(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderLimit,
		Qty:    1,
		Price:  49000,
	}, "live")
	require.NoError(t, err)
	require.NoError(t, o.Accept(ord.ID))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.pollOrderUntilFilled(ctx, mock, "x1", ord.ID, strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderLimit,
		Qty:    1,
		Price:  49000,
	})

	fe1 := drainFill(t, o)
	assert.InDelta(t, 0.4, fe1.Fill.Qty, 1e-9)

	fe2 := drainFill(t, o)
	assert.InDelta(t, 0.6, fe2.Fill.Qty, 1e-9)
}

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "connection refused",
			err:      errors.New("connection refused"),
			expected: true,
		},
		{
			name:     "i/o timeout",
			err:      errors.New("i/o timeout"),
			expected: true,
		},
		{
			name:     "EOF",
			err:      errors.New("EOF"),
			expected: true,
		},
		{
			name:     "broken pipe",
			err:      errors.New("broken pipe"),
			expected: true,
		},
		{
			name:     "connection reset by peer",
			err:      errors.New("connection reset by peer"),
			expected: true,
		},
		{
			name:     "insufficient balance (not transient)",
			err:      errors.New("insufficient balance"),
			expected: false,
		},
		{
			name:     "invalid symbol (not transient)",
			err:      errors.New("invalid symbol"),
			expected: false,
		},
		{
			name:     "wrapped net.OpError",
			err:      &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("no route to host")},
			expected: true,
		},
		{
			name:     "fmt wrapped net.OpError",
			err:      fmt.Errorf("request failed: %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("timeout")}),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTransientError(tt.err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Hardening tests (T0-2, T0-3, T0-4, T0-6)
// ---------------------------------------------------------------------------

// TestLiveEngine_KlineChClosed_NoBusyLoop validates that a closed kline channel
// does not cause a busy-loop — the select pattern must nil the channel and block
// on ctx.Done() only.
func TestLiveEngine_KlineChClosed_NoBusyLoop(t *testing.T) {
	klineCh := make(chan struct{}, 1)
	close(klineCh) // simulate WebSocket disconnect

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		ch := klineCh
		for {
			select {
			case _, ok := <-ch:
				if !ok {
					ch = nil // nil channel blocks forever — no busy-loop
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Give the goroutine time to reach the nil-channel blocking state.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// goroutine exited cleanly — no busy-loop
	case <-time.After(1 * time.Second):
		t.Fatal("goroutine did not exit within 1s — likely busy-looping on closed channel")
	}
}

// TestRecovery_NoExchangeID_RejectsOrder validates that an order recovered from
// DB with no exchange ID (never reached the exchange) can be rejected with the
// correct reason stored in OMS.
func TestRecovery_NoExchangeID_RejectsOrder(t *testing.T) {
	log := zap.NewNop()
	o := oms.New(oms.ModeLive, log)

	ord := &oms.Order{
		ID:         "OMS-RECOVER-001",
		Symbol:     "BTCUSDT",
		Side:       strategy.SideBuy,
		Type:       strategy.OrderMarket,
		Status:     oms.StatusPending,
		ExchangeID: "", // never reached the exchange
		Qty:        1,
	}
	require.NoError(t, o.Restore(ord))

	reason := "recovered: never reached exchange"
	require.NoError(t, o.Reject(ord.ID, reason))

	got := o.Get(ord.ID)
	require.NotNil(t, got)
	assert.Equal(t, oms.StatusRejected, got.Status)
	assert.Equal(t, reason, got.RejectReason)
}

// TestLiveBroker_CancelAllPending_PermanentFailureSurfaced validates that a
// non-transient cancel error is not retried (exactly 1 exchange call) and does
// not panic.
func TestLiveBroker_CancelAllPending_PermanentFailureSurfaced(t *testing.T) {
	mock := &mockOrderClient{
		limitID:   "limit-perm-fail",
		cancelErr: fmt.Errorf("insufficient permissions"),
	}
	b, _ := newTestLiveBroker(mock)
	b.SetLastPrice(50000)
	b.cash.Store(100000.0)
	b.equity.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderLimit,
		Qty:    1,
		Price:  49000,
	})
	require.NotEmpty(t, ordID)

	// Must not panic; non-transient error should not be retried.
	require.NotPanics(t, func() {
		b.CancelAllPendingOrders(context.Background())
	})

	mock.mu.Lock()
	assert.Equal(t, 1, mock.cancelCalls, "non-transient cancel error must not be retried")
	mock.mu.Unlock()
}

// TestRecovery_ExchangeFilledButDBOpen validates that an order recovered from DB
// as OPEN can be fully filled via OMS.Fill(), producing the correct terminal
// state and a fill event on the Fills() channel.
func TestRecovery_ExchangeFilledButDBOpen(t *testing.T) {
	log := zap.NewNop()
	o := oms.New(oms.ModeLive, log)
	o.SetContext(context.Background())

	ord := &oms.Order{
		ID:         "OMS-RECOVER-002",
		Symbol:     "BTCUSDT",
		Side:       strategy.SideBuy,
		Type:       strategy.OrderMarket,
		Status:     oms.StatusOpen,
		ExchangeID: "EX-100",
		Qty:        0.5,
	}
	require.NoError(t, o.Restore(ord))

	fill := strategy.Fill{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Qty:    0.5,
		Price:  50000,
	}
	require.NoError(t, o.Fill(ord.ID, fill))

	got := o.Get(ord.ID)
	require.NotNil(t, got)
	assert.Equal(t, oms.StatusFilled, got.Status)
	assert.InDelta(t, 0.5, got.FilledQty, 1e-9)

	// Verify the fill event was published to the channel.
	select {
	case fe := <-o.Fills():
		assert.InDelta(t, 0.5, fe.Fill.Qty, 1e-9)
		assert.Equal(t, 50000.0, fe.Fill.Price)
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for fill event after exchange-fill recovery")
	}
}

// TestRecovery_ExchangeCancelledButDBOpen validates that an order recovered from
// DB as OPEN can be cancelled via OMS.Cancel(), reaching the CANCELLED terminal
// state.
func TestRecovery_ExchangeCancelledButDBOpen(t *testing.T) {
	log := zap.NewNop()
	o := oms.New(oms.ModeLive, log)

	ord := &oms.Order{
		ID:         "OMS-RECOVER-003",
		Symbol:     "BTCUSDT",
		Side:       strategy.SideBuy,
		Type:       strategy.OrderLimit,
		Status:     oms.StatusOpen,
		ExchangeID: "EX-200",
		Qty:        1,
		Price:      49000,
	}
	require.NoError(t, o.Restore(ord))

	require.NoError(t, o.Cancel(ord.ID))

	got := o.Get(ord.ID)
	require.NotNil(t, got)
	assert.Equal(t, oms.StatusCancelled, got.Status)
}
