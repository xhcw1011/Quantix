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
