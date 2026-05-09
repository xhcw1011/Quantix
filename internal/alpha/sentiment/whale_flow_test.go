package sentiment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Quantix/quantix/internal/alpha"
)

type fakeFlowFetcher struct {
	flows []WhaleFlow
	err   error
}

func (f *fakeFlowFetcher) RecentFlows(_ context.Context, _ string, _ time.Time) ([]WhaleFlow, error) {
	return f.flows, f.err
}

func TestWhaleFlow_NetInflowShort(t *testing.T) {
	flows := []WhaleFlow{
		{Timestamp: now().Add(-30 * time.Minute), AmountUSD: 8_000_000, ToExchange: true},
		{Timestamp: now().Add(-1 * time.Hour), AmountUSD: 2_000_000, FromExchange: true},
	}
	a := NewWhaleFlow(&fakeFlowFetcher{flows: flows}, WhaleFlowConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if s.Direction != alpha.DirShort {
		t.Fatalf("expected SHORT (net inflow), got %d (reason=%s)", s.Direction, s.Reason)
	}
}

func TestWhaleFlow_NetOutflowLong(t *testing.T) {
	flows := []WhaleFlow{
		{Timestamp: now().Add(-30 * time.Minute), AmountUSD: 10_000_000, FromExchange: true},
		{Timestamp: now().Add(-1 * time.Hour), AmountUSD: 2_000_000, ToExchange: true},
	}
	a := NewWhaleFlow(&fakeFlowFetcher{flows: flows}, WhaleFlowConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if s.Direction != alpha.DirLong {
		t.Fatalf("expected LONG (net outflow), got %d", s.Direction)
	}
}

func TestWhaleFlow_BelowMinIsHold(t *testing.T) {
	flows := []WhaleFlow{
		{Timestamp: now().Add(-30 * time.Minute), AmountUSD: 1_000_000, ToExchange: true},
	}
	a := NewWhaleFlow(&fakeFlowFetcher{flows: flows}, WhaleFlowConfig{MinNetUSD: 5_000_000})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("expected HOLD below MinNetUSD, got %d", s.Direction)
	}
}

func TestWhaleFlow_OutsideWindowIgnored(t *testing.T) {
	flows := []WhaleFlow{
		{Timestamp: now().Add(-5 * time.Hour), AmountUSD: 100_000_000, ToExchange: true},
	}
	a := NewWhaleFlow(&fakeFlowFetcher{flows: flows}, WhaleFlowConfig{LookbackWindow: 4 * time.Hour})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("expected HOLD (flow outside window), got %d", s.Direction)
	}
}

func TestWhaleFlow_NeutralFlowsIgnored(t *testing.T) {
	// Wallet-to-wallet transfer (neither side is exchange) — should be ignored.
	flows := []WhaleFlow{
		{Timestamp: now().Add(-30 * time.Minute), AmountUSD: 50_000_000, ToExchange: false, FromExchange: false},
	}
	a := NewWhaleFlow(&fakeFlowFetcher{flows: flows}, WhaleFlowConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("wallet-to-wallet flow should be HOLD, got %d", s.Direction)
	}
}

func TestWhaleFlow_StrengthScales(t *testing.T) {
	small := []WhaleFlow{{Timestamp: now().Add(-30 * time.Minute), AmountUSD: 8_000_000, ToExchange: true}}
	huge := []WhaleFlow{{Timestamp: now().Add(-30 * time.Minute), AmountUSD: 80_000_000, ToExchange: true}}
	cfg := WhaleFlowConfig{SaturationUSD: 50_000_000}
	sSmall := NewWhaleFlow(&fakeFlowFetcher{flows: small}, cfg).Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	sHuge := NewWhaleFlow(&fakeFlowFetcher{flows: huge}, cfg).Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if sHuge.Strength <= sSmall.Strength {
		t.Fatalf("huge inflow strength %f should exceed small %f", sHuge.Strength, sSmall.Strength)
	}
	if sHuge.Strength != 1.0 {
		t.Fatalf("80M inflow with 50M saturation should cap at 1.0, got %f", sHuge.Strength)
	}
}

func TestWhaleFlow_FetchErrorIsHold(t *testing.T) {
	a := NewWhaleFlow(&fakeFlowFetcher{err: errors.New("rate limit")}, WhaleFlowConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("expected HOLD on fetch error")
	}
}

func TestWhaleFlow_CacheTTL(t *testing.T) {
	calls := 0
	fetcher := &countingFlowFetcher{calls: &calls, flows: []WhaleFlow{
		{Timestamp: now().Add(-30 * time.Minute), AmountUSD: 8_000_000, ToExchange: true},
	}}
	a := NewWhaleFlow(fetcher, WhaleFlowConfig{CacheTTL: 5 * time.Minute})
	a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now().Add(2 * time.Minute)})
	if calls != 1 {
		t.Fatalf("expected 1 fetch within TTL, got %d", calls)
	}
}

type countingFlowFetcher struct {
	calls *int
	flows []WhaleFlow
}

func (c *countingFlowFetcher) RecentFlows(_ context.Context, _ string, _ time.Time) ([]WhaleFlow, error) {
	*c.calls++
	return c.flows, nil
}

func TestWhaleFlow_Name(t *testing.T) {
	a := NewWhaleFlow(&fakeFlowFetcher{}, WhaleFlowConfig{})
	if a.Name() != "whale_flow" {
		t.Fatalf("Name=%q", a.Name())
	}
}
