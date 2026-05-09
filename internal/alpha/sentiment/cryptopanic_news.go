package sentiment

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/Quantix/quantix/internal/alpha"
)

// NewsItem is the normalized shape of a news post used by sentiment alphas.
// Different upstream APIs (CryptoPanic, LunarCrush, etc.) map their fields
// onto this shape via their respective fetcher implementations.
type NewsItem struct {
	Title       string
	PublishedAt time.Time
	Bullish     int // votes.positive
	Bearish     int // votes.negative
}

// NewsFetcher abstracts the source of recent news posts.
// `since` is a soft cutoff — fetchers may return slightly older items;
// the alpha re-filters by its own LookbackWindow.
type NewsFetcher interface {
	RecentNews(ctx context.Context, symbol string, since time.Time) ([]NewsItem, error)
}

// CryptopanicConfig tunes the news alpha thresholds.
type CryptopanicConfig struct {
	// LookbackWindow is the time horizon for aggregating news. Default 6h.
	LookbackWindow time.Duration
	// MinVotes is the minimum total weighted vote count required to emit a
	// directional signal. Below this the alpha returns HOLD. Default 5.
	MinVotes int
	// NetThreshold is the minimum |net| for a directional signal in [-1,+1].
	// |net| below this → HOLD. Default 0.2.
	NetThreshold float64
	// CacheTTL bounds how often the fetcher is invoked. CryptoPanic free
	// tier allows ~200 req/day; default 10m keeps us comfortably under.
	CacheTTL time.Duration
}

func (c CryptopanicConfig) withDefaults() CryptopanicConfig {
	if c.LookbackWindow == 0 {
		c.LookbackWindow = 6 * time.Hour
	}
	if c.MinVotes == 0 {
		c.MinVotes = 5
	}
	if c.NetThreshold == 0 {
		c.NetThreshold = 0.2
	}
	if c.CacheTTL == 0 {
		c.CacheTTL = 10 * time.Minute
	}
	return c
}

// CryptopanicNews aggregates recent news bullish/bearish votes into a
// momentum-continuation signal. Trading thesis: news with strong directional
// sentiment in the last N hours predicts continuation, not reversion. Strong
// bullish votes → LONG. Strong bearish votes → SHORT. Posts are recency-
// weighted (linear decay) so fresh news dominates older noise.
type CryptopanicNews struct {
	fetcher NewsFetcher
	cfg     CryptopanicConfig

	mu        sync.Mutex
	cached    []NewsItem
	cacheErr  error
	fetchedAt time.Time
}

// NewCryptopanicNews returns an alpha wired to the given fetcher.
func NewCryptopanicNews(f NewsFetcher, cfg CryptopanicConfig) *CryptopanicNews {
	return &CryptopanicNews{fetcher: f, cfg: cfg.withDefaults()}
}

func (a *CryptopanicNews) Name() string { return "cryptopanic_news" }

func (a *CryptopanicNews) Predict(f alpha.Features) alpha.Signal {
	now := f.Now
	if now.IsZero() {
		now = time.Now()
	}
	items, err := a.getCached(f, now)
	if err != nil {
		return alpha.Signal{Reason: fmt.Sprintf("fetch_error: %v", err)}
	}

	cutoff := now.Add(-a.cfg.LookbackWindow)
	var bullW, bearW float64
	for _, item := range items {
		if item.PublishedAt.Before(cutoff) {
			continue
		}
		age := now.Sub(item.PublishedAt)
		if age < 0 {
			age = 0
		}
		// Linear recency decay: weight in [0, 1], 1 at now, ~0 at lookback edge.
		w := 1 - float64(age)/float64(a.cfg.LookbackWindow)
		if w < 0 {
			w = 0
		}
		bullW += float64(item.Bullish) * w
		bearW += float64(item.Bearish) * w
	}

	totalW := bullW + bearW
	if totalW < float64(a.cfg.MinVotes) {
		return alpha.Signal{Reason: fmt.Sprintf("low_volume bull=%.1f bear=%.1f", bullW, bearW)}
	}

	net := (bullW - bearW) / totalW // in [-1, +1]
	if math.Abs(net) < a.cfg.NetThreshold {
		return alpha.Signal{Reason: fmt.Sprintf("balanced net=%.2f", net)}
	}

	strength := math.Abs(net)
	if strength > 1 {
		strength = 1
	}
	if net > 0 {
		return alpha.Signal{
			Direction: alpha.DirLong,
			Strength:  strength,
			TargetR:   1.0,
			Reason:    fmt.Sprintf("bullish_news net=%.2f bull=%.1f bear=%.1f", net, bullW, bearW),
		}
	}
	return alpha.Signal{
		Direction: alpha.DirShort,
		Strength:  strength,
		TargetR:   1.0,
		Reason:    fmt.Sprintf("bearish_news net=%.2f bull=%.1f bear=%.1f", net, bullW, bearW),
	}
}

// getCached returns the cached news list, refetching when TTL has elapsed.
// Cache is keyed implicitly by the alpha instance — Phase 3 runs one symbol
// per engine so a per-instance cache is sufficient.
func (a *CryptopanicNews) getCached(f alpha.Features, now time.Time) ([]NewsItem, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.fetchedAt.IsZero() && now.Sub(a.fetchedAt) < a.cfg.CacheTTL {
		return a.cached, a.cacheErr
	}
	items, err := a.fetcher.RecentNews(context.Background(), f.Symbol, now.Add(-a.cfg.LookbackWindow))
	a.cached = items
	a.cacheErr = err
	a.fetchedAt = now
	return items, err
}
