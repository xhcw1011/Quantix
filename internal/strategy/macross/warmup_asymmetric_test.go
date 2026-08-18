package macross

import (
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// flatThenSpikeBars builds a bar series that is perfectly FLAT at price p
// (fast SMA == slow SMA throughout, so crossDir never fires — isolating this
// test from cross_reversal behaviour entirely) except for a single bar at
// index spikeAt whose Close jumps to spikeTo before returning to p. Because
// FastPeriod(10) and SlowPeriod(30) both fully absorb the flat run before and
// after, the transient spike never pushes fast strictly past slow by the end
// of the series, so crossDir stays 0 for every bar including the live one.
func flatThenSpikeBars(symbol string, n, spikeAt int, p, spikeTo float64) []exchange.Kline {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]exchange.Kline, n)
	for i := 0; i < n; i++ {
		c := p
		if i == spikeAt {
			c = spikeTo
		}
		bars[i] = exchange.Kline{
			Symbol: symbol, Interval: "5m",
			OpenTime:  t0.Add(time.Duration(i) * 5 * time.Minute),
			CloseTime: t0.Add(time.Duration(i+1) * 5 * time.Minute),
			Open:      c, High: c + 0.5, Low: c - 0.5, Close: c, Volume: 1000, IsClosed: true,
		}
	}
	return bars
}

// TestMACross_WarmupBarsDoNotPolluteTrailingPeak reproduces the real
// 2026-08-18 incident: a restart mid-hold seeded a LONG (via reconcilePosition,
// entry=64126.80) whose warmup replay then walked through unrelated HISTORICAL
// bars — one of which (part of the ordinary backfill window, nothing to do
// with this leg's real price journey) had a close far above the real entry.
// onBarHedge calls manageAsymmetricExit unconditionally whenever a position is
// open, with NO bar.Warmup guard (unlike primeDirection, which explicitly
// takes isWarmup and refuses to act during warmup) — so that single warmup
// bar's inflated floatingPnLPct ratcheted m.posPeakFP up to a level the real
// position never actually reached. On the very first LIVE bar afterward, with
// real floating profit still tiny (nowhere near TrailActivatePct on its own),
// shouldTrailClose saw a huge "giveback" from the bogus warmup peak and fired
// a full close — netting a real loss after round-trip fees despite the
// position's true peak (computed only over bars that actually happened during
// its real holding period) never reaching TrailActivatePct at all.
func TestMACross_WarmupBarsDoNotPolluteTrailingPeak(t *testing.T) {
	const entry = 64126.80
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30,
		EnableShort: true, TrendFilterMin: 0,
		AsymmetricExit: true, MinProfitToClosePct: 0.001,
		TrailActivatePct: 0.003, TrailGivebackFrac: 0.05, // live config as of 2026-08-18
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	ctx.Extra["position_syncer"] = fakeSyncer{long: true, longEntry: entry, longQty: 0.009}

	// 40 warmup bars, flat at `entry` except bar 35, which spikes to +0.5%
	// above entry — comfortably clearing TrailActivatePct=0.3%, but this bar
	// is warmup: it never happened during this leg's real life. Bar 35 (not
	// earlier) matters: manageAsymmetricExit only starts running once
	// len(m.closes) reaches SlowPeriod(30), so a spike before bar index 29
	// would never actually reach floatingPnLPct/posPeakFP at all.
	bars := flatThenSpikeBars("BTCUSDT", 41, 35, entry, entry*1.005)
	for i := 0; i < 40; i++ {
		bars[i].Warmup = true
		m.OnBar(ctx, bars[i])
	}
	broker.reqs = nil

	// First live bar: real floating profit is a tiny +0.065% (matches the
	// actual 2026-08-18 incident) — nowhere near TrailActivatePct=0.3% on its
	// own, and no reverse cross occurs (flat series).
	bars[40].Warmup = false
	bars[40].Close = entry * 1.00065
	bars[40].High = bars[40].Close + 0.5
	bars[40].Low = bars[40].Close - 0.5
	m.OnBar(ctx, bars[40])

	for _, req := range broker.reqs {
		if req.Reason == "trail_giveback" {
			t.Fatalf("a warmup bar's price must not be able to trigger a real trail-close on the first live bar (peak was never legitimately reached during this leg's real holding period): got %+v", req)
		}
	}
}
