package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newTestPool connects to the local dev Postgres (same DSN convention used by
// the project's other DB-backed tests, e.g. internal/data/guardian_state_test.go).
// Skips instead of failing when no local DB is reachable (CI without Postgres).
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgresql://quantix:quantix_secret@localhost:5432/quantix")
	if err != nil {
		t.Skipf("no local postgres available: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no local postgres available: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestComputeKPIs_IsolatedPerUser reproduces the 2026-08-06 finding: engine_id
// ("SYMBOL-INTERVAL-STRATEGY") collides whenever two different users run the
// identical symbol+interval+strategy combo. Without a user_id filter,
// computeKPIs blends both users' fills into one experiment's KPI report and
// would auto-write a won/lost decision to the shared experiments row based on
// the contaminated numbers.
func TestComputeKPIs_IsolatedPerUser(t *testing.T) {
	db := newTestPool(t)
	ctx := context.Background()
	const engineID = "BTCUSDT-15m-macross" // deliberately identical for both users
	const userA, userB = 90003, 90004      // high IDs unlikely to collide with real users
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)
	mid := start.Add(24 * time.Hour)

	t.Cleanup(func() {
		db.Exec(ctx, `DELETE FROM fills WHERE user_id IN ($1,$2)`, userA, userB) //nolint:errcheck
		db.Exec(ctx, `DELETE FROM users WHERE id IN ($1,$2)`, userA, userB)      //nolint:errcheck
	})

	// fills.user_id has a FK to users(id) -- seed throwaway users rather than
	// touch any real account's rows.
	for _, u := range []int{userA, userB} {
		if _, err := db.Exec(ctx, `
			INSERT INTO users (id, username, email, password_hash)
			VALUES ($1, $2, $2 || '@test.local', 'x')
			ON CONFLICT (id) DO NOTHING`,
			u, fmt.Sprintf("test-user-%d", u)); err != nil {
			t.Fatalf("seed user=%d: %v", u, err)
		}
	}

	insertFill := func(userID int, pnl float64) {
		if _, err := db.Exec(ctx, `
			INSERT INTO fills (user_id, strategy_id, symbol, side, qty, price, fee, realized_pnl, filled_at)
			VALUES ($1, $2, 'BTCUSDT', 'SELL', 1, 100, 0, $3, $4)`,
			userID, engineID, pnl, mid); err != nil {
			t.Fatalf("insert fill user=%d: %v", userID, err)
		}
	}
	insertFill(userA, 100)  // userA: a real win
	insertFill(userB, -500) // userB: a real loss, must not leak into userA's report

	kA, err := computeKPIs(ctx, db, userA, engineID, start, end)
	if err != nil {
		t.Fatalf("computeKPIs userA: %v", err)
	}
	if kA.GrossPnL != 100 {
		t.Fatalf("userA's KPI contaminated by userB's fills: got GrossPnL=%v, want 100", kA.GrossPnL)
	}

	kB, err := computeKPIs(ctx, db, userB, engineID, start, end)
	if err != nil {
		t.Fatalf("computeKPIs userB: %v", err)
	}
	if kB.GrossPnL != -500 {
		t.Fatalf("userB's KPI contaminated by userA's fills: got GrossPnL=%v, want -500", kB.GrossPnL)
	}
}
