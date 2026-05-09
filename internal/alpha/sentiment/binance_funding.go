package sentiment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// BinanceFundingFetcher fetches perpetual funding rates from Binance Futures.
// Endpoint: GET /fapi/v1/premiumIndex?symbol=ETHUSDT
// Public, no auth required, generous rate limits.
type BinanceFundingFetcher struct {
	BaseURL string       // default "https://fapi.binance.com"
	Client  *http.Client // default 5s timeout
}

// NewBinanceFundingFetcher returns a fetcher with sensible defaults.
func NewBinanceFundingFetcher() *BinanceFundingFetcher {
	return &BinanceFundingFetcher{
		BaseURL: "https://fapi.binance.com",
		Client:  &http.Client{Timeout: 5 * time.Second},
	}
}

// LatestFunding fetches the current funding rate for the given perpetual
// symbol. Returns the rate as a fraction (e.g. 0.0001 = 0.01% per 8h).
func (b *BinanceFundingFetcher) LatestFunding(ctx context.Context, symbol string) (float64, error) {
	url := fmt.Sprintf("%s/fapi/v1/premiumIndex?symbol=%s", b.BaseURL, symbol)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := b.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("binance premiumIndex: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		LastFundingRate string `json:"lastFundingRate"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, fmt.Errorf("decode: %w", err)
	}
	rate, err := strconv.ParseFloat(payload.LastFundingRate, 64)
	if err != nil {
		return 0, fmt.Errorf("parse rate %q: %w", payload.LastFundingRate, err)
	}
	return rate, nil
}
