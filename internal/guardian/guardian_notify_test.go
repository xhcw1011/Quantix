package guardian

import (
	"testing"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
)

func hasTitle(sent []Alert, title string) bool {
	for _, a := range sent {
		if a.Title == title {
			return true
		}
	}
	return false
}

func TestGuardian_NotifiesRestingLifecycle(t *testing.T) {
	g, _, ctx := newRestingLong()
	fd := &fakeDispatch{}
	g.SetDispatcher(fd)

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100}) // arm + place stop
	if !hasTitle(fd.sent, "stop_placed") {
		t.Fatal("should notify stop_placed when the resting stop is armed")
	}
	g.OnTick(ctx, 106)
	g.OnTick(ctx, 110) // trail advances
	if !hasTitle(fd.sent, "stop_advanced") {
		t.Fatal("should notify stop_advanced when the trail moves the stop")
	}
	g.OnFill(ctx, strategy.Fill{Symbol: "ETHUSDT", Qty: 1, Price: 105})
	if !hasTitle(fd.sent, "closed") {
		t.Fatal("should notify closed when the protective exit fills")
	}
}

func TestGuardian_NotifiesRetireOnExternalClose(t *testing.T) {
	g := NewGuardian("ETHUSDT", NewProtection(SideLong, 100, 1, trailCfg(), 0), 14, zap.NewNop())
	g.SetRestingStop(true)
	fd := &fakeDispatch{}
	g.SetDispatcher(fd)
	pf := &gPortfolio{qty: 1, avg: 100}
	ctx := strategy.NewContext(pf, &relBroker{}, zap.NewNop())

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	pf.qty = 0
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	if !hasTitle(fd.sent, "retired") {
		t.Fatal("should notify retired when the position is closed externally")
	}
}

func TestGuardian_NotifiesRiskPreviewOnArm(t *testing.T) {
	g, _, ctx := newRestingLong() // long 100 x1, stop 95 (5%), risk $5
	fd := &fakeDispatch{}
	g.SetDispatcher(fd)
	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	if !hasTitle(fd.sent, "armed") {
		t.Fatal("should notify an arm summary with the risk preview")
	}
	if hasTitle(fd.sent, "warning") {
		t.Fatal("a 5% stop should not raise a sanity warning")
	}
}

func TestGuardian_WarnsOnTooTightStop(t *testing.T) {
	cfg := trailCfg()
	cfg.StopValue = 0.002 // 0.2% — too tight, will get whipsawed
	g := NewGuardian("ETHUSDT", NewProtection(SideLong, 100, 1, cfg, 0), 14, zap.NewNop())
	g.SetRestingStop(true)
	fd := &fakeDispatch{}
	g.SetDispatcher(fd)
	ctx := strategy.NewContext(&gPortfolio{qty: 1, avg: 100}, &relBroker{}, zap.NewNop())

	g.OnBar(ctx, exchange.Kline{Open: 100, High: 101, Low: 99, Close: 100})
	if !hasTitle(fd.sent, "warning") {
		t.Fatal("a 0.2% stop should raise a too-tight warning")
	}
}
