// Package okx implements exchange.RESTClient and exchange.WSClient for OKX.
package okx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
)

const (
	restBase = "https://www.okx.com"
	wsBase   = "wss://ws.okx.com:8443/ws/v5/public"
)

// intervalMap converts Quantix interval strings to OKX bar strings.
var intervalMap = map[string]string{
	"1m": "1m", "3m": "3m", "5m": "5m", "15m": "15m", "30m": "30m",
	"1h": "1H", "2h": "2H", "4h": "4H", "6h": "6H", "12h": "12H",
	"1d": "1D", "1w": "1W",
}

// toOKXSymbol converts Binance-style symbol (BTCUSDT) to OKX format (BTC-USDT).
func toOKXSymbol(symbol string) string {
	if strings.HasSuffix(symbol, "USDT") {
		return symbol[:len(symbol)-4] + "-USDT"
	}
	if strings.HasSuffix(symbol, "BTC") {
		return symbol[:len(symbol)-3] + "-BTC"
	}
	return symbol
}

func toOKXInterval(interval string) string {
	if v, ok := intervalMap[interval]; ok {
		return v
	}
	return interval
}

// ─── REST ─────────────────────────────────────────────────────────────────────

// RESTClient is an OKX REST market data client.
type RESTClient struct {
	http       *http.Client
	log        *zap.Logger
	marketType string // "swap" → use SWAP instId; else → spot instId
}

// NewRESTClient creates a new OKX REST client.
// marketType: "swap" fetches SWAP (perpetual) klines; "" or "spot" fetches spot klines.
func NewRESTClient(log *zap.Logger, marketType string) *RESTClient {
	return &RESTClient{
		http:       &http.Client{Timeout: 15 * time.Second},
		log:        log,
		marketType: marketType,
	}
}

// instID returns the OKX instrument ID for the given symbol based on marketType.
func (c *RESTClient) instID(symbol string) string {
	if c.marketType == "swap" {
		return toOKXSWAPSymbol(symbol)
	}
	return toOKXSymbol(symbol)
}

// GetKlines fetches the most recent `limit` klines.
func (c *RESTClient) GetKlines(ctx context.Context, symbol, interval string, limit int) ([]exchange.Kline, error) {
	url := fmt.Sprintf("%s/api/v5/market/candles?instId=%s&bar=%s&limit=%d",
		restBase, c.instID(symbol), toOKXInterval(interval), limit)
	return c.fetchCandles(ctx, url, symbol, interval)
}

// GetKlinesBetween fetches klines between start and end using the history endpoint.
func (c *RESTClient) GetKlinesBetween(ctx context.Context, symbol, interval string, start, end time.Time, limit int) ([]exchange.Kline, error) {
	url := fmt.Sprintf("%s/api/v5/market/history-candles?instId=%s&bar=%s&before=%d&after=%d&limit=%d",
		restBase, c.instID(symbol), toOKXInterval(interval), start.UnixMilli(), end.UnixMilli(), limit)
	return c.fetchCandles(ctx, url, symbol, interval)
}

// GetServerTime fetches OKX server time.
func (c *RESTClient) GetServerTime(ctx context.Context) (time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, restBase+"/api/v5/public/time", nil)
	if err != nil {
		return time.Time{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return time.Time{}, fmt.Errorf("OKX server time HTTP %d", resp.StatusCode)
	}

	var result struct {
		Data []struct{ Ts string `json:"ts"` } `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return time.Time{}, err
	}
	if len(result.Data) == 0 {
		return time.Time{}, fmt.Errorf("OKX: empty time response")
	}
	ms, err := strconv.ParseInt(result.Data[0].Ts, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms), nil
}

type okxCandleResp struct {
	Code string     `json:"code"`
	Msg  string     `json:"msg"`
	Data [][]string `json:"data"`
}

func (c *RESTClient) fetchCandles(ctx context.Context, url, symbol, interval string) ([]exchange.Kline, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("OKX read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OKX HTTP %d: %s", resp.StatusCode, string(body))
	}
	var result okxCandleResp
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("OKX decode: %w", err)
	}
	if result.Code != "0" {
		return nil, fmt.Errorf("OKX API %s: %s", result.Code, result.Msg)
	}

	klines := make([]exchange.Kline, 0, len(result.Data))
	for _, row := range result.Data {
		// [ts, open, high, low, close, vol, volCcy, volCcyQuote, confirm]
		if len(row) < 6 {
			continue
		}
		ts, _ := strconv.ParseInt(row[0], 10, 64)
		open, _ := strconv.ParseFloat(row[1], 64)
		high, _ := strconv.ParseFloat(row[2], 64)
		low, _ := strconv.ParseFloat(row[3], 64)
		close_, _ := strconv.ParseFloat(row[4], 64)
		vol, _ := strconv.ParseFloat(row[5], 64)
		var qvol float64
		if len(row) >= 8 {
			qvol, _ = strconv.ParseFloat(row[7], 64)
		}
		if open <= 0 || high <= 0 || low <= 0 || close_ <= 0 {
			continue // skip malformed bar — zero/negative price is never valid
		}
		openTime := time.UnixMilli(ts)
		klines = append(klines, exchange.Kline{
			Symbol:      symbol,
			Interval:    interval,
			OpenTime:    openTime,
			CloseTime:   openTime,
			Open:        open,
			High:        high,
			Low:         low,
			Close:       close_,
			Volume:      vol,
			QuoteVolume: qvol,
			IsClosed:    len(row) >= 9 && row[8] == "1",
		})
	}
	// OKX returns newest first; reverse to chronological
	for i, j := 0, len(klines)-1; i < j; i, j = i+1, j-1 {
		klines[i], klines[j] = klines[j], klines[i]
	}
	return klines, nil
}

// ─── WebSocket ────────────────────────────────────────────────────────────────

// WSClient is an OKX WebSocket market data client.
type WSClient struct {
	log        *zap.Logger
	marketType string // "swap" → subscribe to SWAP instId; else → spot
}

// NewWSClient creates a new OKX WebSocket client.
// marketType: "swap" subscribes to SWAP (perpetual) klines; "" or "spot" subscribes to spot klines.
func NewWSClient(log *zap.Logger, marketType string) *WSClient {
	return &WSClient{log: log, marketType: marketType}
}

// instID returns the OKX instrument ID for the given symbol based on marketType.
func (w *WSClient) instID(symbol string) string {
	if w.marketType == "swap" {
		return toOKXSWAPSymbol(symbol)
	}
	return toOKXSymbol(symbol)
}

// SubscribeKlines runs until ctx is cancelled, reconnecting on error.
func (w *WSClient) SubscribeKlines(ctx context.Context, symbols, intervals []string, handler exchange.KlineHandler) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := w.runKlineWS(ctx, symbols, intervals, handler); err != nil {
			w.log.Error("OKX kline WS error", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// SubscribeTickers runs until ctx is cancelled, reconnecting on error.
func (w *WSClient) SubscribeTickers(ctx context.Context, symbols []string, handler exchange.TickerHandler) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := w.runTickerWS(ctx, symbols, handler); err != nil {
			w.log.Error("OKX ticker WS error", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (w *WSClient) runKlineWS(ctx context.Context, symbols, intervals []string, handler exchange.KlineHandler) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsBase, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	type chanArg struct {
		Channel string `json:"channel"`
		InstID  string `json:"instId"`
	}
	var args []chanArg
	for _, sym := range symbols {
		for _, itv := range intervals {
			args = append(args, chanArg{Channel: "candle" + toOKXInterval(itv), InstID: w.instID(sym)})
		}
	}
	if err := conn.WriteJSON(map[string]any{"op": "subscribe", "args": args}); err != nil {
		return err
	}

	// reverse lookup maps
	symMap := make(map[string]string)
	for _, s := range symbols {
		symMap[w.instID(s)] = s
	}
	itvMap := make(map[string]string)
	for _, iv := range intervals {
		itvMap[toOKXInterval(iv)] = iv
	}

	return w.readLoop(ctx, conn, func(data []byte) {
		var msg struct {
			Arg struct {
				Channel string `json:"channel"`
				InstID  string `json:"instId"`
			} `json:"arg"`
			Data [][]string `json:"data"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			w.log.Debug("OKX kline WS unmarshal failed", zap.Error(err))
			return
		}
		if msg.Arg.Channel == "" || len(msg.Data) == 0 {
			return
		}
		origSym := symMap[msg.Arg.InstID]
		barStr := strings.TrimPrefix(msg.Arg.Channel, "candle")
		origItv := itvMap[barStr]
		if origSym == "" || origItv == "" {
			return
		}
		for _, row := range msg.Data {
			if len(row) < 6 {
				continue
			}
			ts, _ := strconv.ParseInt(row[0], 10, 64)
			open, _ := strconv.ParseFloat(row[1], 64)
			high, _ := strconv.ParseFloat(row[2], 64)
			low, _ := strconv.ParseFloat(row[3], 64)
			close_, _ := strconv.ParseFloat(row[4], 64)
			vol, _ := strconv.ParseFloat(row[5], 64)
			if open <= 0 || high <= 0 || low <= 0 || close_ <= 0 {
				continue // skip malformed bar
			}
			var qvol float64
			if len(row) >= 8 {
				qvol, _ = strconv.ParseFloat(row[7], 64)
			}
			handler(exchange.Kline{
				Symbol:      origSym,
				Interval:    origItv,
				OpenTime:    time.UnixMilli(ts),
				CloseTime:   time.UnixMilli(ts),
				Open:        open,
				High:        high,
				Low:         low,
				Close:       close_,
				Volume:      vol,
				QuoteVolume: qvol,
				IsClosed:    len(row) >= 9 && row[8] == "1",
			})
		}
	})
}

func (w *WSClient) runTickerWS(ctx context.Context, symbols []string, handler exchange.TickerHandler) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsBase, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	type chanArg struct {
		Channel string `json:"channel"`
		InstID  string `json:"instId"`
	}
	var args []chanArg
	for _, sym := range symbols {
		args = append(args, chanArg{Channel: "tickers", InstID: w.instID(sym)})
	}
	if err := conn.WriteJSON(map[string]any{"op": "subscribe", "args": args}); err != nil {
		return err
	}

	symMap := make(map[string]string)
	for _, s := range symbols {
		symMap[w.instID(s)] = s
	}

	return w.readLoop(ctx, conn, func(data []byte) {
		var msg struct {
			Data []struct {
				InstID string `json:"instId"`
				BidPx  string `json:"bidPx"`
				AskPx  string `json:"askPx"`
				Last   string `json:"last"`
				Vol24H string `json:"vol24h"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			w.log.Debug("OKX ticker WS unmarshal failed", zap.Error(err))
			return
		}
		for _, d := range msg.Data {
			origSym, ok := symMap[d.InstID]
			if !ok {
				origSym = d.InstID
			}
			bid, _ := strconv.ParseFloat(d.BidPx, 64)
			ask, _ := strconv.ParseFloat(d.AskPx, 64)
			last, _ := strconv.ParseFloat(d.Last, 64)
			if bid <= 0 || ask <= 0 || last <= 0 {
				continue // skip malformed ticker
			}
			vol, _ := strconv.ParseFloat(d.Vol24H, 64)
			handler(exchange.Ticker{
				Symbol: origSym, BidPrice: bid, AskPrice: ask,
				LastPrice: last, Volume: vol, Timestamp: time.Now(),
			})
		}
	})
}

func (w *WSClient) readLoop(ctx context.Context, conn *websocket.Conn, handle func([]byte)) error {
	done := make(chan struct{})
	var (
		mu      sync.Mutex
		readErr error
	)
	go func() {
		defer close(done)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				mu.Lock()
				readErr = err
				mu.Unlock()
				return
			}
			handle(data)
		}
	}()
	select {
	case <-ctx.Done():
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		return nil
	case <-done:
		mu.Lock()
		defer mu.Unlock()
		return readErr
	}
}

// Compile-time assertions.
var _ exchange.RESTClient = (*RESTClient)(nil)
var _ exchange.WSClient = (*WSClient)(nil)
