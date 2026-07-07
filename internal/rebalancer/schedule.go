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
