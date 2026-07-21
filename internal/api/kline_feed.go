package api

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
)

// klinePollInterval is how often the REST fallback checks for a newly-closed bar.
// 20s is fine for any interval ≥ 1m: a closed bar is picked up within 20s of its
// boundary, negligible for a 5m+ strategy, and only ~3 light REST calls/min.
const klinePollInterval = 20 * time.Second

// klineFetcher is the subset of the REST client the poller needs.
type klineFetcher interface {
	GetKlines(ctx context.Context, symbol, interval string, limit int) ([]exchange.Kline, error)
}

// klineDeduper forwards klines to out, dropping any bar whose open time is not
// strictly newer than the last one already forwarded for that interval. This lets
// several sources — the kline WS, the REST poll fallback, and the warmup backfill
// — feed the same channel without the engine ever double-processing a bar (which
// would corrupt indicators and re-fire signals).
type klineDeduper struct {
	mu   sync.Mutex
	last map[string]int64 // interval → last forwarded open time (unix nano)
	out  chan<- exchange.Kline
	log  *zap.Logger
}

func newKlineDeduper(out chan<- exchange.Kline, log *zap.Logger) *klineDeduper {
	if log == nil {
		log = zap.NewNop()
	}
	return &klineDeduper{last: map[string]int64{}, out: out, log: log}
}

// emit forwards k unless a bar with an equal-or-newer open time was already
// forwarded for its interval. Non-blocking; drops (with a warning) if out is full.
func (d *klineDeduper) emit(k exchange.Kline) bool {
	ot := k.OpenTime.UnixNano()
	d.mu.Lock()
	if prev, ok := d.last[k.Interval]; ok && ot <= prev {
		d.mu.Unlock()
		return false
	}
	d.last[k.Interval] = ot
	d.mu.Unlock()

	select {
	case d.out <- k:
		return true
	default:
		d.log.Warn("kline channel full, dropping bar", zap.String("interval", k.Interval))
		return false
	}
}

// pollOnce fetches the latest few klines and emits every CLOSED bar (close time
// already passed) via emit; the still-forming current bar is skipped. Emitted
// bars are marked IsClosed so the engine treats them like WS-delivered closes.
func pollOnce(ctx context.Context, src klineFetcher, symbol, interval string, now func() time.Time, emit func(exchange.Kline), log *zap.Logger) {
	bars, err := src.GetKlines(ctx, symbol, interval, 3)
	if err != nil {
		log.Warn("kline poll failed", zap.String("symbol", symbol), zap.String("interval", interval), zap.Error(err))
		return
	}
	for _, b := range bars {
		if b.CloseTime.Before(now()) {
			b.IsClosed = true
			emit(b)
		}
	}
}

// pollClosedKlines runs pollOnce every klinePollInterval until ctx is cancelled.
// It is the fallback kline source for engines whose kline WS delivers nothing
// (e.g. Binance's live market-data WS is blocked on this host while the tick
// stream works). Paired with a klineDeduper, it is a harmless no-op whenever the
// WS already delivered the bar, and the sole source when the WS is silent.
func pollClosedKlines(ctx context.Context, src klineFetcher, symbol, interval string, emit func(exchange.Kline), log *zap.Logger) {
	if log == nil {
		log = zap.NewNop()
	}
	t := time.NewTicker(klinePollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pollOnce(ctx, src, symbol, interval, time.Now, emit, log)
		}
	}
}
