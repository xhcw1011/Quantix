package macross

import (
	"testing"

	"github.com/Quantix/quantix/internal/strategy"
)

func TestMemStateStore_SaveLoadClear(t *testing.T) {
	s := NewMemStateStore()

	if _, ok := s.Load(); ok {
		t.Fatal("expected no state before any Save")
	}

	if err := s.Save(PendingEntryState{OrderID: "abc"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	st, ok := s.Load()
	if !ok || st.OrderID != "abc" {
		t.Fatalf("expected {OrderID: abc}, got %+v ok=%v", st, ok)
	}

	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, ok := s.Load(); ok {
		t.Fatal("expected no state after Clear")
	}
}

// TestMACross_PendingEntry_PersistsAcrossRestart simulates a process restart
// while a limit entry is still resting: a fresh *MACross sharing the same
// StateStore must cancel the stale order and clear the persisted record
// rather than silently forgetting about it and leaving an orphaned resting
// order the strategy no longer tracks.
func TestMACross_PendingEntry_PersistsAcrossRestart(t *testing.T) {
	store := NewMemStateStore()
	m, ctx, broker := newHedgeMACross(Config{
		EntryOrderType:      "limit",
		EntryLimitOffsetPct: 0.001,
		EntryTimeoutBars:    5,
		TrendFilterMin:      0,
	})
	m.SetStateStore(store)
	feedBarsNoAutoFill(m, ctx, broker, "BTCUSDT", entryBars)

	if !m.pending.Active() {
		t.Fatal("expected a pending entry after the entry bars")
	}
	persisted, ok := store.Load()
	if !ok || persisted.OrderID == "" {
		t.Fatalf("expected the pending entry's order id to be persisted, got %+v ok=%v", persisted, ok)
	}

	// Simulate a restart: a fresh MACross, same StateStore, same broker (as if
	// the exchange still has the order resting).
	m2, ctx2, broker2 := newHedgeMACross(Config{
		EntryOrderType:      "limit",
		EntryLimitOffsetPct: 0.001,
		EntryTimeoutBars:    5,
		TrendFilterMin:      0,
	})
	m2.SetStateStore(store)
	m2.OnBar(ctx2, hedgeBar("BTCUSDT", 0, 100))

	if len(broker2.cancels) != 1 || broker2.cancels[0] != persisted.OrderID {
		t.Fatalf("expected the stale order to be cancelled on restart, got cancels=%v", broker2.cancels)
	}
	if _, ok := store.Load(); ok {
		t.Error("expected the persisted state to be cleared after restart cleanup")
	}
	if m2.pending.Active() {
		t.Error("a fresh instance should not resume tracking the stale order, just clean it up")
	}
}

func TestMACross_PendingEntry_ClearedOnceFillIsDiscovered(t *testing.T) {
	store := NewMemStateStore()
	m, ctx, broker := newHedgeMACross(Config{
		EntryOrderType:      "limit",
		EntryLimitOffsetPct: 0.001,
		EntryTimeoutBars:    5,
		TrendFilterMin:      0,
	})
	m.SetStateStore(store)
	feedBarsNoAutoFill(m, ctx, broker, "BTCUSDT", entryBars)
	req := lastOrder(broker)

	// The fill lands (hasShort flips true via OnFill); the NEXT bar's
	// checkPendingEntry/pendingFilled discovers it and clears both the
	// in-memory pending state and its persisted record.
	m.OnFill(ctx, strategy.Fill{
		Symbol: "BTCUSDT", Side: req.Side, PositionSide: req.PositionSide, Qty: 1, Price: 90,
	})
	m.OnBar(ctx, hedgeBar("BTCUSDT", 8, 90))

	if _, ok := store.Load(); ok {
		t.Error("expected persisted pending-entry state to be cleared once the fill is discovered")
	}
}
