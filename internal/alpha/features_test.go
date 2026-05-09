package alpha

import (
	"testing"
	"time"

	"github.com/Quantix/quantix/internal/exchange"
)

func TestBuildFeatures_RecentBars(t *testing.T) {
	bars := make([]exchange.Kline, 30)
	for i := range bars {
		bars[i] = exchange.Kline{
			OpenTime: time.Unix(int64(i*300), 0),
			Open:     100 + float64(i),
			High:     101 + float64(i),
			Low:      99 + float64(i),
			Close:    100.5 + float64(i),
			Volume:   1.0,
		}
	}
	f := BuildFeatures("ETHUSDT", bars)
	if f.Symbol != "ETHUSDT" {
		t.Fatalf("Symbol not set: %s", f.Symbol)
	}
	if f.Close != bars[len(bars)-1].Close {
		t.Fatalf("Close mismatch: got %f want %f", f.Close, bars[len(bars)-1].Close)
	}
	if f.High10 == 0 {
		t.Fatalf("High10 not computed")
	}
	if f.ATR == 0 {
		t.Fatalf("ATR not computed")
	}
}

func TestBuildFeatures_TooFewBars_ReturnsZero(t *testing.T) {
	bars := []exchange.Kline{{Close: 100}}
	f := BuildFeatures("ETHUSDT", bars)
	if f.ATR != 0 {
		t.Fatalf("ATR should be 0 for insufficient bars, got %f", f.ATR)
	}
}

// TestBuildFeatures_High10ExclusiveOfCurrent pins the contract that High10
// reflects the rolling 10-bar high BEFORE the current bar — never the current
// close. A Breakout alpha gates on Close > High10; this test ensures that
// gate is achievable when the current bar makes a new high.
func TestBuildFeatures_High10ExclusiveOfCurrent(t *testing.T) {
	// 30 prior bars at high=110, low=90, close=100; current bar close=200
	// (way above prior 10 highs).  High10 must be 110, NOT 200.
	bars := make([]exchange.Kline, 30)
	for i := 0; i < 29; i++ {
		bars[i] = exchange.Kline{
			OpenTime: time.Unix(int64(i*300), 0),
			Open:     100, High: 110, Low: 90, Close: 100, Volume: 1.0,
		}
	}
	bars[29] = exchange.Kline{
		OpenTime: time.Unix(int64(29*300), 0),
		Open:     200, High: 200, Low: 200, Close: 200, Volume: 1.0,
	}
	f := BuildFeatures("ETHUSDT", bars)
	if f.High10 != 110 {
		t.Fatalf("High10 leaked current bar: got %f want 110 (prior 10-bar high)", f.High10)
	}
	if f.Low10 != 90 {
		t.Fatalf("Low10 leaked: got %f want 90", f.Low10)
	}
	// Symmetry check: current close BELOW all prior lows
	bars[29] = exchange.Kline{
		OpenTime: time.Unix(int64(29*300), 0),
		Open:     50, High: 50, Low: 50, Close: 50, Volume: 1.0,
	}
	f2 := BuildFeatures("ETHUSDT", bars)
	if f2.High10 != 110 {
		t.Fatalf("High10 (low-current case): got %f want 110", f2.High10)
	}
	if f2.Low10 != 90 {
		t.Fatalf("Low10 leaked current bar: got %f want 90", f2.Low10)
	}
}
