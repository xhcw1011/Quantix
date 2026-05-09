package sentiment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Quantix/quantix/internal/alpha"
)

// fakeFundingFetcher returns canned funding rates for tests.
type fakeFundingFetcher struct {
	rate float64
	err  error
}

func (f *fakeFundingFetcher) LatestFunding(_ context.Context, _ string) (float64, error) {
	return f.rate, f.err
}

func TestFundingExtreme_LongCrowdedShorts(t *testing.T) {
	a := NewFundingExtreme(&fakeFundingFetcher{rate: 0.00015}, FundingExtremeConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now()})
	if s.Direction != alpha.DirShort {
		t.Fatalf("expected SHORT (longs crowded at +0.015%%), got %d", s.Direction)
	}
	if s.Strength <= 0 {
		t.Fatalf("expected strength>0, got %f", s.Strength)
	}
}

func TestFundingExtreme_ShortCrowdedLongs(t *testing.T) {
	a := NewFundingExtreme(&fakeFundingFetcher{rate: -0.00015}, FundingExtremeConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now()})
	if s.Direction != alpha.DirLong {
		t.Fatalf("expected LONG (shorts crowded), got %d", s.Direction)
	}
}

func TestFundingExtreme_NeutralHold(t *testing.T) {
	// 0.005% — below threshold, no signal
	a := NewFundingExtreme(&fakeFundingFetcher{rate: 0.00005}, FundingExtremeConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("expected HOLD on neutral funding, got %d", s.Direction)
	}
}

func TestFundingExtreme_StrengthScalesWithMagnitude(t *testing.T) {
	near := NewFundingExtreme(&fakeFundingFetcher{rate: 0.00012}, FundingExtremeConfig{})
	extreme := NewFundingExtreme(&fakeFundingFetcher{rate: 0.0005}, FundingExtremeConfig{})
	sNear := near.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now()})
	sExtreme := extreme.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now()})
	if sExtreme.Strength <= sNear.Strength {
		t.Fatalf("extreme (%f) should exceed near-threshold (%f)", sExtreme.Strength, sNear.Strength)
	}
}

func TestFundingExtreme_StrengthCappedAtOne(t *testing.T) {
	// Truly absurd funding (5% per 8h) — must cap at 1.0
	a := NewFundingExtreme(&fakeFundingFetcher{rate: 0.05}, FundingExtremeConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now()})
	if s.Strength != 1.0 {
		t.Fatalf("expected cap at 1.0, got %f", s.Strength)
	}
}

func TestFundingExtreme_FetchErrorReturnsHold(t *testing.T) {
	a := NewFundingExtreme(&fakeFundingFetcher{err: errors.New("network down")}, FundingExtremeConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("fetch error should yield HOLD, got %d", s.Direction)
	}
	if s.Reason == "" {
		t.Fatalf("error path should populate Reason for diagnostics")
	}
}

func TestFundingExtreme_CacheTTLAvoidsRefetch(t *testing.T) {
	// Fetcher counts calls; alpha should fetch only once within TTL
	calls := 0
	counting := &countingFundingFetcher{calls: &calls, rate: 0.00015}
	a := NewFundingExtreme(counting, FundingExtremeConfig{CacheTTL: 100 * time.Millisecond})

	a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now()})
	a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: time.Now().Add(50 * time.Millisecond)})
	if calls != 1 {
		t.Fatalf("cache should suppress 2nd fetch, got %d calls", calls)
	}
}

type countingFundingFetcher struct {
	calls *int
	rate  float64
}

func (c *countingFundingFetcher) LatestFunding(_ context.Context, _ string) (float64, error) {
	*c.calls++
	return c.rate, nil
}

func TestFundingExtreme_Name(t *testing.T) {
	a := NewFundingExtreme(&fakeFundingFetcher{}, FundingExtremeConfig{})
	if a.Name() != "funding_extreme" {
		t.Fatalf("Name=%q", a.Name())
	}
}
