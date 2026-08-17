package macross

import (
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
)

// captureBroker records every order the strategy places.
type captureBroker struct{ reqs []strategy.OrderRequest }

func (b *captureBroker) PlaceOrder(req strategy.OrderRequest) string {
	b.reqs = append(b.reqs, req)
	return "OID-1"
}
func (b *captureBroker) CancelOrder(string) error { return nil }

// flatPV is a portfolio view that reports no one-way position (hedge legs live
// under LONG/SHORT keys, invisible to PortfolioView.Position — which is exactly
// why macross must consult the syncer, not the portfolio, on restart).
type flatPV struct{}

func (flatPV) Cash() float64                            { return 10_000 }
func (flatPV) Position(string) (float64, float64, bool) { return 0, 0, false }
func (flatPV) Equity(map[string]float64) float64        { return 10_000 }

// fakeSyncer satisfies the positionReporter interface macross reads from
// ctx.Extra["position_syncer"].
type fakeSyncer struct {
	long, short          bool
	longEntry, longQty   float64
	shortEntry, shortQty float64
}

func (f fakeSyncer) HasPosition(side string) bool {
	if side == "LONG" {
		return f.long
	}
	return f.short
}

func (f fakeSyncer) GetLong() *position.StrategyPosition {
	if !f.long {
		return nil
	}
	p := &position.StrategyPosition{}
	p.EntryPrice = f.longEntry
	p.Qty = f.longQty
	return p
}

func (f fakeSyncer) GetShort() *position.StrategyPosition {
	if !f.short {
		return nil
	}
	p := &position.StrategyPosition{}
	p.EntryPrice = f.shortEntry
	p.Qty = f.shortQty
	return p
}

// downtrendBars builds a pure linear decline so fast SMA stays strictly below
// slow SMA (fast<slow) and they never cross — no crossover orders fire, so the
// only possible order is the post-warmup priming entry.
func downtrendBars(symbol string, n int) []exchange.Kline {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]exchange.Kline, n)
	for i := 0; i < n; i++ {
		p := 200.0 - float64(i) // strictly decreasing
		bars[i] = exchange.Kline{
			Symbol: symbol, Interval: "1h",
			OpenTime:  t0.Add(time.Duration(i) * time.Hour),
			CloseTime: t0.Add(time.Duration(i+1) * time.Hour),
			Open:      p, High: p + 0.5, Low: p - 0.5, Close: p, Volume: 1000, IsClosed: true,
		}
	}
	return bars
}

// runReconcileScenario feeds warmup bars (Warmup=true) then one live bar, with
// the given syncer injected, and returns the orders placed.
func runReconcileScenario(t *testing.T, syncer any) []strategy.OrderRequest {
	t.Helper()
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30,
		EnableShort: true, TrendFilterMin: 0, // hedge mode, trend filter off for determinism
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	if syncer != nil {
		ctx.Extra["position_syncer"] = syncer
	}

	bars := downtrendBars("BTCUSDT", 41)
	// Replay 40 warmup bars, then clear captured orders — the real engine's broker
	// suppresses order execution during warmup, so only the live bar's behaviour is
	// what matters for the restart re-entry bug.
	for i := 0; i < 40; i++ {
		bars[i].Warmup = true
		m.OnBar(ctx, bars[i])
	}
	broker.reqs = nil
	bars[40].Warmup = false
	m.OnBar(ctx, bars[40])
	return broker.reqs
}

// TestMACross_HedgeSkipsPrimingWhenSyncerHasPosition is the regression test for
// "每次部署 demo 就重新下单": on restart the in-memory hasLong/hasShort reset to
// false, so priming re-opened the position even though the account already held
// it. Reconciling from the syncer before priming must prevent that.
func TestMACross_HedgeSkipsPrimingWhenSyncerHasPosition(t *testing.T) {
	reqs := runReconcileScenario(t, fakeSyncer{short: true})
	if len(reqs) != 0 {
		t.Fatalf("expected NO order when syncer already reports a SHORT (must not re-open on restart), got %d: %+v", len(reqs), reqs)
	}
}

// TestMACross_ReconcileRestoresEntryForAsymmetricExit reproduces the
// 2026-08-06 finding: reconcilePosition seeded hasLong/hasShort from the
// syncer but never posEntry/posQty, which OnFill is otherwise the only writer
// of. Left at their zero-value after a restart, floatingPnLPct() is pinned at
// 0 forever, so under AsymmetricExit a reverse cross can never close the
// position again (it only closes when floatingPnLPct() > 0) no matter how
// profitable it actually is.
func TestMACross_ReconcileRestoresEntryForAsymmetricExit(t *testing.T) {
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30,
		EnableShort: true, AsymmetricExit: true, TrendFilterMin: 0,
	})
	ctx := strategy.NewContext(flatPV{}, &captureBroker{}, zap.NewNop())
	ctx.Extra["position_syncer"] = fakeSyncer{short: true, shortEntry: 100, shortQty: 2}

	m.reconcilePosition(ctx)

	if m.posEntry != 100 {
		t.Fatalf("expected posEntry restored to 100 from the syncer, got %v", m.posEntry)
	}
	if m.posQty != 2 {
		t.Fatalf("expected posQty restored to 2 from the syncer, got %v", m.posQty)
	}
	// Price fell well below the restored short entry → genuinely profitable.
	// Before the fix this returned 0 (posEntry==0 short-circuits floatingPnLPct),
	// which would keep a reverse cross from ever closing the leg again.
	if fp := m.floatingPnLPct(90); fp <= 0 {
		t.Fatalf("expected positive floating PnL after restoring entry, got %v", fp)
	}
}

// TestMACross_DoesNotRePrimeAfterOwnExitClosesRestartSeededPosition reproduces
// the 2026-08-14 incident: a SHORT position seeded from the syncer at restart
// closed via the trailing-giveback mechanism minutes into live trading, and
// the post-warmup priming logic — seeing the account flat again with the
// trend still favoring short — immediately re-opened a new short, undoing the
// giveback's intentional profit-take.
func TestMACross_DoesNotRePrimeAfterOwnExitClosesRestartSeededPosition(t *testing.T) {
	log := zap.NewNop()
	m := New(Config{
		Symbol: "BTCUSDT", FastPeriod: 10, SlowPeriod: 30,
		EnableShort: true, TrendFilterMin: 0,
	})
	broker := &captureBroker{}
	ctx := strategy.NewContext(flatPV{}, broker, log)
	ctx.Extra["position_syncer"] = fakeSyncer{short: true, shortEntry: 100, shortQty: 2}

	bars := downtrendBars("BTCUSDT", 42)
	for i := 0; i < 40; i++ {
		bars[i].Warmup = true
		m.OnBar(ctx, bars[i])
	}
	// First live bar: still short (as seeded from the syncer) — hadPosition
	// must latch true, and (matching TestMACross_HedgeSkipsPrimingWhenSyncerHasPosition)
	// no order fires since we're not flat.
	bars[40].Warmup = false
	m.OnBar(ctx, bars[40])
	if !m.hadPosition {
		t.Fatalf("hadPosition should have latched true once the restart-seeded SHORT was observed")
	}
	if len(broker.reqs) != 0 {
		t.Fatalf("expected no order on the bar that merely observes the seeded position, got %+v", broker.reqs)
	}

	// Simulate the trailing-giveback mechanism (covered separately by exit.go's
	// own unit tests) having just closed the position — the account is flat.
	m.hasShort = false
	broker.reqs = nil

	// Next live bar: trend still favors short (monotonic downtrend) — priming
	// must NOT re-open, since hadPosition is now true.
	bars[41].Warmup = false
	m.OnBar(ctx, bars[41])
	if len(broker.reqs) != 0 {
		t.Fatalf("expected NO re-entry after our own exit closed a restart-seeded position, got %d orders: %+v", len(broker.reqs), broker.reqs)
	}
}

// TestMACross_HedgePrimesWhenGenuinelyFlat guards the intended behaviour: with no
// existing position (no syncer / empty syncer), priming still establishes the
// trend position after warmup (the original 补建仓 fix must be preserved).
func TestMACross_HedgePrimesWhenGenuinelyFlat(t *testing.T) {
	for _, tc := range []struct {
		name   string
		syncer any
	}{
		{"no syncer (e.g. backtest)", nil},
		{"empty syncer", fakeSyncer{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reqs := runReconcileScenario(t, tc.syncer)
			if len(reqs) != 1 {
				t.Fatalf("expected exactly 1 priming order when flat, got %d: %+v", len(reqs), reqs)
			}
			if reqs[0].PositionSide != strategy.PositionSideShort || reqs[0].Side != strategy.SideSell {
				t.Fatalf("expected an OpenShort priming order in a downtrend, got %+v", reqs[0])
			}
		})
	}
}
