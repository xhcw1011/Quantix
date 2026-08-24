package macross

import (
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

func htfBar(interval string, close float64) exchange.Kline {
	return exchange.Kline{
		Symbol: "BTCUSDT", Interval: interval,
		Close: close, High: close + 0.1, Low: close - 0.1,
		IsClosed: true,
	}
}

// bullishHTF/bearishHTF seed m.htfCloses with a monotonic run long enough for
// the default 20/60 SMA pair to read a confident direction.
func bullishHTF(m *MACross) {
	c := 100.0
	for i := 0; i < 65; i++ {
		c += 1
		m.htfCloses = append(m.htfCloses, c)
	}
}

func bearishHTF(m *MACross) {
	c := 200.0
	for i := 0; i < 65; i++ {
		c -= 1
		m.htfCloses = append(m.htfCloses, c)
	}
}

// TestMACross_HTFGate_BlocksReversalAgainstHTFTrend is the core mechanism:
// a death cross tries to close a profitable LONG and open SHORT, but the
// HTF is still bullish -- the reversal must be ignored outright (leg keeps
// riding untouched), not just delayed.
func TestMACross_HTFGate_BlocksReversalAgainstHTFTrend(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		HTFInterval: "1h",
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	bullishHTF(m) // HTF still trending up

	deathFast := []float64{100, 95}
	deathSlow := []float64{100, 100}
	m.onBarHedge(ctx, scaleOutBar(95), deathFast, deathSlow)

	if len(broker.reqs) != 0 {
		t.Fatalf("death cross against a bullish HTF must be ignored, got %+v", broker.reqs)
	}
}

// TestMACross_HTFGate_AllowsReversalWithHTFTrend confirms the gate isn't a
// blanket freeze: once the HTF genuinely agrees with the reversal direction,
// the cross_reversal close+flip fires normally.
func TestMACross_HTFGate_AllowsReversalWithHTFTrend(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		HTFInterval: "1h",
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	bearishHTF(m) // HTF has genuinely turned down too

	deathFast := []float64{100, 95}
	deathSlow := []float64{100, 100}
	m.onBarHedge(ctx, scaleOutBar(95), deathFast, deathSlow)

	if len(broker.reqs) == 0 {
		t.Fatal("death cross agreeing with a bearish HTF must fire the reversal, got no orders")
	}
	if broker.reqs[0].Reason != "cross_reversal" {
		t.Fatalf("expected a cross_reversal close order, got %+v", broker.reqs[0])
	}
	if broker.reqs[0].Side != strategy.SideSell || broker.reqs[0].PositionSide != strategy.PositionSideLong {
		t.Fatalf("expected a SELL LONG close order, got %+v", broker.reqs[0])
	}
}

// TestMACross_HTFGate_DisabledByDefault confirms this is strictly additive:
// an engine that never sets HTFInterval reverses on every cross exactly like
// before, regardless of what htfCloses would say (it stays empty anyway).
func TestMACross_HTFGate_DisabledByDefault(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	deathFast := []float64{100, 95}
	deathSlow := []float64{100, 100}
	m.onBarHedge(ctx, scaleOutBar(95), deathFast, deathSlow)

	if len(broker.reqs) == 0 {
		t.Fatal("with HTFInterval unset the reversal must fire exactly as before, got no orders")
	}
}

// TestMACross_HTFGate_FailsOpenWithoutEnoughHTFData confirms the gate never
// blocks a reversal just because the HTF buffer hasn't filled yet (e.g. right
// after startup, before 60 HTF bars have arrived) — insufficient data reads
// as "no opinion", not "disagree".
func TestMACross_HTFGate_FailsOpenWithoutEnoughHTFData(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		HTFInterval: "1h",
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})
	m.htfCloses = []float64{100, 101, 99} // far short of the 60-bar slow SMA

	deathFast := []float64{100, 95}
	deathSlow := []float64{100, 100}
	m.onBarHedge(ctx, scaleOutBar(95), deathFast, deathSlow)

	if len(broker.reqs) == 0 {
		t.Fatal("insufficient HTF data must fail open (reversal proceeds), got no orders")
	}
}

// TestMACross_HTFGate_NeverBlocksFlatEntry confirms the gate only touches
// reversals of an OPEN leg — an entry from flat always fires on any cross,
// unrestricted, matching "5m进场没问题".
func TestMACross_HTFGate_NeverBlocksFlatEntry(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		HTFInterval: "1h",
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)

	bullishHTF(m) // HTF bullish, but signal is a DEATH cross (would be "disagreeing" if this were a reversal)

	goldFast := []float64{100, 95} // actually build a death cross to open SHORT from flat
	goldSlow := []float64{100, 100}
	m.onBarHedge(ctx, scaleOutBar(95), goldFast, goldSlow)

	if len(broker.reqs) == 0 {
		t.Fatal("an entry from flat must never be blocked by the HTF gate, got no orders")
	}
}

// TestMACross_HTFGate_MinSpreadTreatsNarrowGapAsNeutral confirms the "big
// cycle vs small cycle" hybrid: a decisive-direction but narrow HTF spread
// (below HTFMinSpreadPct) reads as neutral, so it fails open and defers to
// AsymmetricExit's own profitability check instead of vetoing the reversal.
func TestMACross_HTFGate_MinSpreadTreatsNarrowGapAsNeutral(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		AsymmetricExit: true, MinProfitToClosePct: 0,
		HTFInterval: "1h", HTFMinSpreadPct: 0.10, // require a 10% spread to count as decisive
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	// A gentle, small ramp: fast > slow (mildly bullish) but the spread stays
	// well under the 10% threshold — unlike bullishHTF's steep +1/bar ramp.
	c := 100.0
	for i := 0; i < 65; i++ {
		c += 0.02
		m.htfCloses = append(m.htfCloses, c)
	}

	// Death cross while the leg is profitable (bar.Close=105 > entry=100):
	// AsymmetricExit alone would allow this reversal. The HTF is technically
	// bullish (disagreeing) but its spread is too narrow to count as
	// decisive, so it must NOT block it.
	deathFast := []float64{102, 98}
	deathSlow := []float64{100, 100}
	m.onBarHedge(ctx, scaleOutBar(105), deathFast, deathSlow)

	if len(broker.reqs) == 0 {
		t.Fatal("a narrow HTF spread below HTFMinSpreadPct must defer to AsymmetricExit, not block the reversal")
	}
}

// TestMACross_HTFGate_MinSpreadStillBlocksOnDecisiveTrend confirms the
// threshold doesn't just disable the gate outright — a spread that clears
// HTFMinSpreadPct still actively vetoes a disagreeing reversal.
func TestMACross_HTFGate_MinSpreadStillBlocksOnDecisiveTrend(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		HTFInterval: "1h", HTFMinSpreadPct: 0.10,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	m.OnFill(ctx, strategy.Fill{Symbol: "BTCUSDT", Side: strategy.SideBuy, PositionSide: strategy.PositionSideLong, Qty: 1.0, Price: 100})

	// Force a decisive spread well past 10%: fast far above slow.
	m.htfCloses = nil
	for i := 0; i < 59; i++ {
		m.htfCloses = append(m.htfCloses, 100)
	}
	for i := 0; i < 20; i++ {
		m.htfCloses = append(m.htfCloses, 150) // pulls the 20-SMA well above the 60-SMA
	}

	deathFast := []float64{100, 95}
	deathSlow := []float64{100, 100}
	m.onBarHedge(ctx, scaleOutBar(95), deathFast, deathSlow)

	if len(broker.reqs) != 0 {
		t.Fatalf("a decisive HTF spread past HTFMinSpreadPct must still block a disagreeing reversal, got %+v", broker.reqs)
	}
}

// TestMACross_HTFGate_OnBarRoutesHTFBarsToBuffer is the OnBar-level wiring
// test: bars tagged with Config.HTFInterval must feed htfCloses and return
// immediately, without touching primary-interval state (sawWarmup, pending
// entries, position bookkeeping).
func TestMACross_HTFGate_OnBarRoutesHTFBarsToBuffer(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		HTFInterval: "1h",
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)

	m.OnBar(ctx, htfBar("1h", 101))
	m.OnBar(ctx, htfBar("1h", 102))

	if len(m.htfCloses) != 2 {
		t.Fatalf("expected 2 HTF closes buffered, got %d: %v", len(m.htfCloses), m.htfCloses)
	}
	if m.sawWarmup {
		t.Fatal("an HTF bar must not set sawWarmup — that belongs to primary-interval bars only")
	}
	if len(m.closes) != 0 {
		t.Fatalf("an HTF bar must not be appended to the primary close buffer, got %v", m.closes)
	}
}

// TestMACross_HTFGate_PrimaryBarsDontPolluteHTFBuffer is the inverse wiring
// check: ordinary primary-interval bars must not leak into htfCloses.
func TestMACross_HTFGate_PrimaryBarsDontPolluteHTFBuffer(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30, EnableShort: true, TrendFilterMin: 0,
		HTFInterval: "1h",
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)

	m.OnBar(ctx, scaleOutBar(100)) // Interval: "5m"

	if len(m.htfCloses) != 0 {
		t.Fatalf("a 5m primary bar must not be routed into htfCloses, got %v", m.htfCloses)
	}
}
