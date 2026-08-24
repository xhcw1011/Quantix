package guardian

import (
	"testing"
	"time"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
)

// fakeLiveSource satisfies the livePositionSource subset of position.Syncer that
// the guardian reads from ctx.Extra["position_syncer"].
type fakeLiveSource struct{ long, short *position.StrategyPosition }

func (f fakeLiveSource) GetLong() *position.StrategyPosition  { return f.long }
func (f fakeLiveSource) GetShort() *position.StrategyPosition { return f.short }

func entryCfg() ProtectionConfig {
	return ProtectionConfig{StopMode: StopPct, StopValue: 0.03}
}

func shortPos(symbol string, qty, entry float64) *position.StrategyPosition {
	p := &position.StrategyPosition{}
	p.Symbol = symbol
	p.Side = "SHORT"
	p.Qty = qty
	p.EntryPrice = entry
	return p
}

// TestGuardian_EntryAdoptsExistingInsteadOfReopening is the regression test for
// "顺便帮我开仓 每次重启就重新市价开仓": on restart the EntryGuardian is re-created
// from stored config and unconditionally re-placed the market entry. It must
// instead detect the already-open position (via the syncer) and adopt it.
func TestGuardian_EntryAdoptsExistingInsteadOfReopening(t *testing.T) {
	g := NewEntryGuardian("ETHUSDT", SideShort, 0.1, entryCfg(), 14, zap.NewNop())
	b := &gBroker{}
	// Portfolio also reports the short so resyncPosition doesn't retire it; the
	// entry decision itself keys on the syncer.
	ctx := strategy.NewContext(&gPortfolio{qty: -0.1, avg: 2000}, b, zap.NewNop())
	ctx.Extra["position_syncer"] = fakeLiveSource{short: shortPos("ETHUSDT", 0.1, 2000)}

	g.OnBar(ctx, exchange.Kline{Open: 2000, High: 2005, Low: 1995, Close: 2000})

	if len(b.orders) != 0 {
		t.Fatalf("expected NO entry order (position already open on restart), got %d: %+v", len(b.orders), b.orders)
	}
	if g.Prot() == nil {
		t.Fatal("expected protection armed from the adopted position")
	}
	if g.Prot().Side != SideShort {
		t.Fatalf("expected adopted SHORT protection, got %s", g.Prot().Side)
	}
}

// TestGuardian_AdoptsRecoveredPositionWithoutEntryPrice is the regression test
// for the real-money incident: a position recovered from the exchange via the
// margin-ratio query has no entry price, and the old code rejected it and tried
// to re-open. It must adopt (protect from the current price), not re-open.
func TestGuardian_AdoptsRecoveredPositionWithoutEntryPrice(t *testing.T) {
	g := NewEntryGuardian("ETHUSDT", SideShort, 0.1, entryCfg(), 14, zap.NewNop())
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{qty: -0.1, avg: 2000}, b, zap.NewNop())
	recovered := &position.StrategyPosition{}
	recovered.Symbol, recovered.Side, recovered.Qty = "ETHUSDT", "SHORT", 0.1 // NO EntryPrice
	ctx.Extra["position_syncer"] = fakeLiveSource{short: recovered}

	g.OnBar(ctx, exchange.Kline{Open: 2000, High: 2005, Low: 1995, Close: 2000, Warmup: false})

	if len(b.orders) != 0 {
		t.Fatalf("expected NO re-open — must adopt the recovered position, got %d: %+v", len(b.orders), b.orders)
	}
	if g.Prot() == nil || g.Prot().Entry != 2000 {
		t.Fatalf("expected adopted SHORT protection with entry=2000 (fallback to current price), got %+v", g.Prot())
	}
}

// TestGuardian_WarmupBarsDontTriggerExit is the regression test for the real-money
// incident: after adopting, the guardian ran its exit/trail logic on historical
// warmup-replay prices and false-triggered a stop. Warmup bars must not exit.
func TestGuardian_WarmupBarsDontTriggerExit(t *testing.T) {
	cfg := ProtectionConfig{StopMode: StopPct, StopValue: 0.03}
	// Short @2000, stop = 2060. A warmup bar spiking to 2100 would "hit" it.
	g := NewGuardian("ETHUSDT", NewProtection(SideShort, 2000, 0.1, cfg, 0), 14, zap.NewNop())
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{qty: -0.1, avg: 2000}, b, zap.NewNop())

	g.OnBar(ctx, exchange.Kline{Open: 2100, High: 2100, Low: 2100, Close: 2100, Warmup: true})
	if len(b.orders) != 0 {
		t.Fatalf("expected NO close on a warmup replay bar, got %d: %+v", len(b.orders), b.orders)
	}
	// A live bar at the same adverse price legitimately closes.
	g.OnBar(ctx, exchange.Kline{Open: 2100, High: 2100, Low: 2100, Close: 2100, Warmup: false})
	if len(b.orders) != 1 {
		t.Fatalf("expected 1 close on the live bar, got %d", len(b.orders))
	}
}

// TestGuardian_UpdateParamsLive verifies live "修改参数" on a running guardian:
// the stop recomputes from the new value without stopping/rearming.
func TestGuardian_UpdateParamsLive(t *testing.T) {
	cfg := ProtectionConfig{StopMode: StopPct, StopValue: 0.03}
	g := NewGuardian("ETHUSDT", NewProtection(SideLong, 100, 1, cfg, 0), 14, zap.NewNop())
	ctx := strategy.NewContext(&gPortfolio{qty: 1, avg: 100}, &gBroker{}, zap.NewNop())

	if err := g.UpdateParams(ctx, map[string]any{"StopMode": "pct", "StopValue": 0.02}); err != nil {
		t.Fatal(err)
	}
	if g.Prot().Stop != 98 {
		t.Fatalf("stop = %v, want 98 after tightening 3%%→2%%", g.Prot().Stop)
	}
	// Add a take-profit live.
	if err := g.UpdateParams(ctx, map[string]any{"StopMode": "pct", "StopValue": 0.02, "TPValue": 0.10}); err != nil {
		t.Fatal(err)
	}
	if g.Prot().TPPrice() != 110 {
		t.Fatalf("tp = %v, want 110 after adding 10%% TP", g.Prot().TPPrice())
	}
}

// TestGuardian_EntryWaitsForLiveBar is the regression test for "选了帮我开仓但没下单":
// the entry was placed on the first (warmup) bar, where the broker suppresses the
// order and entryPlaced latches — so it never really opened. The open must wait
// for a live bar (Warmup=false).
func TestGuardian_EntryWaitsForLiveBar(t *testing.T) {
	g := NewEntryGuardian("ETHUSDT", SideShort, 0.1, entryCfg(), 14, zap.NewNop())
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{}, b, zap.NewNop())

	// Warmup replay bar → must NOT place (would be suppressed + latched).
	g.OnBar(ctx, exchange.Kline{Open: 2000, High: 2005, Low: 1995, Close: 2000, Warmup: true})
	if len(b.orders) != 0 {
		t.Fatalf("expected NO entry during warmup, got %d: %+v", len(b.orders), b.orders)
	}
	// First live bar → now it opens.
	g.OnBar(ctx, exchange.Kline{Open: 2000, High: 2005, Low: 1995, Close: 2000, Warmup: false})
	if len(b.orders) != 1 {
		t.Fatalf("expected 1 entry order on the first live bar, got %d", len(b.orders))
	}
	if b.orders[0].PositionSide != strategy.PositionSideShort {
		t.Fatalf("expected OpenShort entry, got %+v", b.orders[0])
	}
}

// TestGuardian_EntryOpensPromptlyOnTickWhenLive verifies the instant-open path:
// once the engine signals it's live (past warmup), the entry is placed on the
// next real-time tick rather than waiting for the next closed bar.
func TestGuardian_EntryOpensPromptlyOnTickWhenLive(t *testing.T) {
	g := NewEntryGuardian("ETHUSDT", SideShort, 0.1, entryCfg(), 14, zap.NewNop())
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{}, b, zap.NewNop())

	// Not live yet → a tick must NOT open (order would be suppressed + latched).
	g.OnTick(ctx, 2000)
	if len(b.orders) != 0 {
		t.Fatalf("expected NO entry before engine is live, got %d", len(b.orders))
	}
	// Engine goes live → next tick opens immediately.
	ctx.Extra["engine_live"] = true
	g.OnTick(ctx, 2000)
	if len(b.orders) != 1 {
		t.Fatalf("expected 1 entry on the first live tick, got %d", len(b.orders))
	}
	if b.orders[0].Type != strategy.OrderMarket {
		t.Fatalf("expected a MARKET entry by default, got %s", b.orders[0].Type)
	}
}

// TestGuardian_LimitEntry verifies the entry is placed as a resting LIMIT order
// at the given price when configured, while the exit stays market.
func TestGuardian_LimitEntry(t *testing.T) {
	g := NewEntryGuardian("ETHUSDT", SideLong, 0.2, entryCfg(), 14, zap.NewNop())
	g.SetLimitEntry(1950)
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{}, b, zap.NewNop())
	ctx.Extra["engine_live"] = true

	g.OnTick(ctx, 2000)
	if len(b.orders) != 1 {
		t.Fatalf("expected 1 limit entry, got %d", len(b.orders))
	}
	o := b.orders[0]
	if o.Type != strategy.OrderLimit || o.Price != 1950 {
		t.Fatalf("expected LIMIT @1950, got type=%s price=%v", o.Type, o.Price)
	}
	if o.Side != strategy.SideBuy || o.PositionSide != strategy.PositionSideLong {
		t.Fatalf("expected buy/long open, got %+v", o)
	}
}

// TestGuardian_DoesNotReenterAfterRestartOnceEntryConsumed is the regression test
// for the 2026-08-05 real-money incident: PlaceEntry/wantEntry is a standing
// "open a position for me" instruction re-read from stored config on every
// restart. entryPlaced (the in-memory re-fire guard) resets to false on every
// new process, so once the entry had already fired once (in any prior run) and
// the account is later flat again for ANY reason (closed by the user, by TP/SL,
// or by an unrelated bug), a restart silently placed a brand-new entry the user
// never asked for. The one-shot "entry consumed" fact must survive a restart via
// the same persisted state store used for trail-stop restoration.
func TestGuardian_DoesNotReenterAfterRestartOnceEntryConsumed(t *testing.T) {
	store := NewMemStateStore()
	const key = "BTCUSDT-5m-guardian"

	// Run 1: fresh guardian, flat account -> places its one-shot entry.
	g1 := NewEntryGuardian("BTCUSDT", SideLong, 0.011, entryCfg(), 14, zap.NewNop())
	g1.SetStateStore(store, key)
	b1 := &gBroker{}
	ctx1 := strategy.NewContext(&gPortfolio{}, b1, zap.NewNop())
	g1.OnBar(ctx1, exchange.Kline{Open: 65300, High: 65300, Low: 65300, Close: 65300})
	if len(b1.orders) != 1 {
		t.Fatalf("run 1: expected the one-shot entry to fire, got %d orders", len(b1.orders))
	}

	// Run 2 simulates a restart (fresh Guardian struct, same persisted state, same
	// stateKey) with the account back to FLAT (the earlier position is gone --
	// exactly what happened after the guardian_state cross-user-contamination bug
	// closed it). It must NOT place a fresh entry.
	g2 := NewEntryGuardian("BTCUSDT", SideLong, 0.011, entryCfg(), 14, zap.NewNop())
	g2.SetStateStore(store, key)
	b2 := &gBroker{}
	ctx2 := strategy.NewContext(&gPortfolio{}, b2, zap.NewNop())
	g2.OnBar(ctx2, exchange.Kline{Open: 64000, High: 64000, Low: 64000, Close: 64000})
	if len(b2.orders) != 0 {
		t.Fatalf("run 2 (post-restart, flat): expected NO re-entry, got %d orders: %+v", len(b2.orders), b2.orders)
	}
}

// TestGuardian_ArmsFromLivePositionEvenWithoutFillNotification is the regression
// test for the 2026-08-05 "unmatched fill" incident: a resting LIMIT entry order
// filled on the exchange, but the fill was routed as an "unmatched fill" (the OMS
// no longer recognized its client-order-ID, e.g. after an intervening restart)
// and never reached guardian.OnFill. The guardian was stuck forever in
// "entryPlaced=true, waiting for a fill notification that will never come" —
// leaving a REAL exchange position with zero protection. It must instead notice
// the live position on a later bar and arm from it directly, exactly like the
// restart-adopt path does.
func TestGuardian_ArmsFromLivePositionEvenWithoutFillNotification(t *testing.T) {
	g := NewEntryGuardian("BTCUSDT", SideLong, 0.0015, entryCfg(), 14, zap.NewNop())
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{}, b, zap.NewNop())

	// Bar 1: flat -> places the one-shot limit entry as usual.
	g.OnBar(ctx, exchange.Kline{Open: 65300, High: 65300, Low: 65300, Close: 65300})
	if len(b.orders) != 1 {
		t.Fatalf("expected the entry order to be placed, got %d", len(b.orders))
	}
	if g.Prot() != nil {
		t.Fatal("must not be armed yet -- no fill has been delivered")
	}

	// Simulate the fill landing on the exchange WITHOUT ever calling g.OnFill
	// (the "unmatched fill" case) -- the syncer now reports a live position.
	livePos := &position.StrategyPosition{}
	livePos.Symbol, livePos.Side, livePos.Qty, livePos.EntryPrice = "BTCUSDT", "LONG", 0.001, 65301
	ctx.Extra["position_syncer"] = fakeLiveSource{long: livePos}

	// Bar 2: still no OnFill was ever delivered, but the guardian must notice the
	// live position and arm from it rather than waiting forever.
	g.OnBar(ctx, exchange.Kline{Open: 65301, High: 65301, Low: 65301, Close: 65301})
	if g.Prot() == nil {
		t.Fatal("expected the guardian to arm from the live position despite the missed fill notification")
	}
	if g.Prot().Qty != 0.001 || g.Prot().Entry != 65301 {
		t.Fatalf("armed with wrong entry/qty: %+v", g.Prot())
	}
	// Must NOT have placed a second (duplicate) entry order.
	if len(b.orders) != 1 {
		t.Fatalf("expected still exactly 1 order (no duplicate entry), got %d: %+v", len(b.orders), b.orders)
	}
}

// TestGuardian_DoesNotDoublePartialTPAfterRestart is a second instance of the
// same restart-state class of bug as the entry re-fire above: partialDone (the
// in-memory guard against firing the partial take-profit twice) also isn't
// persisted, so a restart while the position is still above the partial-TP
// trigger price would close ANOTHER fraction of the (already-reduced) position
// the user never asked to reduce again.
func TestGuardian_DoesNotDoublePartialTPAfterRestart(t *testing.T) {
	store := NewMemStateStore()
	const key = "ETHUSDT-partial-guardian"
	cfg := trailCfg() // R = 5% of 100 = 5
	cfg.PartialTPAtR = 2
	cfg.PartialTPFraction = 0.5

	// Run 1: adopt a full 1.0 qty position, price runs to +2R -> partial fires,
	// closing 0.5 and leaving 0.5 on the exchange.
	g1 := NewAdoptGuardian("ETHUSDT", cfg, 14, zap.NewNop())
	g1.SetStateStore(store, key)
	b1 := &gBroker{}
	ctx1 := strategy.NewContext(&gPortfolio{qty: 1, avg: 100}, b1, zap.NewNop())
	g1.OnBar(ctx1, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	g1.OnTick(ctx1, 110) // pnlR = 2.0 -> partial fires
	if n := countReason(b1.orders, "guardian_partial_tp"); n != 1 {
		t.Fatalf("run 1: expected exactly 1 partial TP, got %d", n)
	}

	// Run 2 simulates a restart: fresh Guardian, same persisted state, and the
	// account now genuinely holds only the REDUCED 0.5 qty (the real result of
	// run 1's fill) at the SAME elevated price. It must NOT fire a second partial.
	g2 := NewAdoptGuardian("ETHUSDT", cfg, 14, zap.NewNop())
	g2.SetStateStore(store, key)
	b2 := &gBroker{}
	ctx2 := strategy.NewContext(&gPortfolio{qty: 0.5, avg: 100}, b2, zap.NewNop())
	g2.OnBar(ctx2, exchange.Kline{Open: 110, High: 110, Low: 110, Close: 110})
	g2.OnTick(ctx2, 110) // still pnlR = 2.0
	if n := countReason(b2.orders, "guardian_partial_tp"); n != 0 {
		t.Fatalf("run 2 (post-restart): expected NO second partial TP, got %d: %+v", n, b2.orders)
	}
}

func countReason(orders []strategy.OrderRequest, reason string) int {
	n := 0
	for _, o := range orders {
		if o.Reason == reason {
			n++
		}
	}
	return n
}

// TestGuardian_EntryPlacesWhenFlat guards first-run behaviour: with no existing
// position, arm-with-entry must still place the entry once.
func TestGuardian_EntryPlacesWhenFlat(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  any
	}{
		{"no syncer (e.g. backtest)", nil},
		{"empty syncer", fakeLiveSource{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewEntryGuardian("ETHUSDT", SideShort, 0.1, entryCfg(), 14, zap.NewNop())
			b := &gBroker{}
			ctx := strategy.NewContext(&gPortfolio{}, b, zap.NewNop())
			if tc.src != nil {
				ctx.Extra["position_syncer"] = tc.src
			}
			g.OnBar(ctx, exchange.Kline{Open: 2000, High: 2005, Low: 1995, Close: 2000})
			if len(b.orders) != 1 {
				t.Fatalf("expected 1 entry order when flat, got %d: %+v", len(b.orders), b.orders)
			}
			if b.orders[0].PositionSide != strategy.PositionSideShort {
				t.Fatalf("expected OpenShort entry, got %+v", b.orders[0])
			}
		})
	}
}

// TestGuardian_LateEntryFillAfterSyncerAdoptDoesNotRetire is the regression
// test for the 2026-08-21 incident: the position syncer can discover the
// just-opened position (and arm protection + a resting stop from it) on a
// bar/tick BEFORE the entry order's own fill notification is dispatched to
// OnFill. That late "guardian_entry" fill must not be mistaken for the
// resting stop filling — 3 of 4 same-day guardian retirements on a
// real-money account were exactly this, silently dropping protection on a
// position that was never actually closed.
func TestGuardian_LateEntryFillAfterSyncerAdoptDoesNotRetire(t *testing.T) {
	g := NewEntryGuardian("ETHUSDT", SideLong, 1, entryCfg(), 14, zap.NewNop())
	g.SetRestingStop(true)
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{}, b, zap.NewNop())

	// Bar 1: flat, no syncer position yet -> places the entry order.
	g.OnBar(ctx, exchange.Kline{Open: 2000, High: 2005, Low: 1995, Close: 2000})
	if g.Prot() != nil {
		t.Fatal("must not arm until adopted or filled")
	}

	// Bar 2: the syncer now reports the position the entry order just opened
	// on the exchange -- before that order's own fill event has arrived. The
	// guardian adopts from the syncer and arms a resting stop.
	longPos := &position.StrategyPosition{}
	longPos.Symbol, longPos.Side, longPos.Qty, longPos.EntryPrice = "ETHUSDT", "LONG", 1, 2000
	ctx.Extra["position_syncer"] = fakeLiveSource{long: longPos}
	g.OnBar(ctx, exchange.Kline{Open: 2000, High: 2005, Low: 1995, Close: 2000})
	if g.Prot() == nil {
		t.Fatal("expected protection armed from the syncer-adopted position")
	}
	if g.phase != PhaseWatching {
		t.Fatalf("expected Watching after adopt+arm, got %s", g.phase)
	}

	// The entry order's own fill notification now arrives late.
	g.OnFill(ctx, strategy.Fill{Reason: "guardian_entry", Qty: 1, Price: 2000})

	if g.phase != PhaseWatching {
		t.Fatalf("late entry fill must not retire the guardian, got phase %s", g.phase)
	}
	if g.Retired() {
		t.Fatal("guardian must still be protecting the (never closed) position")
	}
}

// TestGuardian_RejectedEntryResetsOneShotGuard is the regression test for the
// 2026-08-21 incident (hit twice same day on a real-money account): placeEntry
// marked entryPlaced=true (and persisted it) BEFORE knowing whether the
// exchange would actually accept the order. When the order was rejected
// (e.g. "Margin is insufficient"), nothing ever reset the flag -- every
// future restart, even after the user fixed the underlying problem and
// changed params, silently no-opped forever with only a log line to show
// for it. PlaceOrder returns "" on a synchronous rejection (only fill
// confirmation is async), so that's the signal to reset.
func TestGuardian_RejectedEntryResetsOneShotGuard(t *testing.T) {
	g := NewEntryGuardian("ETHUSDT", SideLong, 1, entryCfg(), 14, zap.NewNop())
	b := &gBroker{rejectOrders: true}
	ctx := strategy.NewContext(&gPortfolio{}, b, zap.NewNop())

	g.OnBar(ctx, exchange.Kline{Open: 2000, High: 2005, Low: 1995, Close: 2000})
	if g.entryPlaced {
		t.Fatal("a rejected entry must reset entryPlaced, not leave it stuck true")
	}
	if len(b.orders) != 1 {
		t.Fatalf("expected exactly 1 rejected attempt, got %d", len(b.orders))
	}

	// Immediately retrying (still within the cooldown) must NOT resubmit --
	// otherwise every tick would hammer the exchange with the same doomed
	// order until the underlying problem (e.g. margin) is fixed.
	g.OnBar(ctx, exchange.Kline{Open: 2000, High: 2005, Low: 1995, Close: 2000})
	if len(b.orders) != 1 {
		t.Fatalf("expected no retry within the cooldown, got %d attempts", len(b.orders))
	}

	// Once the cooldown has elapsed, a fresh attempt is allowed -- and this
	// time the exchange accepts it (e.g. the user topped up margin).
	g.entryBackoff.retryAfter = time.Now().Add(-time.Second)
	b.rejectOrders = false
	g.OnBar(ctx, exchange.Kline{Open: 2000, High: 2005, Low: 1995, Close: 2000})
	if len(b.orders) != 2 {
		t.Fatalf("expected a retry after the cooldown elapsed, got %d attempts", len(b.orders))
	}
	if !g.entryPlaced {
		t.Fatal("a successful retry must set entryPlaced again")
	}
}
