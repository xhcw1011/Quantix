package grid

import (
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

func barVol(sym string, close_, vol float64) exchange.Kline {
	return exchange.Kline{Symbol: sym, Interval: "1h", OpenTime: time.Now(), Close: close_, Volume: vol}
}

// End-to-end: with the gate enabled, a volume spike must flatten the held
// inventory (a SELL of the position qty) and re-center the grid (basePrice=0).
func TestGrid_VolGateFlattensAndPausesOnSpike(t *testing.T) {
	g := New(Config{
		Symbol: "BTCUSDT", GridLevels: 3, GridSpacing: 0.01, BaseQty: 0.01,
		VolGateWindow: 20, VolGateRatioBars: 5, VolGateExit: 0.70, VolGateEnter: 0.40,
		VolGateCooldown: 1, VolGatePersistence: 1,
	})
	log, _ := zap.NewDevelopment()
	broker := &mockBroker{}
	port := &mockPortfolio{cash: 10000, qty: 0.03, hasPos: true} // pretend we hold long inventory
	ctx := strategy.NewContext(port, broker, log)

	for i := 0; i < 30; i++ { // calm warmup establishes the baseline + grid
		g.OnBar(ctx, barVol("BTCUSDT", 50000, 100))
	}
	n0 := len(broker.orders)

	g.OnBar(ctx, barVol("BTCUSDT", 50000, 100000)) // volume spike → gate off

	if len(broker.orders) <= n0 {
		t.Fatal("expected a flatten order on volume spike")
	}
	last := broker.orders[len(broker.orders)-1]
	if last.Side != strategy.SideSell || last.Qty != 0.03 {
		t.Errorf("flatten should SELL the held qty 0.03, got side=%v qty=%v", last.Side, last.Qty)
	}
	if g.basePrice != 0 {
		t.Errorf("grid should re-center (basePrice=0) after gate-off, got %f", g.basePrice)
	}
}

// The catastrophic price stop catches low-volume grind-downs the volume gate is
// blind to: when the held inventory's unrealized loss vs avg entry exceeds
// StopLossPct, flatten and re-center.
func TestGrid_CatastrophicStopFlattensOnGrind(t *testing.T) {
	g := New(Config{Symbol: "BTCUSDT", GridLevels: 3, GridSpacing: 0.01, BaseQty: 0.01, StopLossPct: 0.10})
	log, _ := zap.NewDevelopment()
	broker := &mockBroker{}
	port := &mockPortfolio{cash: 10000}
	ctx := strategy.NewContext(port, broker, log)

	g.OnBar(ctx, barVol("BTCUSDT", 50000, 100)) // init grid, base=50000

	// now holding a long at avg 50000; price grinds down 12% (> 10% stop), no vol spike
	port.qty, port.avg, port.hasPos = 0.03, 50000, true
	n0 := len(broker.orders)
	g.OnBar(ctx, barVol("BTCUSDT", 44000, 100)) // -12% vs avg → catastrophic stop

	if len(broker.orders) <= n0 {
		t.Fatal("expected a flatten order when inventory loss exceeds StopLossPct")
	}
	last := broker.orders[len(broker.orders)-1]
	if last.Side != strategy.SideSell || last.Qty != 0.03 {
		t.Errorf("stop should SELL held qty 0.03, got side=%v qty=%v", last.Side, last.Qty)
	}
	if g.basePrice != 0 {
		t.Errorf("grid should re-center (basePrice=0) after stop, got %f", g.basePrice)
	}
}

// A small drawdown within StopLossPct must NOT trigger the stop.
func TestGrid_CatastrophicStopHoldsWithinThreshold(t *testing.T) {
	g := New(Config{Symbol: "BTCUSDT", GridLevels: 3, GridSpacing: 0.01, BaseQty: 0.01, StopLossPct: 0.10})
	log, _ := zap.NewDevelopment()
	broker := &mockBroker{}
	port := &mockPortfolio{cash: 10000}
	ctx := strategy.NewContext(port, broker, log)
	g.OnBar(ctx, barVol("BTCUSDT", 50000, 100)) // init

	port.qty, port.avg, port.hasPos = 0.03, 50000, true
	base := g.basePrice
	g.OnBar(ctx, barVol("BTCUSDT", 48000, 100)) // -4% vs avg, within 10% → no stop

	if g.basePrice != base {
		t.Errorf("no stop within threshold → grid should NOT re-center (base %f -> %f)", base, g.basePrice)
	}
}

func TestPctile(t *testing.T) {
	cases := []struct {
		val    float64
		sample []float64
		want   float64
	}{
		{5, []float64{1, 2, 3, 4, 5}, 1.0}, // largest → all <=
		{3, []float64{1, 2, 3, 4, 5}, 0.6}, // 3 of 5 <= 3
		{0, []float64{1, 2, 3}, 0.0},       // below all
		{2, []float64{2, 2, 2}, 1.0},       // ties count as <=
		{1, nil, 0.5},                      // empty → neutral
	}
	for _, c := range cases {
		if got := pctile(c.val, c.sample); got != c.want {
			t.Errorf("pctile(%v,%v)=%v want %v", c.val, c.sample, got, c.want)
		}
	}
}

// step() is the 3-mechanism state machine: hysteresis (exit>=0.70 / enter<0.40),
// cooldown (wait N bars after exit), persistence (N consecutive lows to re-enter).
func TestVolGateStepHysteresis(t *testing.T) {
	g := newVolGate(volGateCfg{ExitThresh: 0.70, EnterThresh: 0.40, Cooldown: 0, Persistence: 1})
	// starts active
	if !g.step(0.50) {
		t.Fatal("should start active on a mid score")
	}
	// mid score (between enter and exit) keeps it active — hysteresis dead band
	if !g.step(0.60) {
		t.Fatal("0.60 is below exit 0.70 → stays active")
	}
	// score >= exit → gate OFF
	if g.step(0.75) {
		t.Fatal("0.75 >= exit 0.70 → should gate OFF")
	}
	// while off, a score between enter and exit does NOT re-enter (must be < enter)
	if g.step(0.50) {
		t.Fatal("0.50 not < enter 0.40 → stays off")
	}
	// score < enter → re-enter (cooldown 0, persistence 1)
	if !g.step(0.30) {
		t.Fatal("0.30 < enter 0.40 with cooldown0/persist1 → re-enter")
	}
}

func TestVolGateStepCooldownAndPersistence(t *testing.T) {
	g := newVolGate(volGateCfg{ExitThresh: 0.70, EnterThresh: 0.40, Cooldown: 3, Persistence: 2})
	g.step(0.80) // exit → off
	// low scores: need cooldown>=3 AND persistence>=2 consecutive lows
	if g.step(0.20) { // sinceExit1 low1
		t.Fatal("cooldown not met (1<3)")
	}
	if g.step(0.20) { // sinceExit2 low2
		t.Fatal("cooldown not met (2<3)")
	}
	if !g.step(0.20) { // sinceExit3 low3 → both satisfied
		t.Fatal("cooldown3 & persistence2 satisfied → re-enter")
	}
}

func TestVolGateStepPersistenceResetsOnHigh(t *testing.T) {
	g := newVolGate(volGateCfg{ExitThresh: 0.70, EnterThresh: 0.40, Cooldown: 0, Persistence: 3})
	g.step(0.80)      // off
	g.step(0.30)      // low1
	g.step(0.30)      // low2
	if g.step(0.90) { // high → resets low streak, stays off
		t.Fatal("high score mid-recovery must keep it off")
	}
	if g.step(0.30) { // low1 again
		t.Fatal("streak reset → 1 low insufficient (<3)")
	}
	g.step(0.30)       // low2
	if !g.step(0.30) { // low3 → re-enter
		t.Fatal("3 consecutive lows → re-enter")
	}
}

func TestVolGateUpdateWarmupActive(t *testing.T) {
	g := newVolGate(volGateCfg{Window: 50, RatioBars: 8, ExitThresh: 0.70, EnterThresh: 0.40})
	// before Window samples, gate must stay active (can't judge volatility yet)
	for i := 0; i < 40; i++ {
		if !g.update(100) {
			t.Fatalf("bar %d in warmup should be active", i)
		}
	}
}

func TestVolGateUpdateSpikeGatesOff(t *testing.T) {
	g := newVolGate(volGateCfg{Window: 50, RatioBars: 8, ExitThresh: 0.70, EnterThresh: 0.40, Cooldown: 1, Persistence: 1})
	// feed a long calm baseline
	for i := 0; i < 60; i++ {
		g.update(100)
	}
	// a big volume spike → high vol_hi & vol_up → score should exceed exit → OFF
	if g.update(100000) {
		t.Fatal("a huge volume spike should gate the grid OFF")
	}
}
