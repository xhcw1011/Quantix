package data

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/monitor"
)

// Pipeline wires the WebSocket client to the data store.
// It handles backfilling historical data, then subscribes to real-time streams.
type Pipeline struct {
	store   *Store
	rest    exchange.RESTClient
	ws      exchange.WSClient
	metrics *monitor.Metrics
	log     *zap.Logger

	symbols       []string
	intervals     []string
	backfillLimit int

	// OnClosedKline is called (in the WS goroutine) after each closed kline is
	// successfully persisted to the database. Consumers must not block.
	OnClosedKline func(exchange.Kline)
}

// NewPipeline creates a data ingestion pipeline.
func NewPipeline(
	store *Store,
	rest exchange.RESTClient,
	ws exchange.WSClient,
	metrics *monitor.Metrics,
	symbols, intervals []string,
	backfillLimit int,
	log *zap.Logger,
) *Pipeline {
	return &Pipeline{
		store:         store,
		rest:          rest,
		ws:            ws,
		metrics:       metrics,
		symbols:       symbols,
		intervals:     intervals,
		backfillLimit: backfillLimit,
		log:           log,
	}
}

// Run starts the pipeline: backfills history, then subscribes to live streams.
// Blocks until ctx is cancelled.
func (p *Pipeline) Run(ctx context.Context) error {
	if err := p.backfill(ctx); err != nil {
		return fmt.Errorf("backfill failed: %w", err)
	}

	p.log.Info("starting real-time data ingestion",
		zap.Strings("symbols", p.symbols),
		zap.Strings("intervals", p.intervals))

	// Kline stream
	go p.ws.SubscribeKlines(ctx, p.symbols, p.intervals, p.handleKline)

	// Ticker stream
	go p.ws.SubscribeTickers(ctx, p.symbols, p.handleTicker)

	<-ctx.Done()
	p.log.Info("pipeline shutting down")
	return nil
}

// backfill fetches and stores recent historical klines for every symbol/interval.
func (p *Pipeline) backfill(ctx context.Context) error {
	p.log.Info("backfilling historical klines",
		zap.Int("limit", p.backfillLimit),
		zap.Strings("symbols", p.symbols),
		zap.Strings("intervals", p.intervals))

	for _, sym := range p.symbols {
		for _, itv := range p.intervals {
			klines, err := p.rest.GetKlines(ctx, sym, itv, p.backfillLimit)
			if err != nil {
				p.log.Error("backfill failed",
					zap.String("symbol", sym),
					zap.String("interval", itv),
					zap.Error(err))
				continue
			}

			start := time.Now()
			if err := p.store.BulkUpsertKlines(ctx, klines); err != nil {
				p.metrics.StoreErrors.WithLabelValues("bulk_upsert").Inc()
				p.log.Error("store backfill klines",
					zap.String("symbol", sym),
					zap.String("interval", itv),
					zap.Error(err))
				continue
			}
			p.metrics.DBWriteLatency.WithLabelValues("bulk_upsert").Observe(time.Since(start).Seconds())

			p.log.Info("backfilled",
				zap.String("symbol", sym),
				zap.String("interval", itv),
				zap.Int("count", len(klines)))
		}
	}
	return nil
}

// handleKline persists a kline event. Only closed candles are written to DB.
func (p *Pipeline) handleKline(k exchange.Kline) {
	p.metrics.KlinesReceived.WithLabelValues(k.Symbol, k.Interval).Inc()

	if !k.IsClosed {
		// In-progress candle – skip DB write, useful for real-time display later
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	if err := p.store.UpsertKline(ctx, k); err != nil {
		p.metrics.StoreErrors.WithLabelValues("upsert_kline").Inc()
		p.log.Error("store kline",
			zap.String("symbol", k.Symbol),
			zap.String("interval", k.Interval),
			zap.Error(err))
		return
	}
	p.metrics.DBWriteLatency.WithLabelValues("upsert_kline").Observe(time.Since(start).Seconds())
	p.metrics.KlinesStored.WithLabelValues(k.Symbol, k.Interval).Inc()

	// Notify downstream consumers (e.g. paper trading engine)
	if p.OnClosedKline != nil {
		p.OnClosedKline(k)
	}

	p.log.Debug("kline stored",
		zap.String("symbol", k.Symbol),
		zap.String("interval", k.Interval),
		zap.Float64("close", k.Close),
		zap.Time("open_time", k.OpenTime))
}

// handleTicker logs tickers and persists them at a reduced rate to avoid flooding the DB.
func (p *Pipeline) handleTicker(t exchange.Ticker) {
	p.metrics.TickersReceived.WithLabelValues(t.Symbol).Inc()

	// Persist every ticker; the hypertable + compression handles the volume.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := p.store.InsertTicker(ctx, t); err != nil {
		// Ticker inserts are best-effort – log at debug level to avoid noise
		p.log.Debug("store ticker failed",
			zap.String("symbol", t.Symbol),
			zap.Error(err))
	}
}
