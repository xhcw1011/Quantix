package sentiment

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// BinanceLiquidationWS subscribes to Binance Futures !forceOrder@arr public
// websocket and aggregates events into 5-minute buckets per symbol in memory.
// Buckets older than retention (default 1h) are pruned.
type BinanceLiquidationWS struct {
	URL       string
	BucketDur time.Duration // default 5 min
	Retention time.Duration // default 1 h
	log       *zap.Logger

	mu      sync.RWMutex
	buckets map[string]map[int64]*LiquidationBucket // symbol → bucketStart(unix) → bucket
}

// NewBinanceLiquidationWS constructs the collector. Call Start to begin
// consuming the WS. URL defaults to the public Binance Futures endpoint.
func NewBinanceLiquidationWS(log *zap.Logger) *BinanceLiquidationWS {
	return &BinanceLiquidationWS{
		URL:       "wss://fstream.binance.com/ws/!forceOrder@arr",
		BucketDur: 5 * time.Minute,
		Retention: 1 * time.Hour,
		log:       log,
		buckets:   make(map[string]map[int64]*LiquidationBucket),
	}
}

// Start launches the WS consumer in a goroutine. Reconnects automatically
// on disconnect. Stops when ctx is done.
func (b *BinanceLiquidationWS) Start(ctx context.Context) {
	go b.run(ctx)
}

func (b *BinanceLiquidationWS) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := b.consume(ctx)
		if ctx.Err() != nil {
			return
		}
		b.log.Warn("liquidation WS disconnected, retrying in 5s", zap.Error(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (b *BinanceLiquidationWS) consume(ctx context.Context) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, b.URL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	b.log.Info("liquidation WS connected", zap.String("url", b.URL))

	// Binance keeps the connection alive via server-side pings every 3 min.
	// We respond by extending the read deadline whenever a ping arrives.
	// forceOrder@arr is a sparse stream (liquidations are rare), so a short
	// read deadline would falsely timeout the connection. 10 min covers the
	// 3-min ping interval with margin; gorilla's default PongHandler also
	// resets the deadline, so we just need the initial value to be lenient.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
	conn.SetPingHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		// Extend deadline on every successful read (covers cases where
		// ping handler doesn't fire because data is flowing).
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
		b.handle(msg)
	}
}

// forceOrderMsg matches Binance forceOrder push payload.
type forceOrderMsg struct {
	Event string `json:"e"`
	O     struct {
		Symbol   string `json:"s"`
		Side     string `json:"S"` // "SELL" or "BUY"
		Qty      string `json:"q"`
		AvgPrice string `json:"ap"`
		Time     int64  `json:"T"`
	} `json:"o"`
}

func (b *BinanceLiquidationWS) handle(raw []byte) {
	var m forceOrderMsg
	if err := json.Unmarshal(raw, &m); err != nil {
		return // tolerate junk
	}
	if m.Event != "forceOrder" || m.O.Symbol == "" {
		return
	}
	qty, err := strconv.ParseFloat(m.O.Qty, 64)
	if err != nil {
		return
	}
	avg, err := strconv.ParseFloat(m.O.AvgPrice, 64)
	if err != nil {
		return
	}
	notional := qty * avg
	if notional <= 0 {
		return
	}
	bucketStart := time.UnixMilli(m.O.Time).Truncate(b.BucketDur)

	b.mu.Lock()
	defer b.mu.Unlock()
	symBuckets, ok := b.buckets[m.O.Symbol]
	if !ok {
		symBuckets = make(map[int64]*LiquidationBucket)
		b.buckets[m.O.Symbol] = symBuckets
	}
	key := bucketStart.Unix()
	bkt, ok := symBuckets[key]
	if !ok {
		bkt = &LiquidationBucket{StartTime: bucketStart}
		symBuckets[key] = bkt
	}
	switch m.O.Side {
	case "SELL":
		bkt.LongLiqUSD += notional
	case "BUY":
		bkt.ShortLiqUSD += notional
	}

	// Prune old buckets
	cutoff := time.Now().Add(-b.Retention).Unix()
	for k := range symBuckets {
		if k < cutoff {
			delete(symBuckets, k)
		}
	}
}

// RecentBuckets returns completed buckets in the window, most recent first.
func (b *BinanceLiquidationWS) RecentBuckets(symbol string, since, until time.Time) []LiquidationBucket {
	b.mu.RLock()
	defer b.mu.RUnlock()
	symBuckets, ok := b.buckets[symbol]
	if !ok {
		return nil
	}
	var out []LiquidationBucket
	for _, bkt := range symBuckets {
		if !bkt.StartTime.Before(since) && bkt.StartTime.Before(until) {
			out = append(out, *bkt)
		}
	}
	return out
}
