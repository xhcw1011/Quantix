package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
)

// The deduper lets WS + REST-poll + backfill feed one channel without the engine
// ever seeing the same bar twice: only a strictly-newer open time per interval
// gets forwarded.
func TestKlineDeduperForwardsNewDropsDuplicate(t *testing.T) {
	out := make(chan exchange.Kline, 10)
	d := newKlineDeduper(out, zap.NewNop())
	bar := func(iv string, ms int64) exchange.Kline {
		return exchange.Kline{Interval: iv, OpenTime: time.UnixMilli(ms)}
	}
	require.True(t, d.emit(bar("15m", 1000)), "new bar forwarded")
	require.False(t, d.emit(bar("15m", 1000)), "duplicate open time dropped")
	require.False(t, d.emit(bar("15m", 500)), "older bar dropped")
	require.True(t, d.emit(bar("15m", 2000)), "newer bar forwarded")
	require.True(t, d.emit(bar("5m", 1000)), "different interval tracked separately")
	require.Len(t, out, 3)
}

type fakeKlineFetcher struct{ bars []exchange.Kline }

func (f *fakeKlineFetcher) GetKlines(_ context.Context, _, _ string, _ int) ([]exchange.Kline, error) {
	return f.bars, nil
}

// pollOnce must emit only CLOSED bars (close time already passed), skipping the
// still-forming current bar, and mark them IsClosed.
func TestPollOnceEmitsOnlyClosedBars(t *testing.T) {
	now := time.UnixMilli(10000)
	f := &fakeKlineFetcher{bars: []exchange.Kline{
		{Interval: "15m", OpenTime: time.UnixMilli(1000), CloseTime: time.UnixMilli(2000)},  // closed
		{Interval: "15m", OpenTime: time.UnixMilli(2000), CloseTime: time.UnixMilli(3000)},  // closed
		{Interval: "15m", OpenTime: time.UnixMilli(9000), CloseTime: time.UnixMilli(20000)}, // forming
	}}
	var got []exchange.Kline
	pollOnce(context.Background(), f, "BTCUSDT", "15m",
		func() time.Time { return now },
		func(k exchange.Kline) { got = append(got, k) }, zap.NewNop())

	require.Len(t, got, 2, "only the two closed bars emitted, not the forming one")
	require.True(t, got[0].IsClosed)
	require.True(t, got[1].IsClosed)
}
