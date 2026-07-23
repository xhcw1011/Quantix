package live

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/oms"
)

// closePosMock is a mockOrderClient that also answers position queries
// (PositionQuerier) and records which exchange order IDs were cancelled — enough
// to verify Engine.ClosePosition cancels the position's paired protective stop.
type closePosMock struct {
	*mockOrderClient
	positions    []exchange.PositionInfo
	positionsSeq [][]exchange.PositionInfo // if set, GetPositions returns the next slice per call
	posCall      int
	cancelledIDs []string
}

func (c *closePosMock) GetPositions(context.Context) ([]exchange.PositionInfo, error) {
	if c.positionsSeq != nil {
		i := c.posCall
		if i >= len(c.positionsSeq) {
			i = len(c.positionsSeq) - 1
		}
		c.posCall++
		return c.positionsSeq[i], nil
	}
	return c.positions, nil
}

func (c *closePosMock) CancelOrder(_ context.Context, _, id string) error {
	c.mu.Lock()
	c.cancelledIDs = append(c.cancelledIDs, id)
	c.mu.Unlock()
	return c.cancelErr
}

// The web "close position" button routes through Engine.ClosePosition, which fires
// a market close directly at the exchange client — bypassing the broker's normal
// closing-fill flow where cancelProtectiveOrders runs. Without an explicit cancel
// here, the resting stop-loss is orphaned on the exchange after the position closes.
func TestEngineClosePositionCancelsProtectiveStop(t *testing.T) {
	log := zap.NewNop()
	mock := &closePosMock{
		mockOrderClient: &mockOrderClient{
			marketFill: exchange.OrderFill{ExchangeID: "close-1", FilledQty: 0.043, AvgPrice: 64000, Status: "filled"},
		},
		positions: []exchange.PositionInfo{
			{Symbol: "BTCUSDT", PositionSide: "LONG", Amt: 0.043, EntryPrice: 64884},
		},
	}
	o := oms.New(oms.ModeLive, log)
	pm := oms.NewPositionManager()
	b := New(mock, o, pm, nil, log)
	b.SetEngineCtx(context.Background())
	b.protectiveOrders[brokerPosKey("BTCUSDT", "LONG")] = protectiveIDs{stopID: "stop-abc"}

	e := &Engine{broker: b, log: log, positions: pm}
	_, _, err := e.ClosePosition(context.Background(), "BTCUSDT", "LONG")
	require.NoError(t, err)

	assert.Contains(t, mock.cancelledIDs, "stop-abc",
		"web close-position must cancel the position's paired stop-loss (else it orphans on the exchange)")
}

// After a full close, the OMS position manager must no longer show the position —
// otherwise the UI keeps displaying the (already-closed) position and a repeat
// close fails with "no open position". The exchange reports the position on the
// first query (for the close) and flat on the reconcile query.
func TestEngineClosePositionRemovesFromPositionManager(t *testing.T) {
	log := zap.NewNop()
	mock := &closePosMock{
		mockOrderClient: &mockOrderClient{
			marketFill: exchange.OrderFill{ExchangeID: "close-1", FilledQty: 0.011, Status: "filled"},
		},
		positionsSeq: [][]exchange.PositionInfo{
			{{Symbol: "BTCUSDT", PositionSide: "SHORT", Amt: -0.011, EntryPrice: 66300}}, // for the close
			{}, // reconcile: exchange now flat
		},
	}
	o := oms.New(oms.ModeLive, log)
	pm := oms.NewPositionManager()
	pm.SeedPosition("BTCUSDT", "SHORT", 0.011, 66300) // bot believes it holds the short
	b := New(mock, o, pm, nil, log)
	b.SetEngineCtx(context.Background())

	e := &Engine{broker: b, log: log, positions: pm}
	_, _, err := e.ClosePosition(context.Background(), "BTCUSDT", "SHORT")
	require.NoError(t, err)

	_, ok := pm.ShortPosition("BTCUSDT")
	require.False(t, ok, "a fully-closed short must be removed from the position manager")
}

// TestClosePosition_PersistsOrderRecord reproduces a 2026-08-17 real-money
// incident: closing a position via the web UI's "平仓" button places the
// market order directly on the exchange client (Engine.ClosePosition),
// bypassing the OMS entirely — so unlike every other order path, it never
// generates an oms.OrderEvent, and persistOrderEvent (which only fires from
// that event stream) never runs. The trade genuinely executed on the
// exchange (confirmed independently) but left no row in the `orders` table
// at all, on top of the separately-fixed missing `fills` row.
func TestClosePosition_PersistsOrderRecord(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const userID = 90040
	const credentialID = 90040
	const strategyID = "test-close-position-persist"

	rawPool, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("raw pool for cleanup: %v", err)
	}
	if _, err := rawPool.Exec(ctx,
		`INSERT INTO users (id, username, email, password_hash) VALUES ($1, $2, $3, 'x')
		 ON CONFLICT (id) DO NOTHING`,
		userID, "test-close-position", "test-close-position@example.invalid"); err != nil {
		t.Fatalf("insert throwaway test user: %v", err)
	}
	if _, err := rawPool.Exec(ctx,
		`INSERT INTO exchange_credentials (id, user_id, exchange, label, api_key, api_secret)
		 VALUES ($1, $2, 'binance', 'test-close-position', 'x', 'x')
		 ON CONFLICT (id) DO NOTHING`,
		credentialID, userID); err != nil {
		t.Fatalf("insert throwaway test credential: %v", err)
	}
	t.Cleanup(func() {
		_, _ = rawPool.Exec(ctx, "DELETE FROM orders WHERE strategy_id = $1", strategyID)
		_, _ = rawPool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
		rawPool.Close()
	})

	mock := &mockOrderClient{
		positions: []exchange.PositionInfo{
			{Symbol: "BTCUSDT", PositionSide: "SHORT", Amt: -0.01, EntryPrice: 62792.5},
		},
		marketFill: exchange.OrderFill{ExchangeID: "1105049620912", FilledQty: 0.01, AvgPrice: 63434.8, Fee: 0.5, Status: "filled"},
	}
	broker, _ := newTestLiveBroker(mock)
	e := &Engine{
		cfg:    EngineConfig{UserID: userID, CredentialID: credentialID, StrategyID: strategyID, Store: store},
		broker: broker,
		log:    zap.NewNop(),
	}

	if _, _, err := e.ClosePosition(ctx, "BTCUSDT", "SHORT"); err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}

	orders, err := store.GetOrders(ctx, userID, 10, 0, data.RecordFilter{StrategyID: strategyID})
	if err != nil {
		t.Fatalf("GetOrders: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("expected exactly 1 persisted order for the manual close, got %d", len(orders))
	}
	got := orders[0]
	if got.Status != "FILLED" {
		t.Errorf("status = %q, want FILLED", got.Status)
	}
	if got.FilledQuantity != 0.01 || got.AvgFillPrice != 63434.8 {
		t.Errorf("filled_quantity/avg_fill_price = %v/%v, want 0.01/63434.8", got.FilledQuantity, got.AvgFillPrice)
	}
	if got.ExchangeID != "1105049620912" {
		t.Errorf("exchange_id = %q, want 1105049620912", got.ExchangeID)
	}
}

// Closing a stale phantom (bot shows a position the exchange no longer has) must
// clear the display and succeed — not error with "no open position", which would
// leave the user stuck with an uncloseable ghost.
func TestEngineClosePositionClearsStalePhantom(t *testing.T) {
	log := zap.NewNop()
	mock := &closePosMock{
		mockOrderClient: &mockOrderClient{},
		positions:       []exchange.PositionInfo{}, // exchange is FLAT
	}
	pm := oms.NewPositionManager()
	pm.SeedPosition("BTCUSDT", "SHORT", 0.011, 66300) // stale: bot shows it, exchange doesn't
	b := New(mock, oms.New(oms.ModeLive, log), pm, nil, log)
	b.SetEngineCtx(context.Background())

	e := &Engine{broker: b, log: log, positions: pm}
	_, _, err := e.ClosePosition(context.Background(), "BTCUSDT", "SHORT")
	require.NoError(t, err, "closing a stale phantom should clear it and succeed")

	_, ok := pm.ShortPosition("BTCUSDT")
	require.False(t, ok, "stale phantom short must be cleared")
}
