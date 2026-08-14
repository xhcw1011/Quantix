package guardian

import (
	"testing"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
)

// fakeSyncer is a minimal livePositionSource for tests that need arm-with-entry
// (ModeEntry) to see an exchange-truth position independent of ctx.Portfolio
// (mirroring how the live engine always wires a real *position.Syncer).
type fakeSyncer struct{ long, short *position.StrategyPosition }

func (f *fakeSyncer) GetLong() *position.StrategyPosition  { return f.long }
func (f *fakeSyncer) GetShort() *position.StrategyPosition { return f.short }

// These tests are the core deliverable of the Phase/Mode state-machine
// refactor: a guardian restarted mid-close (persisted Closing:true) must
// never re-arm blindly, never place a duplicate close/entry, and must always
// clear its persisted state once truly done -- otherwise a stale Closing:true
// permanently poisons a future, unrelated guardian that reuses the same
// (user, engine_id) (2026-08-07 finding, see clearState doc comment).

// A tick-based close is always a plain market order (never a resting order),
// so by the time any realistic restart happens it has already resolved on
// the exchange one way or another -- there's no "still in flight" state to
// avoid racing. So an unresolved-looking qty (unchanged, or rejected) is
// re-armed just like a genuine partial fill (see PartialFillReArmsRemainder
// below), and if the market is still beyond the stop it correctly closes
// again -- exactly once, not zero and not twice.
func TestGuardian_RestartWhileClosing_UnresolvedCloseReArmsThenRecloses(t *testing.T) {
	store := NewMemStateStore()
	pf := &gPortfolio{qty: 1, avg: 100}
	mk := func(b *relBroker) (*Guardian, *strategy.Context) {
		g := NewAdoptGuardian("ETHUSDT", trailCfg(), 14, zap.NewNop())
		g.SetStateStore(store, "eng1")
		ctx := strategy.NewContext(pf, b, zap.NewNop())
		return g, ctx
	}

	b1 := &relBroker{}
	g1, ctx1 := mk(b1)
	g1.OnBar(ctx1, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // arm, stop=95
	g1.OnTick(ctx1, 94)                                                       // stop hit -> close placed
	if g1.phase != PhaseClosing {
		t.Fatalf("expected phase Closing, got %s", g1.phase)
	}

	// "restart": the account still shows the original qty (the exchange
	// either rejected the close, or it simply never touched the position)
	// and the price is still beyond the stop.
	b2 := &relBroker{}
	g2, ctx2 := mk(b2)
	g2.OnBar(ctx2, exchange.Kline{Open: 94, High: 95, Low: 93, Close: 94})

	if n := len(b2.byReason("guardian_stop_loss")); n != 1 {
		t.Fatalf("expected exactly one fresh close on the re-armed position, got %d", n)
	}
	if g2.phase != PhaseClosing {
		t.Fatalf("expected the fresh close to move phase back to Closing, got %s", g2.phase)
	}
}

func TestGuardian_RestartWhileClosing_ResyncConfirmsCloseAndRetires(t *testing.T) {
	store := NewMemStateStore()
	pf := &gPortfolio{qty: 1, avg: 100}
	mk := func(b *relBroker) (*Guardian, *strategy.Context) {
		g := NewAdoptGuardian("ETHUSDT", trailCfg(), 14, zap.NewNop())
		g.SetStateStore(store, "eng1")
		ctx := strategy.NewContext(pf, b, zap.NewNop())
		return g, ctx
	}

	b1 := &relBroker{}
	g1, ctx1 := mk(b1)
	g1.OnBar(ctx1, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	g1.OnTick(ctx1, 94) // close placed, Closing persisted

	// "restart": the close actually filled while the process was down.
	pf.qty = 0
	b2 := &relBroker{}
	g2, ctx2 := mk(b2)
	g2.OnBar(ctx2, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 94})

	if g2.phase != PhaseClosed {
		t.Fatalf("expected phase Closed once resync confirms qty=0, got %s", g2.phase)
	}
	if _, ok := store.Load("eng1"); ok {
		t.Error("expected persisted state cleared once retired (must not poison a future guardian)")
	}
}

func TestGuardian_RestartWhileClosing_PartialFillReArmsRemainder(t *testing.T) {
	store := NewMemStateStore()
	pf := &gPortfolio{qty: 1, avg: 100}
	mk := func(b *relBroker) (*Guardian, *strategy.Context) {
		g := NewAdoptGuardian("ETHUSDT", trailCfg(), 14, zap.NewNop())
		g.SetStateStore(store, "eng1")
		ctx := strategy.NewContext(pf, b, zap.NewNop())
		return g, ctx
	}

	b1 := &relBroker{}
	g1, ctx1 := mk(b1)
	g1.OnBar(ctx1, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	g1.OnTick(ctx1, 94) // close placed for qty 1, Closing persisted

	// "restart": the close only partially filled while the process was down.
	pf.qty = 0.4
	b2 := &relBroker{}
	g2, ctx2 := mk(b2)
	g2.OnBar(ctx2, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})

	if g2.phase != PhaseWatching {
		t.Fatalf("expected the remainder re-armed into Watching, got %s", g2.phase)
	}
	if g2.prot == nil || g2.prot.Qty != 0.4 {
		t.Fatalf("expected remainder armed with qty 0.4, got %+v", g2.prot)
	}
	if n := len(b2.byType(strategy.OrderMarket)); n != 0 {
		t.Fatalf("must not place another close on the re-armed remainder this bar, got %d", n)
	}
}

func TestGuardian_OnFillClearsClosingFlagForFutureReuse(t *testing.T) {
	store := NewMemStateStore()
	g, _, ctx := newLongGuardian(trailCfg())
	g.SetStateStore(store, "eng1")
	g.OnTick(ctx, 106) // activate, stop -> 101
	g.OnTick(ctx, 100) // 100 <= 101 -> close placed, Closing persisted

	if _, ok := store.Load("eng1"); !ok {
		t.Fatal("expected Closing state persisted after placeClose")
	}
	g.OnFill(ctx, strategy.Fill{Symbol: "ETHUSDT", Qty: 1, Price: 100}) // protective close confirmed

	if g.phase != PhaseClosed {
		t.Fatalf("expected phase Closed after OnFill, got %s", g.phase)
	}
	if _, ok := store.Load("eng1"); ok {
		t.Error("expected persisted state cleared on OnFill close-confirmation, or a future guardian reusing this engine_id inherits stale Closing:true")
	}
}

func TestGuardian_RetireClearsStateForFutureReuse(t *testing.T) {
	store := NewMemStateStore()
	_ = store.Save("eng1", GuardianState{Stop: 101, Activated: true}) // simulate prior trail progress
	g := NewGuardian("ETHUSDT", NewProtection(SideLong, 100, 1, trailCfg(), 0), 14, zap.NewNop())
	g.SetStateStore(store, "eng1")
	pf := &gPortfolio{qty: 1, avg: 100}
	b := &relBroker{}
	ctx := strategy.NewContext(pf, b, zap.NewNop())

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // restores trail overlay
	pf.qty = 0                                                              // user closes externally
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // detect flat -> retire

	if g.phase != PhaseClosed {
		t.Fatalf("expected phase Closed, got %s", g.phase)
	}
	if _, ok := store.Load("eng1"); ok {
		t.Error("expected persisted trail state cleared on external-close retire")
	}
}

func TestGuardian_RestingStopFillStillMarksClosedViaStopOrderIDPath(t *testing.T) {
	store := NewMemStateStore()
	g, _, ctx := newRestingLong()
	g.SetStateStore(store, "eng1")
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // arm + rest stop @95, persists

	if _, ok := store.Load("eng1"); !ok {
		t.Fatal("expected resting-stop placement to persist state")
	}
	if g.phase == PhaseClosing {
		t.Fatal("resting stop mode should stay Watching until the exchange fill arrives, not pre-emptively Closing")
	}
	g.OnFill(ctx, strategy.Fill{Symbol: "ETHUSDT", Qty: 1, Price: 95}) // exchange stop filled

	if g.phase != PhaseClosed {
		t.Fatalf("expected phase Closed via the stopOrderID fallback path, got %s", g.phase)
	}
	if _, ok := store.Load("eng1"); ok {
		t.Error("expected persisted state cleared once the resting stop fill closes the guardian")
	}
}

func TestGuardian_ModeEntry_ClosingFlagBlocksReArmNotReEntry(t *testing.T) {
	store := NewMemStateStore()
	syncer := &fakeSyncer{long: &position.StrategyPosition{
		ExchangePosition: position.ExchangePosition{Symbol: "ETHUSDT", Side: "LONG", Qty: 1, EntryPrice: 100},
	}}
	mk := func(b *relBroker) (*Guardian, *strategy.Context) {
		g := NewEntryGuardian("ETHUSDT", SideLong, 1, trailCfg(), 14, zap.NewNop())
		g.SetStateStore(store, "eng1")
		ctx := strategy.NewContext(&gPortfolio{}, b, zap.NewNop())
		ctx.Extra["position_syncer"] = syncer
		return g, ctx
	}

	b1 := &relBroker{}
	g1, ctx1 := mk(b1)
	g1.OnBar(ctx1, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // adopts from the syncer, arms immediately
	if g1.prot == nil {
		t.Fatal("expected the guardian to arm from the adopted position")
	}
	g1.OnTick(ctx1, 94) // stop hit -> close placed, Closing persisted
	if len(b1.byReason("guardian_stop_loss")) != 1 {
		t.Fatalf("expected the protective close, got %d", len(b1.byReason("guardian_stop_loss")))
	}

	// "restart": the close only partially filled while the process was down.
	syncer.long.Qty = 0.4
	b2 := &relBroker{}
	g2, ctx2 := mk(b2)
	g2.OnBar(ctx2, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 94})

	if n := len(b2.byReason("guardian_entry")); n != 0 {
		t.Fatalf("restart while closing must never place a second entry, got %d", n)
	}
	if g2.phase != PhaseWatching {
		t.Fatalf("expected the remainder re-armed into Watching, got %s", g2.phase)
	}
	if g2.prot == nil || g2.prot.Qty != 0.4 {
		t.Fatalf("expected remainder armed with qty 0.4, got %+v", g2.prot)
	}
}
