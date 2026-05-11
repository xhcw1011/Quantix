package sentiment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Quantix/quantix/internal/alpha"
)

type fakeBasisFetcher struct {
	basis float64
	err   error
}

func (f *fakeBasisFetcher) LatestBasis(_ context.Context, _ string) (float64, error) {
	return f.basis, f.err
}

func TestBasisDispersion_NoSignalDuringWarmup(t *testing.T) {
	a := NewBasisDispersion(&fakeBasisFetcher{basis: 0.001}, BasisDispersionConfig{Window: 20})
	// Feed 19 bars (one short of window)
	for i := 0; i < 19; i++ {
		s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now().Add(time.Duration(i) * time.Minute)})
		if s.Direction != alpha.DirHold {
			t.Fatalf("warmup bar %d: expected HOLD, got %d", i, s.Direction)
		}
	}
}

func TestBasisDispersion_ExtremePositiveBasisShort(t *testing.T) {
	// Build history with mean ~0.0001 std ~0.00005, then huge spike to 0.005 (z~98 sigma)
	fetcher := &sequentialBasisFetcher{values: []float64{}}
	for i := 0; i < 50; i++ {
		// alternate 0.00005 and 0.00015 to give mean ~0.0001, std ~0.00005
		v := 0.00005
		if i%2 == 0 {
			v = 0.00015
		}
		fetcher.values = append(fetcher.values, v)
	}
	fetcher.values = append(fetcher.values, 0.005) // extreme spike

	a := NewBasisDispersion(fetcher, BasisDispersionConfig{Window: 50, ZThreshold: 2.0})
	var last alpha.Signal
	for i := 0; i < 51; i++ {
		last = a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now().Add(time.Duration(i) * time.Minute)})
	}
	if last.Direction != alpha.DirShort {
		t.Fatalf("expected SHORT on positive z-spike, got %d (reason=%s)", last.Direction, last.Reason)
	}
	if last.Strength <= 0 {
		t.Fatalf("expected Strength>0, got %f", last.Strength)
	}
}

func TestBasisDispersion_ExtremeNegativeBasisLong(t *testing.T) {
	fetcher := &sequentialBasisFetcher{values: []float64{}}
	for i := 0; i < 50; i++ {
		v := 0.00005
		if i%2 == 0 {
			v = 0.00015
		}
		fetcher.values = append(fetcher.values, v)
	}
	fetcher.values = append(fetcher.values, -0.005) // extreme negative

	a := NewBasisDispersion(fetcher, BasisDispersionConfig{Window: 50, ZThreshold: 2.0})
	var last alpha.Signal
	for i := 0; i < 51; i++ {
		last = a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now().Add(time.Duration(i) * time.Minute)})
	}
	if last.Direction != alpha.DirLong {
		t.Fatalf("expected LONG on negative z-spike, got %d", last.Direction)
	}
}

func TestBasisDispersion_NeutralBasisHold(t *testing.T) {
	fetcher := &sequentialBasisFetcher{values: []float64{}}
	for i := 0; i < 51; i++ {
		v := 0.0001
		if i%2 == 0 {
			v = 0.00012
		}
		fetcher.values = append(fetcher.values, v) // tight cluster, no extreme
	}
	a := NewBasisDispersion(fetcher, BasisDispersionConfig{Window: 50, ZThreshold: 2.0})
	var last alpha.Signal
	for i := 0; i < 51; i++ {
		last = a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now().Add(time.Duration(i) * time.Minute)})
	}
	if last.Direction != alpha.DirHold {
		t.Fatalf("expected HOLD on tight-cluster basis, got %d", last.Direction)
	}
}

func TestBasisDispersion_FetchErrorIsHold(t *testing.T) {
	a := NewBasisDispersion(&fakeBasisFetcher{err: errors.New("net down")}, BasisDispersionConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("fetch error should yield HOLD")
	}
	if s.Reason == "" {
		t.Fatalf("error path must populate Reason")
	}
}

func TestBasisDispersion_ZeroStdHold(t *testing.T) {
	// All-same history → std=0 → division by zero → must HOLD safely
	fetcher := &sequentialBasisFetcher{values: []float64{}}
	for i := 0; i < 51; i++ {
		fetcher.values = append(fetcher.values, 0.0001)
	}
	a := NewBasisDispersion(fetcher, BasisDispersionConfig{Window: 50, ZThreshold: 2.0})
	var last alpha.Signal
	for i := 0; i < 51; i++ {
		last = a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now().Add(time.Duration(i) * time.Minute)})
	}
	if last.Direction != alpha.DirHold {
		t.Fatalf("zero-std must produce HOLD, got %d (reason=%s)", last.Direction, last.Reason)
	}
}

func TestBasisDispersion_CacheTTL(t *testing.T) {
	calls := 0
	fetcher := &countingBasisFetcher{calls: &calls, basis: 0.0001}
	a := NewBasisDispersion(fetcher, BasisDispersionConfig{CacheTTL: 60 * time.Second})
	a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now()})
	a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now().Add(30 * time.Second)})
	if calls != 1 {
		t.Fatalf("expected 1 fetch within TTL, got %d", calls)
	}
}

func TestBasisDispersion_Name(t *testing.T) {
	a := NewBasisDispersion(&fakeBasisFetcher{}, BasisDispersionConfig{})
	if a.Name() != "basis_dispersion" {
		t.Fatalf("Name=%q", a.Name())
	}
}

// Helpers
type sequentialBasisFetcher struct {
	values []float64
	idx    int
}

func (s *sequentialBasisFetcher) LatestBasis(_ context.Context, _ string) (float64, error) {
	if s.idx >= len(s.values) {
		return 0, errors.New("exhausted")
	}
	v := s.values[s.idx]
	s.idx++
	return v, nil
}

type countingBasisFetcher struct {
	calls *int
	basis float64
}

func (c *countingBasisFetcher) LatestBasis(_ context.Context, _ string) (float64, error) {
	*c.calls++
	return c.basis, nil
}
