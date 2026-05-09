package sentiment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// WhaleAlertFetcher pulls whale transfers from whale-alert.io API.
//
// Free tier: 100 req/day, requires API key (register at whale-alert.io).
// API key passed via constructor; alpha caches aggressively to stay under quota.
//
// Endpoint: GET https://api.whale-alert.io/v1/transactions
//
//	?api_key=...&start=<unix>&min_value=1000000&currency=eth
type WhaleAlertFetcher struct {
	APIKey   string
	BaseURL  string
	MinValue int // minimum USD value, default 1M
	Client   *http.Client
}

// NewWhaleAlertFetcher returns a fetcher with sensible production defaults.
func NewWhaleAlertFetcher(apiKey string) *WhaleAlertFetcher {
	return &WhaleAlertFetcher{
		APIKey:   apiKey,
		BaseURL:  "https://api.whale-alert.io",
		MinValue: 1_000_000,
		Client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// RecentFlows returns flows since the cutoff. `symbol` is mapped to the API's
// currency code (e.g. "ETHUSDT" → "eth").
func (w *WhaleAlertFetcher) RecentFlows(ctx context.Context, symbol string, since time.Time) ([]WhaleFlow, error) {
	if w.APIKey == "" {
		return nil, fmt.Errorf("whale-alert: missing API key")
	}
	currency := strings.ToLower(stripQuoteCurrency(symbol))
	url := fmt.Sprintf("%s/v1/transactions?api_key=%s&start=%d&min_value=%d&currency=%s",
		w.BaseURL, w.APIKey, since.Unix(), w.MinValue, currency)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := w.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whale-alert: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Result       string `json:"result"`
		Transactions []struct {
			Timestamp int64   `json:"timestamp"`
			AmountUSD float64 `json:"amount_usd"`
			From      struct {
				OwnerType string `json:"owner_type"`
			} `json:"from"`
			To struct {
				OwnerType string `json:"owner_type"`
			} `json:"to"`
		} `json:"transactions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := make([]WhaleFlow, 0, len(payload.Transactions))
	for _, tx := range payload.Transactions {
		out = append(out, WhaleFlow{
			Timestamp:    time.Unix(tx.Timestamp, 0),
			AmountUSD:    tx.AmountUSD,
			ToExchange:   tx.To.OwnerType == "exchange",
			FromExchange: tx.From.OwnerType == "exchange",
		})
	}
	return out, nil
}
