package live

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// TestApplyUnmatchedFillCash_RoutesToStratFillCh reproduces a 2026-08-17
// finding: exchange-native stop-loss/take-profit fills (and any other
// "unmatched" fill — manual trades, external closes) go through
// applyUnmatchedFillCash, which updates cash/equity/the OMS PositionManager
// but never used to push onto stratFillCh — the only channel that drives
// strategy.OnFill. Without that, a strategy's own hasLong/hasShort belief
// (macross tracks these itself, separate from the OMS) never learns a
// position closed via its own stop-loss — the primary safety backstop is
// capable of leaving the strategy permanently stuck believing a closed
// position is still open until the process restarts.
func TestApplyUnmatchedFillCash_RoutesToStratFillCh(t *testing.T) {
	mock := &mockOrderClient{}
	broker, _ := newTestLiveBroker(mock)
	e := &Engine{
		cfg:         EngineConfig{Leverage: 5},
		broker:      broker,
		positions:   broker.positions,
		log:         zap.NewNop(),
		stratFillCh: make(chan strategy.Fill, 16),
	}

	e.applyUnmatchedFillCash(exchange.OrderFill{
		Symbol: "BTCUSDT", Side: "SELL", PositionSide: "LONG",
		FilledQty: 0.01, AvgPrice: 60000, Fee: 1,
	})

	select {
	case fill := <-e.stratFillCh:
		if fill.Symbol != "BTCUSDT" || fill.Side != strategy.SideSell || fill.PositionSide != strategy.PositionSideLong || fill.Qty != 0.01 {
			t.Fatalf("unexpected fill routed to stratFillCh: %+v", fill)
		}
	default:
		t.Fatal("expected an unmatched fill (e.g. a stop-loss trigger) to be routed to stratFillCh so strategy.OnFill updates its own position belief, got nothing")
	}
}

// TestApplyUnmatchedFillCash_PersistsFillRecord reproduces a 2026-08-17
// real-money incident: the user closed a position via the web UI's "平仓"
// button, which places the order directly on the exchange client (bypassing
// the OMS entirely — see Engine.ClosePosition), so it was never tracked as
// an "order" in the system. The fill was then correctly picked up by the
// unmatched-fill detector — cash/equity/realized PnL updated correctly, a
// Telegram alert fired — but applyUnmatchedFillCash never wrote a `fills` DB
// row for it, so the trade (a real $6.74 realized loss) was completely
// invisible in the web UI's order/fill history despite being fully accounted
// for internally.
func TestApplyUnmatchedFillCash_PersistsFillRecord(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const userID = 90030
	const strategyID = "test-unmatched-fill-persist"

	rawPool, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("raw pool for cleanup: %v", err)
	}
	if _, err := rawPool.Exec(ctx,
		`INSERT INTO users (id, username, email, password_hash) VALUES ($1, $2, $3, 'x')
		 ON CONFLICT (id) DO NOTHING`,
		userID, "test-unmatched-fill", "test-unmatched-fill@example.invalid"); err != nil {
		t.Fatalf("insert throwaway test user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = rawPool.Exec(ctx, "DELETE FROM fills WHERE strategy_id = $1", strategyID)
		_, _ = rawPool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
		rawPool.Close()
	})

	mock := &mockOrderClient{}
	broker, _ := newTestLiveBroker(mock)
	e := &Engine{
		cfg:         EngineConfig{Leverage: 5, UserID: userID, StrategyID: strategyID, Store: store},
		broker:      broker,
		positions:   broker.positions,
		log:         zap.NewNop(),
		stratFillCh: make(chan strategy.Fill, 16),
	}
	e.broker.positions.SeedPosition("BTCUSDT", "SHORT", 0.01, 62792.5) // real entry from the 2026-08-17 incident

	e.applyUnmatchedFillCash(exchange.OrderFill{
		ExchangeID: "1105049620912", Symbol: "BTCUSDT", Side: "BUY", PositionSide: "SHORT",
		FilledQty: 0.01, AvgPrice: 63434.8, Fee: 0.5, IsReduceOnly: true, Status: "FILLED",
	})
	e.dbWg.Wait() // DB persist happens in a goroutine, same as processFills

	fills, err := store.GetFills(ctx, userID, 10, 0, data.RecordFilter{StrategyID: strategyID})
	if err != nil {
		t.Fatalf("GetFills: %v", err)
	}
	if len(fills) != 1 {
		t.Fatalf("expected exactly 1 persisted fill for the manual close, got %d", len(fills))
	}
	got := fills[0]
	if got.ExchangeOrderID != "1105049620912" {
		t.Errorf("exchange_order_id = %q, want %q", got.ExchangeOrderID, "1105049620912")
	}
	if got.RealizedPnL >= 0 {
		t.Errorf("expected a negative realized_pnl (price rose against the short), got %v", got.RealizedPnL)
	}
}
