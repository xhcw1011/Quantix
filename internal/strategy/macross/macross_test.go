package macross

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/backtest"
	"github.com/Quantix/quantix/internal/exchange"
)

// buildKlines generates a synthetic price series.
// It alternates between uptrend and downtrend to trigger crossovers.
func buildKlines(symbol string, prices []float64) []exchange.Kline {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]exchange.Kline, len(prices))
	for i, p := range prices {
		bars[i] = exchange.Kline{
			Symbol:    symbol,
			Interval:  "1h",
			OpenTime:  t0.Add(time.Duration(i) * time.Hour),
			CloseTime: t0.Add(time.Duration(i+1) * time.Hour),
			Open:      p,
			High:      p * 1.005,
			Low:       p * 0.995,
			Close:     p,
			Volume:    1000,
			IsClosed:  true,
		}
	}
	return bars
}

// syntheticPrices creates a price series guaranteed to trigger at least one
// MA crossover: a downtrend (so fast SMA < slow SMA) followed by a sharp
// uptrend (so fast SMA crosses above slow SMA — golden cross).
func syntheticPrices(n int) []float64 {
	prices := make([]float64, n)
	half := n / 2
	// Phase 1: downtrend — fast SMA falls below slow SMA
	for i := 0; i < half; i++ {
		prices[i] = 150 - float64(i)*0.4
	}
	// Phase 2: strong uptrend — fast SMA crosses above slow SMA (golden cross)
	base := prices[half-1]
	for i := half; i < n; i++ {
		prices[i] = base + float64(i-half)*0.8
	}
	return prices
}

func TestMACross_Name(t *testing.T) {
	m := New(Config{Symbol: "BTCUSDT", FastPeriod: 5, SlowPeriod: 20})
	assert.Equal(t, "MACross(5,20)", m.Name())
}

func TestMACross_NoSignalBeforeWarmup(t *testing.T) {
	// With only 5 bars and slow=20, no signal should fire
	prices := make([]float64, 5)
	for i := range prices {
		prices[i] = float64(100 + i)
	}
	klines := buildKlines("BTCUSDT", prices)

	log, _ := zap.NewDevelopment()
	engine := backtest.New(backtest.Config{
		Symbol:         "BTCUSDT",
		Interval:       "1h",
		InitialCapital: 10_000,
		FeeRate:        0.001,
		Klines:         klines,
	}, nil, New(Config{Symbol: "BTCUSDT", FastPeriod: 5, SlowPeriod: 20}), log)

	report, err := engine.Run(context.Background())
	require.NoError(t, err)
	// Not enough bars for SMA(20) — no trades should fire
	assert.Equal(t, 0, report.TotalTrades)
}

func TestMACross_GeneratesTrades(t *testing.T) {
	// 200-bar series with clear trend changes
	prices := syntheticPrices(200)
	klines := buildKlines("BTCUSDT", prices)

	log, _ := zap.NewDevelopment()
	engine := backtest.New(backtest.Config{
		Symbol:         "BTCUSDT",
		Interval:       "1h",
		InitialCapital: 10_000,
		FeeRate:        0.001,
		Slippage:       0.0005,
		Klines:         klines,
	}, nil, New(Config{Symbol: "BTCUSDT", FastPeriod: 5, SlowPeriod: 20}), log)

	report, err := engine.Run(context.Background())
	require.NoError(t, err)
	// Should have generated at least one trade
	assert.GreaterOrEqual(t, report.TotalTrades, 1)
	// Report fields should be populated
	assert.Equal(t, 10_000.0, report.InitialCapital)
	assert.Greater(t, report.FinalEquity, 0.0)
}

func TestMACross_EquityCurveLength(t *testing.T) {
	prices := syntheticPrices(100)
	klines := buildKlines("BTCUSDT", prices)

	log, _ := zap.NewDevelopment()
	engine := backtest.New(backtest.Config{
		Symbol:         "BTCUSDT",
		Interval:       "1h",
		InitialCapital: 10_000,
		Klines:         klines,
	}, nil, New(Config{Symbol: "BTCUSDT", FastPeriod: 5, SlowPeriod: 20}), log)

	report, err := engine.Run(context.Background())
	require.NoError(t, err)
	// Equity curve = initial point + one point per bar
	assert.Equal(t, len(prices)+1, len(report.EquityCurve))
}
