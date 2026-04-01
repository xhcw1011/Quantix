// Package bybit implements exchange.RESTClient and exchange.WSClient for Bybit.
package bybit

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
	restBase = "https://api.bybit.com"
	wsBase   = "wss://stream.bybit.com/v5/public/spot"
)

func toBybitInterval(interval string) string {
	m := map[string]string{
		"1m": "1", "3m": "3", "5m": "5", "15m": "15", "30m": "30",
		"1h": "60", "2h": "120", "4h": "240", "6h": "360", "12h": "720",
		"1d": "D", "1w": "W",
	}
	if v, ok := m[interval]; ok {
		return v
	}
	return interval
}

// ─── REST ─────────────────────────────────────────────────────────────────────

// RESTClient is a Bybit REST market data client.
type RESTClient struct {
	http *http.Client
	log  *zap.Logger
}

// NewRESTClient creates a new Bybit REST client.
func NewRESTClient(log *zap.Logger) *RESTClient {
	return &RESTClient{http: &http.Client{Timeout: 15 * time.Second}, log: log}
}

// GetKlines fetches the most recent `limit` klines.
func (c *RESTClient) GetKlines(ctx context.Context, symbol, interval string, limit int) ([]exchange.Kline, error) {
	url := fmt.Sprintf("%s/v5/market/kline?category=spot&symbol=%s&interval=%s&limit=%d",
		restBase, symbol, toBybitInterval(interval), limit)
	return c.fetchKlines(ctx, url, symbol, interval)
}

// GetKlinesBetween fetches klines between start and end times.
func (c *RESTClient) GetKlinesBetween(ctx context.Context, symbol, interval string, start, end time.Time, limit int) ([]exchange.Kline, error) {
	url := fmt.Sprintf("%s/v5/market/kline?category=spot&symbol=%s&interval=%s&start=%d&end=%d&limit=%d",
		restBase, symbol, toBybitInterval(interval), start.UnixMilli(), end.UnixMilli(), limit)
	return c.fetchKlines(ctx, url, symbol, interval)
}

// GetServerTime fetches Bybit server time.
func (c *RESTClient) GetServerTime(ctx context.Context) (time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, restBase+"/v5/market/time", nil)
	if err != nil {
		return time.Time{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return time.Time{}, fmt.Errorf("Bybit server time HTTP %d", resp.StatusCode)
	}

	var result struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			TimeSecond string `json:"timeSecond"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return time.Time{}, err
	}
	if result.RetCode != 0 {
		return time.Time{}, fmt.Errorf("Bybit API %d: %s", result.RetCode, result.RetMsg)
	}
	sec, err := strconv.ParseInt(result.Result.TimeSecond, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(sec, 0), nil
}

type bybitResp struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List [][]string `json:"list"`
	} `json:"result"`
}

func (c *RESTClient) fetchKlines(ctx context.Context, url, symbol, interval string) ([]exchange.Kline, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("Bybit read body: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Bybit HTTP %d: %s", resp.StatusCode, string(body))
	}
	var result bybitResp
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("Bybit decode: %w", err)
	}
	if result.RetCode != 0 {
		return nil, fmt.Errorf("Bybit API %d: %s", result.RetCode, result.RetMsg)
	}

	klines := make([]exchange.Kline, 0, len(result.Result.List))
	for _, row := range result.Result.List {
		// [startTime, open, high, low, close, volume, turnover]
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
		if len(row) >= 7 {
			qvol, _ = strconv.ParseFloat(row[6], 64)
		}
		if open <= 0 || high <= 0 || low <= 0 || close_ <= 0 {
			continue // skip malformed bar — zero/negative price is never valid
		}
		openTime := time.UnixMilli(ts)
		klines = append(klines, exchange.Kline{
			Symbol: symbol, Interval: interval,
			OpenTime: openTime, CloseTime: openTime,
			Open: open, High: high, Low: low, Close: close_,
			Volume: vol, QuoteVolume: qvol, IsClosed: true,
		})
	}
	// Bybit returns newest first; reverse to chronological
	for i, j := 0, len(klines)-1; i < j; i, j = i+1, j-1 {
		klines[i], klines[j] = klines[j], klines[i]
	}
	return klines, nil
}

// ─── WebSocket ────────────────────────────────────────────────────────────────

// WSClient is a Bybit WebSocket market data client.
type WSClient struct {
	log *zap.Logger
}

// NewWSClient creates a new Bybit WebSocket client.
func NewWSClient(log *zap.Logger) *WSClient {
	return &WSClient{log: log}
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
			w.log.Error("Bybit kline WS error", zap.Error(err))
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
			w.log.Error("Bybit ticker WS error", zap.Error(err))
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

	var topics []string
	for _, sym := range symbols {
		for _, itv := range intervals {
			topics = append(topics, fmt.Sprintf("kline.%s.%s", toBybitInterval(itv), sym))
		}
	}
	if err := conn.WriteJSON(map[string]any{"op": "subscribe", "args": topics}); err != nil {
		return err
	}

	revItvMap := make(map[string]string)
	for _, iv := range intervals {
		revItvMap[toBybitInterval(iv)] = iv
	}

	return readLoop(ctx, conn, func(data []byte) {
		var msg struct {
			Topic string `json:"topic"`
			Data  struct {
				Start    int64  `json:"start"`
				End      int64  `json:"end"`
				Interval string `json:"interval"`
				Open     string `json:"open"`
				Close    string `json:"close"`
				High     string `json:"high"`
				Low      string `json:"low"`
				Volume   string `json:"volume"`
				Turnover string `json:"turnover"`
				Confirm  bool   `json:"confirm"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			w.log.Debug("Bybit kline WS unmarshal failed", zap.Error(err))
			return
		}
		if msg.Topic == "" {
			return
		}
		parts := strings.SplitN(msg.Topic, ".", 3)
		if len(parts) != 3 {
			return
		}
		symbol := parts[2]
		origItv := revItvMap[parts[1]]
		open, _ := strconv.ParseFloat(msg.Data.Open, 64)
		high, _ := strconv.ParseFloat(msg.Data.High, 64)
		low, _ := strconv.ParseFloat(msg.Data.Low, 64)
		close_, _ := strconv.ParseFloat(msg.Data.Close, 64)
		if open <= 0 || high <= 0 || low <= 0 || close_ <= 0 {
			return // skip malformed bar
		}
		vol, _ := strconv.ParseFloat(msg.Data.Volume, 64)
		qvol, _ := strconv.ParseFloat(msg.Data.Turnover, 64)
		handler(exchange.Kline{
			Symbol: symbol, Interval: origItv,
			OpenTime:  time.UnixMilli(msg.Data.Start),
			CloseTime: time.UnixMilli(msg.Data.End),
			Open: open, High: high, Low: low, Close: close_,
			Volume: vol, QuoteVolume: qvol, IsClosed: msg.Data.Confirm,
		})
	})
}

func (w *WSClient) runTickerWS(ctx context.Context, symbols []string, handler exchange.TickerHandler) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsBase, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	var topics []string
	for _, sym := range symbols {
		topics = append(topics, "tickers."+sym)
	}
	if err := conn.WriteJSON(map[string]any{"op": "subscribe", "args": topics}); err != nil {
		return err
	}

	return readLoop(ctx, conn, func(data []byte) {
		var msg struct {
			Data struct {
				Symbol    string `json:"symbol"`
				Bid1Price string `json:"bid1Price"`
				Ask1Price string `json:"ask1Price"`
				LastPrice string `json:"lastPrice"`
				Volume24H string `json:"volume24h"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			w.log.Debug("Bybit ticker WS unmarshal failed", zap.Error(err))
			return
		}
		if msg.Data.Symbol == "" {
			return
		}
		bid, _ := strconv.ParseFloat(msg.Data.Bid1Price, 64)
		ask, _ := strconv.ParseFloat(msg.Data.Ask1Price, 64)
		last, _ := strconv.ParseFloat(msg.Data.LastPrice, 64)
		if bid <= 0 || ask <= 0 || last <= 0 {
			return // skip malformed ticker
		}
		vol, _ := strconv.ParseFloat(msg.Data.Volume24H, 64)
		handler(exchange.Ticker{
			Symbol: msg.Data.Symbol, BidPrice: bid, AskPrice: ask,
			LastPrice: last, Volume: vol, Timestamp: time.Now(),
		})
	})
}

func readLoop(ctx context.Context, conn *websocket.Conn, handle func([]byte)) error {
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
