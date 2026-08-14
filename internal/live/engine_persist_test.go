package live

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/strategy"
)

const testDSN = "postgresql://quantix:quantix_secret@localhost:5432/quantix"

// newTestStore connects to the local dev Postgres (same DSN convention used by
// the project's other DB-backed tests, e.g. internal/data/guardian_state_test.go).
// Skips instead of failing when no local DB is reachable.
func newTestStore(t *testing.T) *data.Store {
	t.Helper()
	log := zap.NewNop()
	s, err := data.New(context.Background(), testDSN, log)
	if err != nil {
		t.Skipf("no local postgres available: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestPersistOrderEvent_LaterEventWins reproduces the 2026-08-13 finding: two
// events for the SAME order (client_order_id) — first "NEW"/OPEN right after
// submit, then "FILLED" moments later once the exchange confirms — used to
// fire as independent, unordered goroutines. Whichever write reached Postgres
// last won, so a slow "NEW" write could clobber an already-persisted "FILLED"
// write, leaving the order stuck showing OPEN/filled_qty=0 forever despite
// having actually filled. persistOrderEvent is now synchronous; calling it
// twice in the CORRECT temporal order must leave the DB reflecting the
// second (FILLED) call, not the first.
func TestPersistOrderEvent_LaterEventWins(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const userID = 90020
	clientID := "test-order-race-2026-08-13"
	rawPool, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("raw pool for cleanup: %v", err)
	}
	// orders.user_id and orders.credential_id both have FKs — insert throwaway
	// rows so the upsert doesn't fail the constraints. credential cascades away
	// when the user is deleted (ON DELETE CASCADE), so only orders + users need
	// explicit cleanup.
	const credentialID = 90020
	if _, err := rawPool.Exec(ctx,
		`INSERT INTO users (id, username, email, password_hash) VALUES ($1, $2, $3, 'x')
		 ON CONFLICT (id) DO NOTHING`,
		userID, "test-persist-order-race", "test-persist-order-race@example.invalid"); err != nil {
		t.Fatalf("insert throwaway test user: %v", err)
	}
	if _, err := rawPool.Exec(ctx,
		`INSERT INTO exchange_credentials (id, user_id, exchange, label, api_key, api_secret)
		 VALUES ($1, $2, 'binance', 'test-persist-order-race', 'x', 'x')
		 ON CONFLICT (id) DO NOTHING`,
		credentialID, userID); err != nil {
		t.Fatalf("insert throwaway test credential: %v", err)
	}
	t.Cleanup(func() {
		_, _ = rawPool.Exec(ctx, "DELETE FROM orders WHERE client_order_id = $1", clientID)
		_, _ = rawPool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
		rawPool.Close()
	})

	e := &Engine{
		cfg: EngineConfig{Store: store, UserID: userID, CredentialID: credentialID, StrategyID: "test-persist-order"},
		log: zap.NewNop(),
	}

	// First event: order just submitted, exchange hasn't confirmed yet.
	e.persistOrderEvent(oms.OrderEvent{Order: oms.Order{
		ClientOrderID: clientID,
		Symbol:        "BTCUSDT",
		Side:          strategy.SideBuy,
		PositionSide:  strategy.PositionSideShort,
		Type:          strategy.OrderMarket,
		Status:        oms.StatusOpen,
		Qty:           0.002,
		FilledQty:     0,
		CreatedAt:     time.Now(),
	}})

	// Second event: exchange fill confirmed moments later.
	e.persistOrderEvent(oms.OrderEvent{Order: oms.Order{
		ClientOrderID: clientID,
		Symbol:        "BTCUSDT",
		Side:          strategy.SideBuy,
		PositionSide:  strategy.PositionSideShort,
		Type:          strategy.OrderMarket,
		Status:        oms.StatusFilled,
		Qty:           0.002,
		FilledQty:     0.002,
		AvgFillPrice:  63462.4,
		CreatedAt:     time.Now(),
	}})

	orders, err := store.GetOrders(ctx, userID, 10, 0, data.RecordFilter{ClientOrderID: clientID})
	if err != nil {
		t.Fatalf("GetOrders: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("expected exactly 1 order row, got %d", len(orders))
	}
	got := orders[0]
	if got.Status != string(oms.StatusFilled) {
		t.Errorf("status = %q, want %q (the later FILLED event must win, not the earlier NEW event)", got.Status, oms.StatusFilled)
	}
	if got.FilledQuantity != 0.002 {
		t.Errorf("filled_quantity = %v, want 0.002", got.FilledQuantity)
	}
}
