package sentiment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// BinanceBasisFetcher fetches (markPrice - indexPrice)/indexPrice from
// Binance Futures /fapi/v1/premiumIndex. Same endpoint as funding fetcher
// but extracting different fields. Public, no auth required.
type BinanceBasisFetcher struct {
	BaseURL string
	Client  *http.Client
}

func NewBinanceBasisFetcher() *BinanceBasisFetcher {
	return &BinanceBasisFetcher{
		BaseURL: "https://fapi.binance.com",
		Client:  &http.Client{Timeout: 5 * time.Second},
	}
}

// LatestBasis returns the current basis (perp - spot) / spot.
func (b *BinanceBasisFetcher) LatestBasis(ctx context.Context, symbol string) (float64, error) {
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
		return 0, fmt.Errorf("premiumIndex HTTP %d", resp.StatusCode)
	}
	var payload struct {
		MarkPrice  string `json:"markPrice"`
		IndexPrice string `json:"indexPrice"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, err
	}
	mark, err := strconv.ParseFloat(payload.MarkPrice, 64)
	if err != nil {
		return 0, fmt.Errorf("parse mark %q: %w", payload.MarkPrice, err)
	}
	idx, err := strconv.ParseFloat(payload.IndexPrice, 64)
	if err != nil {
		return 0, fmt.Errorf("parse index %q: %w", payload.IndexPrice, err)
	}
	if idx == 0 {
		return 0, fmt.Errorf("zero index price")
	}
	return (mark - idx) / idx, nil
}
