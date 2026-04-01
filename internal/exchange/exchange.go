// Package exchange defines the core market data interfaces and types.
package exchange

import (
	"context"
	"time"
)

// RESTClient is the minimal interface for REST market data.
type RESTClient interface {
	GetKlines(ctx context.Context, symbol, interval string, limit int) ([]Kline, error)
	GetKlinesBetween(ctx context.Context, symbol, interval string, start, end time.Time, limit int) ([]Kline, error)
	GetServerTime(ctx context.Context) (time.Time, error)
}

// WSClient is the minimal interface for WebSocket market data streams.
// Both methods are blocking and run until ctx is cancelled.
type WSClient interface {
	SubscribeKlines(ctx context.Context, symbols, intervals []string, handler KlineHandler)
	SubscribeTickers(ctx context.Context, symbols []string, handler TickerHandler)
}
