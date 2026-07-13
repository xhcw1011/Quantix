package rebalancer

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// NextTick returns the next rebalance instant strictly after now: the soonest day
// whose day-number since the Unix epoch is a multiple of everyDays, at atHourUTC:00:00
// UTC. Epoch-anchored so the cadence is deterministic across process restarts.
func NextTick(now time.Time, everyDays, atHourUTC int) time.Time {
	if everyDays < 1 {
		everyDays = 1
	}
	now = now.UTC()
	c := time.Date(now.Year(), now.Month(), now.Day(), atHourUTC, 0, 0, 0, time.UTC)
	for !c.After(now) || int(c.Unix()/86400)%everyDays != 0 {
		c = c.AddDate(0, 0, 1)
		c = time.Date(c.Year(), c.Month(), c.Day(), atHourUTC, 0, 0, 0, time.UTC)
	}
	return c
}

// NextTickHours returns the next instant strictly after now that lands on an
// every-N-hours boundary (hour-of-day % everyHours == 0), at :00:00 UTC. For
// everyHours=8 that's 00:00 / 08:00 / 16:00 UTC — aligned to Binance funding
// settlements (8h and 4h coins both settle at these hours).
func NextTickHours(now time.Time, everyHours int) time.Time {
	if everyHours < 1 {
		everyHours = 1
	}
	now = now.UTC()
	h := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, time.UTC)
	for !h.After(now) || h.Hour()%everyHours != 0 {
		h = h.Add(time.Hour)
	}
	return h
}

// LoopHours runs tick at every N-hour boundary until ctx is cancelled (the 8h-cadence
// driver). Re-ranking is cheap; the tick only trades the delta, so unchanged rankings
// cost nothing.
func LoopHours(ctx context.Context, everyHours int, now func() time.Time, tick func(scheduled time.Time), log *zap.Logger) {
	for {
		next := NextTickHours(now(), everyHours)
		if log != nil {
			log.Info("rebalancer: next rotation scheduled", zap.Time("at_utc", next), zap.Duration("in", next.Sub(now())))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now())):
			tick(next)
		}
	}
}

// Loop runs tick at every scheduled rebalance until ctx is cancelled. It sleeps until
// the next tick (NextTick), logging when the next one is due; tick receives the
// scheduled instant. Blocking — run in its own goroutine.
func Loop(ctx context.Context, everyDays, atHourUTC int, now func() time.Time, tick func(scheduled time.Time), log *zap.Logger) {
	for {
		next := NextTick(now(), everyDays, atHourUTC)
		if log != nil {
			log.Info("rebalancer: next rotation scheduled", zap.Time("at_utc", next), zap.Duration("in", next.Sub(now())))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now())):
			tick(next)
		}
	}
}
