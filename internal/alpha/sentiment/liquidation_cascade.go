package sentiment

import (
	"fmt"
	"math"
	"time"

	"github.com/Quantix/quantix/internal/alpha"
)

// LiquidationBucket aggregates liquidation volume for a single 5-minute window.
type LiquidationBucket struct {
	StartTime   time.Time
	LongLiqUSD  float64 // forced SELLs (longs being liquidated)
	ShortLiqUSD float64 // forced BUYs (shorts being liquidated)
}

// LiquidationCollector provides bucketed historical liquidation volume.
// Production impl subscribes to a websocket; tests use a fake.
type LiquidationCollector interface {
	RecentBuckets(symbol string, since, until time.Time) []LiquidationBucket
}

// LiquidationCascadeConfig tunes the alpha thresholds.
type LiquidationCascadeConfig struct {
	LookbackWindow time.Duration // total lookback (default 15m = ~3 buckets)
	MinTotalUSD    float64       // total liq below this → HOLD (default $500K)
	DominanceRatio float64       // one side / other > this to trigger (default 2.0)
	SaturationUSD  float64       // dominant side at this → Strength=1.0 (default $5M)
}

func (c LiquidationCascadeConfig) withDefaults() LiquidationCascadeConfig {
	if c.LookbackWindow == 0 {
		c.LookbackWindow = 15 * time.Minute
	}
	if c.MinTotalUSD == 0 {
		c.MinTotalUSD = 500_000
	}
	if c.DominanceRatio == 0 {
		c.DominanceRatio = 2.0
	}
	if c.SaturationUSD == 0 {
		c.SaturationUSD = 5_000_000
	}
	return c
}

// LiquidationCascade fades large one-sided liquidation cascades. After a
// burst of long liquidations (forced SELLs pushing price down), expect a
// mean-reverting bounce → emit LONG. After short liquidations → SHORT.
type LiquidationCascade struct {
	collector LiquidationCollector
	cfg       LiquidationCascadeConfig
}

func NewLiquidationCascade(c LiquidationCollector, cfg LiquidationCascadeConfig) *LiquidationCascade {
	return &LiquidationCascade{collector: c, cfg: cfg.withDefaults()}
}

func (a *LiquidationCascade) Name() string { return "liquidation_cascade" }

func (a *LiquidationCascade) Predict(f alpha.Features) alpha.Signal {
	now := f.Now
	if now.IsZero() {
		now = time.Now()
	}
	buckets := a.collector.RecentBuckets(f.Symbol, now.Add(-a.cfg.LookbackWindow), now)
	if len(buckets) == 0 {
		return alpha.Signal{Reason: "no_buckets"}
	}

	var longLiq, shortLiq float64
	for _, b := range buckets {
		longLiq += b.LongLiqUSD
		shortLiq += b.ShortLiqUSD
	}
	total := longLiq + shortLiq
	if total < a.cfg.MinTotalUSD {
		return alpha.Signal{Reason: fmt.Sprintf("total_low %.0f", total)}
	}

	// Dominance check
	if longLiq > shortLiq*a.cfg.DominanceRatio {
		strength := math.Min(longLiq/a.cfg.SaturationUSD, 1.0)
		return alpha.Signal{
			Direction: alpha.DirLong,
			Strength:  strength,
			TargetR:   1.0,
			Reason:    fmt.Sprintf("long_cascade long=%.0fK short=%.0fK", longLiq/1000, shortLiq/1000),
		}
	}
	if shortLiq > longLiq*a.cfg.DominanceRatio {
		strength := math.Min(shortLiq/a.cfg.SaturationUSD, 1.0)
		return alpha.Signal{
			Direction: alpha.DirShort,
			Strength:  strength,
			TargetR:   1.0,
			Reason:    fmt.Sprintf("short_cascade long=%.0fK short=%.0fK", longLiq/1000, shortLiq/1000),
		}
	}
	return alpha.Signal{Reason: fmt.Sprintf("balanced long=%.0fK short=%.0fK", longLiq/1000, shortLiq/1000)}
}
