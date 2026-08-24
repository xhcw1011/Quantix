package guardian

import (
	"fmt"
	"testing"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
)

// relBroker records placements and cancels and hands out distinct order ids so the
// reliability tests can assert the resting stop is placed, replaced, and cancelled.
type relBroker struct {
	placed        []strategy.OrderRequest
	canceled      []string
	seq           int
	noRestingStop bool // simulate an environment where STOP_MARKET can't rest (returns "")
	noRestingTP   bool // simulate an environment where the resting take-profit LIMIT can't rest (returns "")
}

func (b *relBroker) PlaceOrder(req strategy.OrderRequest) string {
	b.placed = append(b.placed, req)
	if req.Type == strategy.OrderStopMarket && b.noRestingStop {
		return ""
	}
	if req.Type == strategy.OrderLimit && req.Reason == "guardian_resting_tp" && b.noRestingTP {
		return ""
	}
	b.seq++
	return fmt.Sprintf("ord%d", b.seq)
}
func (b *relBroker) CancelOrder(id string) error { b.canceled = append(b.canceled, id); return nil }

func (b *relBroker) byType(t strategy.OrderType) []strategy.OrderRequest {
	var out []strategy.OrderRequest
	for _, o := range b.placed {
		if o.Type == t {
			out = append(out, o)
		}
	}
	return out
}

func newRestingLong() (*Guardian, *relBroker, *strategy.Context) {
	g := NewGuardian("ETHUSDT", NewProtection(SideLong, 100, 1, trailCfg(), 0), 14, zap.NewNop())
	g.SetRestingStop(true)
	b := &relBroker{}
	ctx := strategy.NewContext(&gPortfolio{qty: 1, avg: 100}, b, zap.NewNop())
	return g, b, ctx
}

func TestGuardian_PlacesRestingStopOnArm(t *testing.T) {
	g, b, ctx := newRestingLong() // stop = 95
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})

	so := b.byType(strategy.OrderStopMarket)
	if len(so) != 1 {
		t.Fatalf("want exactly 1 resting stop, got %d", len(so))
	}
	if so[0].StopPrice != 95 || so[0].Side != strategy.SideSell {
		t.Fatalf("resting stop wrong: %+v", so[0])
	}
	if !g.restingMode {
		t.Fatal("should be in resting mode after a successful placement")
	}
}

func TestGuardian_RestingMode_NoTickCloseUntilFill(t *testing.T) {
	g, b, ctx := newRestingLong()
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // arm + rest stop @95
	g.OnTick(ctx, 94)                                                       // below stop, but exchange owns the trigger

	if n := len(b.byType(strategy.OrderMarket)); n != 0 {
		t.Fatalf("resting mode must not place a market close, got %d", n)
	}
	g.OnFill(ctx, strategy.Fill{Symbol: "ETHUSDT", Qty: 1, Price: 95}) // exchange stop filled
	if g.phase != PhaseClosed {
		t.Fatalf("OnFill of the resting stop should mark phase Closed, got %s", g.phase)
	}
}

func TestGuardian_FallsBackToTickCloseWhenRestingUnsupported(t *testing.T) {
	g := NewGuardian("ETHUSDT", NewProtection(SideLong, 100, 1, trailCfg(), 0), 14, zap.NewNop())
	g.SetRestingStop(true)
	b := &relBroker{noRestingStop: true}
	ctx := strategy.NewContext(&gPortfolio{qty: 1, avg: 100}, b, zap.NewNop())

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // arm; resting returns "" → fallback
	if g.restingMode {
		t.Fatal("should have fallen back to tick mode when resting stop unsupported")
	}
	g.OnTick(ctx, 94) // stop hit → fallback market close
	if n := len(b.byType(strategy.OrderMarket)); n != 1 {
		t.Fatalf("fallback should place exactly 1 market close, got %d", n)
	}
}

func TestGuardian_AdvancesRestingStopOnTrail(t *testing.T) {
	g, b, ctx := newRestingLong()
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // arm + rest @95
	g.OnTick(ctx, 106)                                                      // activate, stop -> 101
	g.OnTick(ctx, 110)                                                      // stop -> 105

	so := b.byType(strategy.OrderStopMarket)
	last := so[len(so)-1]
	if last.StopPrice != 105 {
		t.Fatalf("resting stop should advance to 105, got %v (all: %d)", last.StopPrice, len(so))
	}
	if len(b.canceled) < 1 {
		t.Fatal("old resting stop must be canceled when replaced")
	}
}

func TestGuardian_DoesNotChurnRestingStopBelowEpsilon(t *testing.T) {
	g, b, ctx := newRestingLong()
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	g.OnTick(ctx, 106) // activate, stop -> 101, replace
	n := len(b.byType(strategy.OrderStopMarket))
	g.OnTick(ctx, 106.02) // stop -> ~101.02, tiny advance
	if len(b.byType(strategy.OrderStopMarket)) != n {
		t.Fatal("sub-epsilon advance must not churn a replacement")
	}
}

func TestGuardian_RestoresTrailedStopAfterRestart(t *testing.T) {
	store := NewMemStateStore()
	mk := func(b *relBroker) (*Guardian, *strategy.Context) {
		g := NewGuardian("ETHUSDT", NewProtection(SideLong, 100, 1, trailCfg(), 0), 14, zap.NewNop())
		g.SetRestingStop(true)
		g.SetStateStore(store, "eng1")
		ctx := strategy.NewContext(&gPortfolio{qty: 1, avg: 100}, b, zap.NewNop())
		return g, ctx
	}

	// first run: arm then trail the stop up to 105.
	b1 := &relBroker{}
	g1, c1 := mk(b1)
	g1.OnBar(c1, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	g1.OnTick(c1, 106)
	g1.OnTick(c1, 110)
	if g1.prot.Stop != 105 {
		t.Fatalf("pre-restart stop should be 105, got %v", g1.prot.Stop)
	}

	// "restart": a fresh Guardian sharing the store/key must restore the trailed stop.
	b2 := &relBroker{}
	g2, c2 := mk(b2)
	g2.OnBar(c2, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	if g2.prot.Stop != 105 {
		t.Fatalf("restart must restore trailed stop 105 (not reset to initial 95), got %v", g2.prot.Stop)
	}
	if n := len(b2.byType(strategy.OrderStopMarket)); n != 0 {
		t.Fatalf("restored guardian must not place a duplicate resting stop, got %d", n)
	}
}

func TestGuardian_RetiresWhenPositionClosedExternally(t *testing.T) {
	g := NewGuardian("ETHUSDT", NewProtection(SideLong, 100, 1, trailCfg(), 0), 14, zap.NewNop())
	g.SetRestingStop(true)
	pf := &gPortfolio{qty: 1, avg: 100}
	b := &relBroker{}
	ctx := strategy.NewContext(pf, b, zap.NewNop())

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // arm + resting stop
	pf.qty = 0                                                              // user closes / liquidation
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // detect flat

	if g.phase != PhaseClosed {
		t.Fatalf("guardian must retire (phase Closed) when the position is closed externally, got %s", g.phase)
	}
	if len(b.canceled) < 1 {
		t.Fatal("guardian must cancel its resting stop on retire (no orphan)")
	}
	if n := len(b.byType(strategy.OrderMarket)); n != 0 {
		t.Fatalf("must not place a close on a flat account, got %d", n)
	}
}

func (b *relBroker) byReason(reason string) []strategy.OrderRequest {
	var out []strategy.OrderRequest
	for _, o := range b.placed {
		if o.Reason == reason {
			out = append(out, o)
		}
	}
	return out
}

func TestGuardian_PartialTakeProfit(t *testing.T) {
	cfg := trailCfg() // R = 5 (5% of 100)
	cfg.PartialTPAtR = 2
	cfg.PartialTPFraction = 0.5
	g := NewGuardian("ETHUSDT", NewProtection(SideLong, 100, 1, cfg, 0), 14, zap.NewNop())
	b := &relBroker{}
	ctx := strategy.NewContext(&gPortfolio{qty: 1, avg: 100}, b, zap.NewNop())

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	g.OnTick(ctx, 108) // pnlR 1.6 < 2: no partial yet
	if len(b.byReason("guardian_partial_tp")) != 0 {
		t.Fatal("partial must not fire before +2R")
	}
	g.OnTick(ctx, 110) // pnlR 2.0 >= 2: close half
	pt := b.byReason("guardian_partial_tp")
	if len(pt) != 1 || pt[0].Qty != 0.5 {
		t.Fatalf("partial should close 0.5 once, got %+v", pt)
	}
	if g.phase == PhaseClosed {
		t.Fatal("a partial take-profit must not end the guardian")
	}
	g.OnTick(ctx, 112) // must not fire a second partial
	if len(b.byReason("guardian_partial_tp")) != 1 {
		t.Fatal("partial take-profit fires only once")
	}
}

func TestGuardian_ArmWithEntry(t *testing.T) {
	g := NewEntryGuardian("ETHUSDT", SideLong, 1, trailCfg(), 14, zap.NewNop())
	b := &relBroker{}
	pf := &gPortfolio{qty: 0} // no position yet
	ctx := strategy.NewContext(pf, b, zap.NewNop())

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // place entry
	ent := b.byReason("guardian_entry")
	if len(ent) != 1 || ent[0].Side != strategy.SideBuy {
		t.Fatalf("should place a market entry (BUY) once, got %+v", ent)
	}
	if g.prot != nil {
		t.Fatal("must not arm until the entry fills")
	}

	g.OnFill(ctx, strategy.Fill{Reason: "guardian_entry", Qty: 1, Price: 100}) // entry fills
	pf.qty = 1                                                                 // account now holds the position
	if g.prot == nil || g.prot.Entry != 100 || g.prot.Qty != 1 {
		t.Fatalf("should arm from the entry fill @100 qty1, got %+v", g.prot)
	}

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	if len(b.byReason("guardian_entry")) != 1 {
		t.Fatal("entry must be placed only once")
	}
}

// tpCfg mirrors trailCfg() (stop = 95) but also configures a take-profit at
// 3R = 115, for resting-take-profit tests.
func tpCfg() ProtectionConfig {
	cfg := trailCfg()
	cfg.TPMode = TPR
	cfg.TPValue = 3
	return cfg
}

func newRestingLongWithTP() (*Guardian, *relBroker, *strategy.Context) {
	g := NewGuardian("ETHUSDT", NewProtection(SideLong, 100, 1, tpCfg(), 0), 14, zap.NewNop())
	g.SetRestingStop(true)
	g.SetRestingTP(true)
	b := &relBroker{}
	ctx := strategy.NewContext(&gPortfolio{qty: 1, avg: 100}, b, zap.NewNop())
	return g, b, ctx
}

func TestGuardian_PlacesRestingTPOnArm(t *testing.T) {
	g, b, ctx := newRestingLongWithTP() // tp = 115
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})

	to := b.byType(strategy.OrderLimit)
	if len(to) != 1 {
		t.Fatalf("want exactly 1 resting take-profit, got %d", len(to))
	}
	if to[0].Price != 115 || to[0].Side != strategy.SideSell || to[0].Reason != "guardian_resting_tp" {
		t.Fatalf("resting take-profit wrong: %+v", to[0])
	}
	if !g.restingTPMode {
		t.Fatal("should be in resting-TP mode after a successful placement")
	}
}

func TestGuardian_RestingTPMode_NoTickCloseUntilFill(t *testing.T) {
	g, b, ctx := newRestingLongWithTP()
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // arm + rest stop@95 + TP@115
	g.OnTick(ctx, 116)                                                      // above TP, but the exchange owns the fill

	if n := len(b.byType(strategy.OrderMarket)); n != 0 {
		t.Fatalf("resting-TP mode must not place a market close, got %d", n)
	}
	g.OnFill(ctx, strategy.Fill{Symbol: "ETHUSDT", Qty: 1, Price: 115, Reason: "guardian_resting_tp"})
	if g.phase != PhaseClosed {
		t.Fatalf("OnFill of the resting take-profit should mark phase Closed, got %s", g.phase)
	}
}

func TestGuardian_FallsBackToTickCloseWhenRestingTPUnsupported(t *testing.T) {
	g := NewGuardian("ETHUSDT", NewProtection(SideLong, 100, 1, tpCfg(), 0), 14, zap.NewNop())
	g.SetRestingTP(true)
	b := &relBroker{noRestingTP: true}
	ctx := strategy.NewContext(&gPortfolio{qty: 1, avg: 100}, b, zap.NewNop())

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // arm; resting TP returns "" → fallback
	if g.restingTPMode {
		t.Fatal("should have fallen back to tick mode when resting take-profit unsupported")
	}
	g.OnTick(ctx, 116) // TP hit -> fallback market close
	if n := len(b.byType(strategy.OrderMarket)); n != 1 {
		t.Fatalf("fallback should place exactly 1 market close, got %d", n)
	}
}

func TestGuardian_OnFill_RestingTPCancelsSiblingStop(t *testing.T) {
	g, b, ctx := newRestingLongWithTP()
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // rest stop@95 + TP@115
	stopID := g.stopOrderID
	if stopID == "" {
		t.Fatal("expected a resting stop order id")
	}

	g.OnFill(ctx, strategy.Fill{Symbol: "ETHUSDT", Qty: 1, Price: 115, Reason: "guardian_resting_tp"})
	if g.phase != PhaseClosed {
		t.Fatalf("resting take-profit fill should mark phase Closed, got %s", g.phase)
	}
	if g.stopOrderID != "" {
		t.Fatal("sibling resting stop tracker must be cleared")
	}
	found := false
	for _, id := range b.canceled {
		if id == stopID {
			found = true
		}
	}
	if !found {
		t.Fatalf("sibling resting stop %q must be cancelled, canceled=%v", stopID, b.canceled)
	}
}

func TestGuardian_OnFill_RestingStopCancelsSiblingTP(t *testing.T) {
	g, b, ctx := newRestingLongWithTP()
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // rest stop@95 + TP@115
	tpID := g.tpOrderID
	if tpID == "" {
		t.Fatal("expected a resting take-profit order id")
	}

	g.OnFill(ctx, strategy.Fill{Symbol: "ETHUSDT", Qty: 1, Price: 95, Reason: "guardian_resting_stop"})
	if g.phase != PhaseClosed {
		t.Fatalf("resting stop fill should mark phase Closed, got %s", g.phase)
	}
	if g.tpOrderID != "" {
		t.Fatal("sibling resting take-profit tracker must be cleared")
	}
	found := false
	for _, id := range b.canceled {
		if id == tpID {
			found = true
		}
	}
	if !found {
		t.Fatalf("sibling resting take-profit %q must be cancelled, canceled=%v", tpID, b.canceled)
	}
}

func TestGuardian_OnFill_UntaggedFillCancelsBothRestingOrders(t *testing.T) {
	// Some fill paths (e.g. restart recovery) don't carry a Reason. Without
	// it we can't tell which leg filled, so both must be cancelled — the one
	// that actually filled is a harmless no-op cancel, but skipping the
	// genuine sibling would orphan it on the exchange.
	g, b, ctx := newRestingLongWithTP()
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	stopID, tpID := g.stopOrderID, g.tpOrderID

	g.OnFill(ctx, strategy.Fill{Symbol: "ETHUSDT", Qty: 1, Price: 115})
	if g.phase != PhaseClosed {
		t.Fatalf("untagged fill while a resting order is live should still mark phase Closed, got %s", g.phase)
	}
	canceled := map[string]bool{}
	for _, id := range b.canceled {
		canceled[id] = true
	}
	if !canceled[stopID] || !canceled[tpID] {
		t.Fatalf("both resting orders must be cancelled defensively, canceled=%v", b.canceled)
	}
}

func TestGuardian_ResizesRestingTPWhenPositionChanges(t *testing.T) {
	g, _, _ := newRestingLongWithTP()
	pf := &gPortfolio{qty: 1, avg: 100}
	b := &relBroker{}
	ctx := strategy.NewContext(pf, b, zap.NewNop())

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // resting TP qty 1
	pf.qty = 2                                                              // user added to the position
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // detect resize

	to := b.byType(strategy.OrderLimit)
	if to[len(to)-1].Qty != 2 {
		t.Fatalf("resting take-profit must resize to qty 2, got %v", to[len(to)-1].Qty)
	}
}

func TestGuardian_ForceResyncRestingTP_OnParamUpdate(t *testing.T) {
	g, b, ctx := newRestingLongWithTP() // tp = 115
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	firstTP := g.tpOrderID
	if firstTP == "" {
		t.Fatal("expected an initial resting take-profit")
	}

	if err := g.UpdateParams(ctx, map[string]any{
		"StopValue": 0.05, "TPMode": "r", "TPValue": 5.0, // tp -> 100 + 5*5 = 125
	}); err != nil {
		t.Fatalf("UpdateParams: %v", err)
	}
	if g.prot.TPPrice() != 125 {
		t.Fatalf("TP price should update to 125, got %v", g.prot.TPPrice())
	}
	to := b.byType(strategy.OrderLimit)
	last := to[len(to)-1]
	if last.Price != 125 {
		t.Fatalf("resting take-profit should move to 125, got %v", last.Price)
	}
	if len(b.canceled) < 1 {
		t.Fatal("old resting take-profit must be cancelled when replaced")
	}

	// Turning take-profit off must cancel the resting order and not replace it.
	if err := g.UpdateParams(ctx, map[string]any{"StopValue": 0.05, "TPValue": 0.0}); err != nil {
		t.Fatalf("UpdateParams: %v", err)
	}
	if g.tpOrderID != "" || g.restingTPMode {
		t.Fatal("disabling take-profit must cancel the resting order and not replace it")
	}
}

func TestGuardian_ResizesRestingStopWhenPositionChanges(t *testing.T) {
	g := NewGuardian("ETHUSDT", NewProtection(SideLong, 100, 1, trailCfg(), 0), 14, zap.NewNop())
	g.SetRestingStop(true)
	pf := &gPortfolio{qty: 1, avg: 100}
	b := &relBroker{}
	ctx := strategy.NewContext(pf, b, zap.NewNop())

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // resting stop qty 1
	pf.qty = 2                                                              // user added to the position
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // detect resize

	so := b.byType(strategy.OrderStopMarket)
	if so[len(so)-1].Qty != 2 {
		t.Fatalf("resting stop must resize to qty 2, got %v", so[len(so)-1].Qty)
	}
	if len(b.canceled) < 1 {
		t.Fatal("old resting stop must be canceled on resize")
	}
}
