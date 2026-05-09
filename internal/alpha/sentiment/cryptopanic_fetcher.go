package sentiment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CryptopanicFetcher pulls news posts from cryptopanic.com public API.
//
// The public endpoint requires no auth but the free tier is rate-limited to
// roughly 200 req/day. Use a generous CacheTTL on CryptopanicNews to stay
// well under that quota.
//
// Endpoint: GET https://cryptopanic.com/api/v1/posts/?currencies={SYMBOL}&filter=hot&public=true
type CryptopanicFetcher struct {
	BaseURL string
	Client  *http.Client
}

// NewCryptopanicFetcher returns a fetcher with sensible production defaults.
func NewCryptopanicFetcher() *CryptopanicFetcher {
	return &CryptopanicFetcher{
		BaseURL: "https://cryptopanic.com",
		Client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// RecentNews returns posts for the given symbol filtered to those published
// after `since`. `symbol` may be a base currency like "ETH" or a perpetual
// pair like "ETHUSDT" — the fetcher strips known quote suffixes before
// querying CryptoPanic, which expects only the base currency.
func (c *CryptopanicFetcher) RecentNews(ctx context.Context, symbol string, since time.Time) ([]NewsItem, error) {
	cur := stripQuoteCurrency(symbol)
	url := fmt.Sprintf("%s/api/v1/posts/?currencies=%s&filter=hot&public=true", c.BaseURL, cur)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cryptopanic: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Results []struct {
			Title       string    `json:"title"`
			PublishedAt time.Time `json:"published_at"`
			Votes       struct {
				Positive int `json:"positive"`
				Negative int `json:"negative"`
			} `json:"votes"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := make([]NewsItem, 0, len(payload.Results))
	for _, r := range payload.Results {
		if r.PublishedAt.Before(since) {
			continue
		}
		out = append(out, NewsItem{
			Title:       r.Title,
			PublishedAt: r.PublishedAt,
			Bullish:     r.Votes.Positive,
			Bearish:     r.Votes.Negative,
		})
	}
	return out, nil
}

// stripQuoteCurrency converts "ETHUSDT" → "ETH" etc. for the CryptoPanic API
// which expects the base currency only. Returns input unchanged when no
// known quote suffix matches.
func stripQuoteCurrency(sym string) string {
	for _, suffix := range []string{"USDT", "USDC", "BUSD", "USD"} {
		if strings.HasSuffix(sym, suffix) && len(sym) > len(suffix) {
			return sym[:len(sym)-len(suffix)]
		}
	}
	return sym
}
