// Package exchange defines domain types and interfaces for interacting with
// cryptocurrency exchanges.
package exchange

import "time"

// Kline represents a single OHLCV candlestick.
type Kline struct {
	Symbol    string
	Interval  string
	OpenTime  time.Time
	CloseTime time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	// Quote asset volume (e.g. USDT volume)
	QuoteVolume float64
	// Number of trades during the interval
	NumTrades int64
	// Whether the candle is closed (vs. in-progress)
	IsClosed bool
}

// Ticker represents the best bid/ask and last price for a symbol.
type Ticker struct {
	Symbol    string
	BidPrice  float64
	AskPrice  float64
	LastPrice float64
	Volume    float64
	Timestamp time.Time
}

// Interval constants matching Binance API values.
const (
	Interval1m  = "1m"
	Interval3m  = "3m"
	Interval5m  = "5m"
	Interval15m = "15m"
	Interval30m = "30m"
	Interval1h  = "1h"
	Interval2h  = "2h"
	Interval4h  = "4h"
	Interval6h  = "6h"
	Interval8h  = "8h"
	Interval12h = "12h"
	Interval1d  = "1d"
	Interval3d  = "3d"
	Interval1w  = "1w"
	Interval1M  = "1M"
)
