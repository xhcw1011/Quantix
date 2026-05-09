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
