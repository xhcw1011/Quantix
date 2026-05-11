package sentiment

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/Quantix/quantix/internal/alpha"
)

// BasisFetcher abstracts the source of perpetual-to-spot basis.
// basis = (perp_mark_price - spot_index) / spot_index
type BasisFetcher interface {
	LatestBasis(ctx context.Context, symbol string) (basis float64, err error)
}

// BasisDispersionConfig tunes the basis alpha.
type BasisDispersionConfig struct {
	Window     int           // rolling history length (default 100)
	ZThreshold float64       // |z| threshold for signal (default 2.0)
	CacheTTL   time.Duration // fetch cache (default 60s)
}

func (c BasisDispersionConfig) withDefaults() BasisDispersionConfig {
	if c.Window == 0 {
		c.Window = 100
	}
	if c.ZThreshold == 0 {
		c.ZThreshold = 2.0
	}
	if c.CacheTTL == 0 {
		c.CacheTTL = 60 * time.Second
	}
	return c
}

// BasisDispersion fades extreme perp-vs-spot basis. Maintains a rolling
// history of basis observations and emits a signal when current z-score
// exceeds threshold (perp expensive → SHORT perp; perp cheap → LONG perp).
type BasisDispersion struct {
	fetcher BasisFetcher
	cfg     BasisDispersionConfig

	mu        sync.Mutex
	history   []float64
	cached    float64
	cacheErr  error
	fetchedAt time.Time
}

func NewBasisDispersion(f BasisFetcher, cfg BasisDispersionConfig) *BasisDispersion {
	return &BasisDispersion{fetcher: f, cfg: cfg.withDefaults()}
}

func (a *BasisDispersion) Name() string { return "basis_dispersion" }

func (a *BasisDispersion) Predict(f alpha.Features) alpha.Signal {
	now := f.Now
	if now.IsZero() {
		now = time.Now()
	}
	basis, err := a.getCached(f, now)
	if err != nil {
		return alpha.Signal{Reason: fmt.Sprintf("fetch_error: %v", err)}
	}

	a.mu.Lock()
	a.history = append(a.history, basis)
	if len(a.history) > a.cfg.Window {
		a.history = a.history[len(a.history)-a.cfg.Window:]
	}
	hist := append([]float64(nil), a.history...)
	a.mu.Unlock()

	if len(hist) < a.cfg.Window {
		return alpha.Signal{Reason: fmt.Sprintf("warmup %d/%d", len(hist), a.cfg.Window)}
	}

	mean, std := meanStd(hist)
	if std == 0 {
		return alpha.Signal{Reason: "zero_std"}
	}
	z := (basis - mean) / std

	if math.Abs(z) < a.cfg.ZThreshold {
		return alpha.Signal{Reason: fmt.Sprintf("z=%.2f within band", z)}
	}

	strength := math.Min(math.Abs(z)/(a.cfg.ZThreshold*2), 1.0)
	if z > 0 {
		// Perp expensive — fade by going SHORT
		return alpha.Signal{
			Direction: alpha.DirShort,
			Strength:  strength,
			TargetR:   1.0,
			Reason:    fmt.Sprintf("basis_high z=%.2f basis=%.5f mean=%.5f", z, basis, mean),
		}
	}
	return alpha.Signal{
		Direction: alpha.DirLong,
		Strength:  strength,
		TargetR:   1.0,
		Reason:    fmt.Sprintf("basis_low z=%.2f basis=%.5f mean=%.5f", z, basis, mean),
	}
}

func (a *BasisDispersion) getCached(f alpha.Features, now time.Time) (float64, error) {
	a.mu.Lock()
	if !a.fetchedAt.IsZero() && now.Sub(a.fetchedAt) < a.cfg.CacheTTL {
		v, e := a.cached, a.cacheErr
		a.mu.Unlock()
		return v, e
	}
	a.mu.Unlock()

	basis, err := a.fetcher.LatestBasis(context.Background(), f.Symbol)

	a.mu.Lock()
	a.cached = basis
	a.cacheErr = err
	a.fetchedAt = now
	a.mu.Unlock()
	return basis, err
}

func meanStd(xs []float64) (mean, std float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	for _, x := range xs {
		d := x - mean
		std += d * d
	}
	std = math.Sqrt(std / float64(len(xs)))
	return mean, std
}
