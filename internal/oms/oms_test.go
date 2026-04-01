package oms

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
)

func newTestOMS() *OMS {
	log, _ := zap.NewDevelopment()
	return New(ModePaper, log)
}

// newTestOMSWithDrain creates an OMS with a background goroutine that drains
// both channels, preventing backpressure from blocking during concurrent tests.
// Call the returned cancel func to stop the drain goroutine.
func newTestOMSWithDrain() (*OMS, context.CancelFunc) {
	o := newTestOMS()
	ctx, cancel := context.WithCancel(context.Background())
	o.SetContext(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-o.fillsCh:
			case <-o.ordersCh:
			}
		}
	}()
	return o, cancel
}

func buyReq(sym string) strategy.OrderRequest {
	return strategy.OrderRequest{Symbol: sym, Side: strategy.SideBuy, Type: strategy.OrderMarket, Qty: 1}
}

// ─── State machine tests ──────────────────────────────────────────────────────

func TestOrder_ValidTransitions(t *testing.T) {
	ord := &Order{Status: StatusPending}
	assert.NoError(t, ord.TransitionTo(StatusOpen))
	assert.NoError(t, ord.TransitionTo(StatusFilled))
	assert.True(t, ord.IsTerminal())
}

func TestOrder_InvalidTransition(t *testing.T) {
	ord := &Order{Status: StatusFilled}
	err := ord.TransitionTo(StatusCancelled)
	assert.ErrorIs(t, err, ErrInvalidTransition)
}

func TestOrder_PendingToRejected(t *testing.T) {
	ord := &Order{Status: StatusPending}
	assert.NoError(t, ord.TransitionTo(StatusRejected))
	assert.True(t, ord.IsTerminal())
}

func TestOrder_PartialFill(t *testing.T) {
	ord := &Order{Status: StatusOpen}
	assert.NoError(t, ord.TransitionTo(StatusPartial))
	assert.NoError(t, ord.TransitionTo(StatusFilled))
}

// ─── OMS lifecycle tests ──────────────────────────────────────────────────────

func TestOMS_SubmitAndAccept(t *testing.T) {
	o := newTestOMS()
	ord, err := o.Submit(buyReq("BTCUSDT"), "test")
	require.NoError(t, err)
	assert.Equal(t, StatusPending, ord.Status)

	require.NoError(t, o.Accept(ord.ID))
	got := o.Get(ord.ID)
	require.NotNil(t, got)
	assert.Equal(t, StatusOpen, got.Status)
}

func TestOMS_Fill_FullFill(t *testing.T) {
	o := newTestOMS()
	ord, _ := o.Submit(buyReq("BTCUSDT"), "test")
	o.Accept(ord.ID) //nolint:errcheck

	fill := strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, Qty: 1, Price: 50000, Fee: 50}
	require.NoError(t, o.Fill(ord.ID, fill))

	got := o.Get(ord.ID)
	assert.Equal(t, StatusFilled, got.Status)
	assert.Equal(t, 50000.0, got.AvgFillPrice)
	assert.Equal(t, 50.0, got.Commission)
}

func TestOMS_Fill_PublishesFillEvent(t *testing.T) {
	o := newTestOMS()
	ord, _ := o.Submit(buyReq("BTCUSDT"), "test")
	o.Accept(ord.ID) //nolint:errcheck

	fill := strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, Qty: 1, Price: 50000, Fee: 50}
	o.Fill(ord.ID, fill) //nolint:errcheck

	select {
	case event := <-o.Fills():
		assert.Equal(t, ord.ID, event.Order.ID)
		assert.Equal(t, fill.Price, event.Fill.Price)
	default:
		t.Fatal("expected a fill event on the channel")
	}
}

func TestOMS_Reject(t *testing.T) {
	o := newTestOMS()
	ord, _ := o.Submit(buyReq("BTCUSDT"), "test")
	require.NoError(t, o.Reject(ord.ID, "risk limit"))

	got := o.Get(ord.ID)
	assert.Equal(t, StatusRejected, got.Status)
	assert.Equal(t, "risk limit", got.RejectReason)
}

func TestOMS_Cancel(t *testing.T) {
	o := newTestOMS()
	ord, _ := o.Submit(buyReq("BTCUSDT"), "test")
	o.Accept(ord.ID) //nolint:errcheck
	require.NoError(t, o.Cancel(ord.ID))
	assert.Equal(t, StatusCancelled, o.Get(ord.ID).Status)
}

func TestOMS_OpenOrders(t *testing.T) {
	o := newTestOMS()
	ord1, _ := o.Submit(buyReq("BTCUSDT"), "s1")
	ord2, _ := o.Submit(buyReq("ETHUSDT"), "s1")
	o.Accept(ord1.ID) //nolint:errcheck
	o.Accept(ord2.ID) //nolint:errcheck

	// Fill ord1
	o.Fill(ord1.ID, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, Qty: 1, Price: 100}) //nolint:errcheck

	open := o.OpenOrders()
	assert.Len(t, open, 1)
	assert.Equal(t, "ETHUSDT", open[0].Symbol)
}

// ─── Concurrency safety ───────────────────────────────────────────────────────

func TestOMS_ConcurrentSubmit(t *testing.T) {
	o, cancel := newTestOMSWithDrain()
	defer cancel()
	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ord, err := o.Submit(buyReq("BTCUSDT"), "concurrent")
			if err == nil {
				o.Accept(ord.ID) //nolint:errcheck
			}
		}()
	}
	wg.Wait()
	assert.Len(t, o.OpenOrders(), n)
}

func TestOMS_PruneTerminal(t *testing.T) {
	log, _ := zap.NewDevelopment()
	o := New(ModePaper, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o.SetContext(ctx)

	// 1. Submit, accept, and fill the first order (BTCUSDT).
	ord, err := o.Submit(strategy.OrderRequest{Symbol: "BTCUSDT", Side: strategy.SideBuy, Type: strategy.OrderMarket, Qty: 1.0}, "test")
	require.NoError(t, err)
	require.NoError(t, o.Accept(ord.ID))
	require.NoError(t, o.Fill(ord.ID, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, Qty: 1.0, Price: 50000}))

	// 2. Drain the fills channel so it doesn't block.
	select {
	case <-o.Fills():
	default:
	}

	// 3. Backdate the filled order's UpdatedAt to 1 hour ago.
	o.mu.Lock()
	o.orders[ord.ID].UpdatedAt = time.Now().Add(-1 * time.Hour)
	o.mu.Unlock()

	// 4. Submit a second order (ETHUSDT) and accept it — leave it OPEN (non-terminal).
	ord2, err := o.Submit(strategy.OrderRequest{Symbol: "ETHUSDT", Side: strategy.SideBuy, Type: strategy.OrderMarket, Qty: 2.0}, "test")
	require.NoError(t, err)
	require.NoError(t, o.Accept(ord2.ID))

	// 5. Prune with a 30-minute cutoff — only the 1h-old filled order should be removed.
	pruned := o.PruneTerminal(30 * time.Minute)

	assert.Equal(t, 1, pruned)
	assert.Nil(t, o.Get(ord.ID), "filled order should have been pruned")
	assert.NotNil(t, o.Get(ord2.ID), "open order should still be present")
}
