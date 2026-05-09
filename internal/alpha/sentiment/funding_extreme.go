// Package sentiment hosts non-TA alphas driven by funding, on-chain flow,
// news, and event-prediction signals. Each alpha takes a fetcher interface
// dependency so it can be unit-tested with mocks and run live with HTTP
// implementations.
package sentiment

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/Quantix/quantix/internal/alpha"
)

// FundingFetcher abstracts the source of perpetual-future funding rates so
// alphas can be tested with fakes and run live against any exchange API.
type FundingFetcher interface {
	// LatestFunding returns the most recent funding rate for the given
	// perpetual symbol (e.g. "ETHUSDT"). Rate is per-period (8h on Binance).
	// Positive = longs pay shorts.
	LatestFunding(ctx context.Context, symbol string) (rate float64, err error)
}

// FundingExtremeConfig tunes the alpha's thresholds and caching.
type FundingExtremeConfig struct {
	// Threshold below which |rate| is considered neutral (no signal).
	// Default 0.0001 (0.01% per 8h ≈ ~10% APR).
	NeutralBand float64
	// Rate at which Strength saturates to 1.0. Default 0.001 (0.1% per 8h).
	SaturationRate float64
	// How long to cache a fetched rate before refetching. Funding updates
	// every 8 hours on Binance so a 1-minute cache costs nothing in
	// freshness and saves API quota. Default: 60s.
	CacheTTL time.Duration
}

func (c FundingExtremeConfig) withDefaults() FundingExtremeConfig {
	if c.NeutralBand == 0 {
		c.NeutralBand = 0.0001
	}
	if c.SaturationRate == 0 {
		c.SaturationRate = 0.001
	}
	if c.CacheTTL == 0 {
		c.CacheTTL = 60 * time.Second
	}
	return c
}

// FundingExtreme emits a reversal signal when funding rate is at an extreme.
// Positive funding (longs crowded) → SHORT signal. Negative → LONG.
//
// This is a structurally-motivated, well-documented crypto signal. Not a
// guarantee of profit but a defensible edge candidate that doesn't depend
// on price-based technical patterns.
type FundingExtreme struct {
	fetcher FundingFetcher
	cfg     FundingExtremeConfig

	mu        sync.Mutex
	cached    float64
	fetchedAt time.Time
	cacheErr  error
}

// NewFundingExtreme returns an alpha wired to the given fetcher.
func NewFundingExtreme(f FundingFetcher, cfg FundingExtremeConfig) *FundingExtreme {
	return &FundingExtreme{fetcher: f, cfg: cfg.withDefaults()}
}

func (a *FundingExtreme) Name() string { return "funding_extreme" }

func (a *FundingExtreme) Predict(f alpha.Features) alpha.Signal {
	rate, err := a.getCached(f)
	if err != nil {
		return alpha.Signal{Reason: fmt.Sprintf("fetch_error: %v", err)}
	}
	if math.Abs(rate) < a.cfg.NeutralBand {
		return alpha.Signal{Reason: fmt.Sprintf("neutral_band rate=%.4f%%", rate*100)}
	}

	strength := math.Abs(rate) / a.cfg.SaturationRate
	if strength > 1.0 {
		strength = 1.0
	}

	if rate > 0 {
		// Longs are crowded — fade them, go short.
		return alpha.Signal{
			Direction: alpha.DirShort,
			Strength:  strength,
			TargetR:   1.0,
			Reason:    fmt.Sprintf("longs_crowded rate=%.4f%%", rate*100),
		}
	}
	// Shorts are crowded — fade them, go long.
	return alpha.Signal{
		Direction: alpha.DirLong,
		Strength:  strength,
		TargetR:   1.0,
		Reason:    fmt.Sprintf("shorts_crowded rate=%.4f%%", rate*100),
	}
}

// getCached returns the funding rate, using a TTL'd cache to avoid hammering
// the API. The cache is per-alpha instance (not shared across symbols), but
// in Phase 3 each engine runs one symbol so this is fine.
func (a *FundingExtreme) getCached(f alpha.Features) (float64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := f.Now
	if now.IsZero() {
		now = time.Now()
	}
	if !a.fetchedAt.IsZero() && now.Sub(a.fetchedAt) < a.cfg.CacheTTL {
		return a.cached, a.cacheErr
	}

	rate, err := a.fetcher.LatestFunding(context.Background(), f.Symbol)
	a.cached = rate
	a.cacheErr = err
	a.fetchedAt = now
	return rate, err
}
