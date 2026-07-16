package macross

import (
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
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
type fakeSyncer struct{ long, short bool }

func (f fakeSyncer) HasPosition(side string) bool {
	if side == "LONG" {
		return f.long
	}
	return f.short
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
