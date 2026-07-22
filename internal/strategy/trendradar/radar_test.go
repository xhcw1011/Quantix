package trendradar

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// evaluateSignal is the core gate: fire only when the 15m histogram is decisively
// strong (|hist| > M×avg), the 4h agrees, and we haven't already fired that side.
func TestEvaluateSignal(t *testing.T) {
	const M = 1.0
	// decisive-up + 4h bull + not-yet-fired-long → fire golden (+1)
	require.Equal(t, 1, evaluateSignal(30, 10, M, true, 0))
	// already fired long → debounced
	require.Equal(t, 0, evaluateSignal(30, 10, M, true, 1))
	// 4h not bullish → golden suppressed (no alignment)
	require.Equal(t, 0, evaluateSignal(30, 10, M, false, 0))
	// histogram too weak (< M×avg) → no fire
	require.Equal(t, 0, evaluateSignal(5, 10, M, true, 0))
	// decisive-down + 4h bear + not-yet-fired-short → fire death (-1)
	require.Equal(t, -1, evaluateSignal(-30, 10, M, false, 0))
	// death but 4h bullish → suppressed
	require.Equal(t, 0, evaluateSignal(-30, 10, M, true, 0))
}

type fakeDispatcher struct{ n int }

func (f *fakeDispatcher) Send(_, _ string) error { f.n++; return nil }

func bar(iv string, close float64, warmup bool) exchange.Kline {
	return exchange.Kline{Symbol: "BTCUSDT", Interval: iv, Close: close, IsClosed: true, Warmup: warmup}
}

// A clear down-then-up 15m sequence with a rising 4h must (a) never alert on
// warmup-replayed bars, and (b) alert on a live decisive golden cross once.
func TestRadarAlertsLiveNotWarmup(t *testing.T) {
	feed := func(r *Radar, warmup bool) {
		ctx := &strategy.Context{Log: zap.NewNop()}
		// prime 4h with a steady rise → 4h bullish
		for i := 0; i < 60; i++ {
			r.OnBar(ctx, bar("4h", 100+float64(i), warmup))
		}
		// 15m: decline then a strong rally → golden cross with positive histogram
		for i := 0; i < 60; i++ {
			r.OnBar(ctx, bar("15m", 100-float64(i), warmup))
		}
		for i := 0; i < 60; i++ {
			r.OnBar(ctx, bar("15m", 40+float64(i)*2, warmup))
		}
	}

	// warmup: must not alert
	rw := New("BTCUSDT", "15m", "4h", 0, zap.NewNop())
	dw := &fakeDispatcher{}
	rw.SetDispatcher(dw)
	feed(rw, true)
	require.Equal(t, 0, dw.n, "no alerts on warmup-replayed bars")

	// live: must alert at least once on the decisive golden cross
	rl := New("BTCUSDT", "15m", "4h", 0, zap.NewNop())
	dl := &fakeDispatcher{}
	rl.SetDispatcher(dl)
	feed(rl, false)
	require.GreaterOrEqual(t, dl.n, 1, "a live decisive golden cross (4h aligned) should alert")
}
