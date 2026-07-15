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
}

func (b *relBroker) PlaceOrder(req strategy.OrderRequest) string {
	b.placed = append(b.placed, req)
	if req.Type == strategy.OrderStopMarket && b.noRestingStop {
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
	if !g.done {
		t.Fatal("OnFill of the resting stop should mark done")
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

	if !g.done {
		t.Fatal("guardian must retire (done) when the position is closed externally")
	}
	if len(b.canceled) < 1 {
		t.Fatal("guardian must cancel its resting stop on retire (no orphan)")
	}
	if n := len(b.byType(strategy.OrderMarket)); n != 0 {
		t.Fatalf("must not place a close on a flat account, got %d", n)
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
