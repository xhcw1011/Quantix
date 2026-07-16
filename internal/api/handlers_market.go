package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

var tickerClient = &http.Client{Timeout: 5 * time.Second}

// handleTicker returns the latest price for a symbol from Binance USDM's public
// ticker endpoint (no credentials needed) — used to show the current price while
// setting up an order. Returns 502 if the symbol has no price there.
func (s *Server) handleTicker(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	if symbol == "" {
		jsonError(w, "symbol required", http.StatusBadRequest)
		return
	}
	resp, err := tickerClient.Get("https://fapi.binance.com/fapi/v1/ticker/price?symbol=" + url.QueryEscape(symbol))
	if err != nil {
		jsonError(w, "price fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	var body struct {
		Symbol string `json:"symbol"`
		Price  string `json:"price"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil || body.Price == "" {
		jsonError(w, "price unavailable for this symbol", http.StatusBadGateway)
		return
	}
	jsonOK(w, map[string]any{"symbol": body.Symbol, "price": body.Price})
}
