package live

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
)

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
