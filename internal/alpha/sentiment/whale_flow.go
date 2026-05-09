package sentiment

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/Quantix/quantix/internal/alpha"
)

// WhaleFlow is a single large on-chain transfer with exchange-side classification.
type WhaleFlow struct {
	Timestamp    time.Time
	AmountUSD    float64
	ToExchange   bool
	FromExchange bool
}

// FlowFetcher abstracts the source of whale-flow data.
type FlowFetcher interface {
	RecentFlows(ctx context.Context, symbol string, since time.Time) ([]WhaleFlow, error)
}

// WhaleFlowConfig tunes thresholds for the whale-flow alpha.
type WhaleFlowConfig struct {
	LookbackWindow time.Duration // default 4h
	MinNetUSD      float64       // default $5M; below → HOLD
	SaturationUSD  float64       // default $50M; |net| at which Strength=1.0
	CacheTTL       time.Duration // default 5 minutes
}

func (c WhaleFlowConfig) withDefaults() WhaleFlowConfig {
	if c.LookbackWindow == 0 {
		c.LookbackWindow = 4 * time.Hour
	}
	if c.MinNetUSD == 0 {
		c.MinNetUSD = 5_000_000
	}
	if c.SaturationUSD == 0 {
		c.SaturationUSD = 50_000_000
	}
	if c.CacheTTL == 0 {
		c.CacheTTL = 5 * time.Minute
	}
	return c
}

// WhaleFlowAlpha: net exchange inflow over lookback window predicts short-term direction.
// Inflow → near-term sell pressure → SHORT. Outflow → accumulation → LONG.
type WhaleFlowAlpha struct {
	fetcher FlowFetcher
	cfg     WhaleFlowConfig

	mu        sync.Mutex
	cached    []WhaleFlow
	cacheErr  error
	fetchedAt time.Time
}

// NewWhaleFlow returns a WhaleFlowAlpha wired to the fetcher.
func NewWhaleFlow(f FlowFetcher, cfg WhaleFlowConfig) *WhaleFlowAlpha {
	return &WhaleFlowAlpha{fetcher: f, cfg: cfg.withDefaults()}
}

// Name returns the alpha identifier.
func (a *WhaleFlowAlpha) Name() string { return "whale_flow" }

// Predict aggregates exchange inflow vs outflow over the lookback window.
func (a *WhaleFlowAlpha) Predict(f alpha.Features) alpha.Signal {
	now := f.Now
	if now.IsZero() {
		now = time.Now()
	}
	flows, err := a.getCached(f, now)
	if err != nil {
		return alpha.Signal{Reason: fmt.Sprintf("fetch_error: %v", err)}
	}

	cutoff := now.Add(-a.cfg.LookbackWindow)
	var inflow, outflow float64
	for _, fl := range flows {
		if fl.Timestamp.Before(cutoff) {
			continue
		}
		// Only exchange-relevant flows count; pure wallet-to-wallet ignored.
		if fl.ToExchange && !fl.FromExchange {
			inflow += fl.AmountUSD
		} else if fl.FromExchange && !fl.ToExchange {
			outflow += fl.AmountUSD
		}
	}

	net := inflow - outflow // positive = net selling pressure inbound
	if math.Abs(net) < a.cfg.MinNetUSD {
		return alpha.Signal{Reason: fmt.Sprintf("below_min net_usd=%.0f", net)}
	}

	strength := math.Abs(net) / a.cfg.SaturationUSD
	if strength > 1.0 {
		strength = 1.0
	}

	if net > 0 {
		return alpha.Signal{
			Direction: alpha.DirShort,
			Strength:  strength,
			TargetR:   1.0,
			Reason:    fmt.Sprintf("net_inflow=%.0fM in=%.0fM out=%.0fM", net/1e6, inflow/1e6, outflow/1e6),
		}
	}
	return alpha.Signal{
		Direction: alpha.DirLong,
		Strength:  strength,
		TargetR:   1.0,
		Reason:    fmt.Sprintf("net_outflow=%.0fM in=%.0fM out=%.0fM", -net/1e6, inflow/1e6, outflow/1e6),
	}
}

func (a *WhaleFlowAlpha) getCached(f alpha.Features, now time.Time) ([]WhaleFlow, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.fetchedAt.IsZero() && now.Sub(a.fetchedAt) < a.cfg.CacheTTL {
		return a.cached, a.cacheErr
	}
	flows, err := a.fetcher.RecentFlows(context.Background(), f.Symbol, now.Add(-a.cfg.LookbackWindow))
	a.cached = flows
	a.cacheErr = err
	a.fetchedAt = now
	return flows, err
}
