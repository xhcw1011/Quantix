package sentiment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Quantix/quantix/internal/alpha"
)

type fakeNewsFetcher struct {
	items []NewsItem
	err   error
}

func (f *fakeNewsFetcher) RecentNews(_ context.Context, _ string, _ time.Time) ([]NewsItem, error) {
	return f.items, f.err
}

func now() time.Time { return time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC) }

func TestCryptopanic_StrongBullish(t *testing.T) {
	items := []NewsItem{
		{PublishedAt: now().Add(-30 * time.Minute), Bullish: 50, Bearish: 5},
		{PublishedAt: now().Add(-2 * time.Hour), Bullish: 30, Bearish: 10},
	}
	a := NewCryptopanicNews(&fakeNewsFetcher{items: items}, CryptopanicConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if s.Direction != alpha.DirLong {
		t.Fatalf("expected LONG on strong-bullish news, got %d (reason=%s)", s.Direction, s.Reason)
	}
	if s.Strength <= 0 {
		t.Fatalf("expected Strength>0, got %f", s.Strength)
	}
}

func TestCryptopanic_StrongBearish(t *testing.T) {
	items := []NewsItem{
		{PublishedAt: now().Add(-30 * time.Minute), Bullish: 5, Bearish: 60},
		{PublishedAt: now().Add(-3 * time.Hour), Bullish: 8, Bearish: 25},
	}
	a := NewCryptopanicNews(&fakeNewsFetcher{items: items}, CryptopanicConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if s.Direction != alpha.DirShort {
		t.Fatalf("expected SHORT on bearish news, got %d", s.Direction)
	}
}

func TestCryptopanic_NeutralBalance(t *testing.T) {
	items := []NewsItem{
		{PublishedAt: now().Add(-1 * time.Hour), Bullish: 20, Bearish: 22},
	}
	a := NewCryptopanicNews(&fakeNewsFetcher{items: items}, CryptopanicConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("expected HOLD on balanced votes, got %d", s.Direction)
	}
}

func TestCryptopanic_LowVoteCountIsHold(t *testing.T) {
	items := []NewsItem{
		{PublishedAt: now().Add(-1 * time.Hour), Bullish: 1, Bearish: 0},
	}
	a := NewCryptopanicNews(&fakeNewsFetcher{items: items}, CryptopanicConfig{MinVotes: 5})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("low-vote count should yield HOLD, got %d", s.Direction)
	}
}

func TestCryptopanic_NoItemsIsHold(t *testing.T) {
	a := NewCryptopanicNews(&fakeNewsFetcher{items: nil}, CryptopanicConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("expected HOLD with no news, got %d", s.Direction)
	}
}

func TestCryptopanic_FetchErrorIsHold(t *testing.T) {
	a := NewCryptopanicNews(&fakeNewsFetcher{err: errors.New("rate limited")}, CryptopanicConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("fetch error should yield HOLD, got %d", s.Direction)
	}
	if s.Reason == "" {
		t.Fatalf("error path should populate Reason")
	}
}

func TestCryptopanic_RecencyWeighting(t *testing.T) {
	// Recent BEARISH should outweigh older BULLISH despite equal vote counts.
	items := []NewsItem{
		{PublishedAt: now().Add(-30 * time.Minute), Bullish: 5, Bearish: 30},                  // recent bearish
		{PublishedAt: now().Add(-5*time.Hour - 30*time.Minute), Bullish: 30, Bearish: 5}, // old bullish, near lookback edge
	}
	a := NewCryptopanicNews(&fakeNewsFetcher{items: items}, CryptopanicConfig{
		LookbackWindow: 6 * time.Hour,
	})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if s.Direction != alpha.DirShort {
		t.Fatalf("recent bearish should dominate older bullish, got %d (reason=%s)", s.Direction, s.Reason)
	}
}

func TestCryptopanic_OutsideWindowIgnored(t *testing.T) {
	// Item is 10 hours old — outside default 6h lookback. Should be filtered out → HOLD (no items).
	items := []NewsItem{
		{PublishedAt: now().Add(-10 * time.Hour), Bullish: 100, Bearish: 5},
	}
	a := NewCryptopanicNews(&fakeNewsFetcher{items: items}, CryptopanicConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("expected HOLD (item outside window), got %d", s.Direction)
	}
}

func TestCryptopanic_CacheTTL(t *testing.T) {
	calls := 0
	fetcher := &countingNewsFetcher{calls: &calls, items: []NewsItem{
		{PublishedAt: now().Add(-1 * time.Hour), Bullish: 50, Bearish: 5},
	}}
	a := NewCryptopanicNews(fetcher, CryptopanicConfig{CacheTTL: 10 * time.Minute})
	a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now()})
	a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: now().Add(5 * time.Minute)})
	if calls != 1 {
		t.Fatalf("expected 1 fetch within TTL, got %d", calls)
	}
}

type countingNewsFetcher struct {
	calls *int
	items []NewsItem
}

func (c *countingNewsFetcher) RecentNews(_ context.Context, _ string, _ time.Time) ([]NewsItem, error) {
	*c.calls++
	return c.items, nil
}

func TestCryptopanic_Name(t *testing.T) {
	a := NewCryptopanicNews(&fakeNewsFetcher{}, CryptopanicConfig{})
	if a.Name() != "cryptopanic_news" {
		t.Fatalf("Name=%q", a.Name())
	}
}
