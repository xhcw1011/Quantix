package okx

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
)

const (
	okxAPIBase = "https://www.okx.com"
)

// OrderBroker submits SWAP (perpetual) or Spot orders to OKX.
// It implements exchange.OrderClient.
type OrderBroker struct {
	httpClient *http.Client
	apiKey     string
	apiSecret  string
	passphrase string
	demo       bool   // true → x-simulated-trading: 1
	marketType string // "swap" or "spot"
	ctValCache sync.Map // instId → float64 (lazy-loaded contract size)
	log        *zap.Logger
}

// NewOrderBroker creates an OKX OrderBroker.
//
// Safety gates:
//   - demo=true  → x-simulated-trading: 1 header (no real money)
//   - demo=false → requires env QUANTIX_LIVE_CONFIRM=true
//
// marketType: "swap" (BTC-USDT-SWAP perpetual) or "spot" (BTC-USDT).
func NewOrderBroker(apiKey, apiSecret, passphrase string, demo bool, marketType string, log *zap.Logger) (*OrderBroker, error) {
	if marketType == "" {
		marketType = "swap"
	}

	if demo {
		log.Info("OKX order broker: DEMO trading mode",
			zap.String("market_type", marketType))
	} else {
		if os.Getenv("QUANTIX_LIVE_CONFIRM") != "true" {
			return nil, fmt.Errorf(
				"OKX live mode requires QUANTIX_LIVE_CONFIRM=true env var; " +
					"set it explicitly to confirm real-money trading")
		}
		log.Warn("OKX order broker: LIVE mode — REAL MONEY AT RISK",
			zap.String("market_type", marketType))
	}

	b := &OrderBroker{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		apiKey:     apiKey,
		apiSecret:  apiSecret,
		passphrase: passphrase,
		demo:       demo,
		marketType: marketType,
		log:        log,
	}

	// Verify connectivity via balance check
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := b.GetBalance(ctx, "USDT"); err != nil {
		return nil, fmt.Errorf("OKX API connectivity check failed: %w", err)
	}

	log.Info("OKX order broker ready")
	return b, nil
}

// PlaceMarketOrder submits a market order. For SWAP, qty is in base-asset units (BTC);
// it is converted to contracts internally using the instrument's ctVal.
// positionSide: "long", "short", or "" (net mode).
func (b *OrderBroker) PlaceMarketOrder(ctx context.Context, symbol string, side exchange.OrderSide, positionSide string, qty float64, clientOrderID string) (exchange.OrderFill, error) {
	instID := b.toInstID(symbol)

	var okxSide string
	switch side {
	case exchange.OrderSideBuy:
		okxSide = "buy"
	case exchange.OrderSideSell:
		okxSide = "sell"
	default:
		return exchange.OrderFill{}, fmt.Errorf("unknown side: %s", side)
	}

	var sz string
	if b.marketType == "swap" {
		ctVal, err := b.fetchCtVal(ctx, instID)
		if err != nil {
			return exchange.OrderFill{}, fmt.Errorf("fetch ctVal: %w", err)
		}
		contracts := math.Floor(qty / ctVal)
		if contracts < 1 {
			return exchange.OrderFill{}, fmt.Errorf(
				"qty %.8f BTC is too small for 1 contract (ctVal=%.8f); minimum qty=%.8f BTC",
				qty, ctVal, ctVal)
		}
		sz = strconv.FormatInt(int64(contracts), 10)
	} else {
		sz = fmt.Sprintf("%.8f", qty)
	}

	type orderReq struct {
		InstID   string `json:"instId"`
		TdMode   string `json:"tdMode"`
		Side     string `json:"side"`
		PosSide  string `json:"posSide,omitempty"`
		OrdType  string `json:"ordType"`
		Sz       string `json:"sz"`
		ClOrdId  string `json:"clOrdId,omitempty"`
	}

	tdMode := "cash" // spot
	if b.marketType == "swap" {
		tdMode = "cross" // cross-margin perpetual
	}

	reqBody := orderReq{
		InstID:  instID,
		TdMode:  tdMode,
		Side:    okxSide,
		OrdType: "market",
		Sz:      sz,
		ClOrdId: clientOrderID,
	}
	if positionSide != "" {
		reqBody.PosSide = strings.ToLower(positionSide)
	}

	var placeResp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			OrdID   string `json:"ordId"`
			SCode   string `json:"sCode"`
			SMsg    string `json:"sMsg"`
		} `json:"data"`
	}

	if err := b.post(ctx, "/api/v5/trade/order", reqBody, &placeResp); err != nil {
		return exchange.OrderFill{}, fmt.Errorf("OKX place order: %w", err)
	}
	if placeResp.Code != "0" {
		return exchange.OrderFill{}, fmt.Errorf("OKX place order API error %s: %s", placeResp.Code, placeResp.Msg)
	}
	if len(placeResp.Data) == 0 {
		return exchange.OrderFill{}, fmt.Errorf("OKX place order: empty data response")
	}
	if placeResp.Data[0].SCode != "0" {
		return exchange.OrderFill{}, fmt.Errorf("OKX place order rejected: %s — %s",
			placeResp.Data[0].SCode, placeResp.Data[0].SMsg)
	}

	ordID := placeResp.Data[0].OrdID

	// Poll for fill details (market orders typically fill immediately)
	fill, err := b.pollOrderFill(ctx, instID, ordID)
	if err != nil {
		// A market order that doesn't confirm fill is a critical uncertainty:
		// the order is on the exchange but we don't know the fill state.
		// Propagate the error so the caller can handle it (retry/reconcile).
		b.log.Error("OKX market order placed but fill unconfirmed — order may be live on exchange",
			zap.String("ord_id", ordID), zap.String("inst_id", instID), zap.Error(err))
		return exchange.OrderFill{
			ExchangeID: ordID,
			Status:     "live",
		}, fmt.Errorf("OKX market order %s placed but fill unconfirmed: %w", ordID, err)
	}

	b.log.Info("OKX market order filled",
		zap.String("exchange_id", fill.ExchangeID),
		zap.String("inst_id", instID),
		zap.String("side", okxSide),
		zap.Float64("filled_qty", fill.FilledQty),
		zap.Float64("avg_price", fill.AvgPrice),
		zap.Float64("fee", fill.Fee),
	)
	return fill, nil
}

// pollOrderFill polls the order status up to 3 times waiting for "filled".
func (b *OrderBroker) pollOrderFill(ctx context.Context, instID, ordID string) (exchange.OrderFill, error) {
	const maxTries = 3
	const retryDelay = 200 * time.Millisecond

	type orderDetail struct {
		OrdID  string `json:"ordId"`
		State  string `json:"state"` // "filled", "partially_filled", "live", "canceled"
		FillSz string `json:"fillSz"`
		AvgPx  string `json:"avgPx"`
		Fee    string `json:"fee"`
	}

	var resp struct {
		Code string        `json:"code"`
		Msg  string        `json:"msg"`
		Data []orderDetail `json:"data"`
	}

	path := fmt.Sprintf("/api/v5/trade/order?instId=%s&ordId=%s", instID, ordID)
	for attempt := 0; attempt < maxTries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return exchange.OrderFill{}, ctx.Err()
			case <-time.After(retryDelay):
			}
		}

		if err := b.get(ctx, path, &resp); err != nil {
			continue
		}
		if resp.Code != "0" || len(resp.Data) == 0 {
			continue
		}

		d := resp.Data[0]
		if d.State == "filled" || d.State == "partially_filled" {
			fillSz, errSz := strconv.ParseFloat(d.FillSz, 64)
			if errSz != nil {
				b.log.Error("pollOrderFill: unparseable fillSz, skipping fill",
					zap.String("ord_id", ordID), zap.String("raw", d.FillSz), zap.Error(errSz))
				continue
			}
			avgPx, errPx := strconv.ParseFloat(d.AvgPx, 64)
			if errPx != nil {
				b.log.Error("pollOrderFill: unparseable avgPx, skipping fill",
					zap.String("ord_id", ordID), zap.String("raw", d.AvgPx), zap.Error(errPx))
				continue
			}
			fee, errFee := strconv.ParseFloat(d.Fee, 64)
			if errFee != nil {
				b.log.Warn("pollOrderFill: unparseable fee, defaulting to 0",
					zap.String("ord_id", ordID), zap.String("raw", d.Fee), zap.Error(errFee))
				fee = 0
			}
			if fee < 0 {
				fee = -fee // OKX returns fees as negative
			}
			// For SWAP orders, OKX returns fillSz in contracts, not base-asset units.
			// Convert back to base-asset units by multiplying by ctVal.
			if b.marketType == "swap" {
				ctVal, ctErr := b.fetchCtVal(ctx, instID)
				if ctErr != nil {
					return exchange.OrderFill{}, fmt.Errorf("pollOrderFill: fetch ctVal for qty conversion: %w", ctErr)
				}
				fillSz *= ctVal
			}
			return exchange.OrderFill{
				ExchangeID: ordID,
				FilledQty:  fillSz,
				AvgPrice:   avgPx,
				Fee:        fee,
				Status:     d.State,
			}, nil
		}
	}

	return exchange.OrderFill{}, fmt.Errorf("order %s not filled after %d attempts", ordID, maxTries)
}

// CancelOrder cancels an OKX order.
func (b *OrderBroker) CancelOrder(ctx context.Context, symbol, exchangeID string) error {
	instID := b.toInstID(symbol)

	type cancelReq struct {
		InstID string `json:"instId"`
		OrdID  string `json:"ordId"`
	}

	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}

	if err := b.post(ctx, "/api/v5/trade/cancel-order", cancelReq{InstID: instID, OrdID: exchangeID}, &resp); err != nil {
		return fmt.Errorf("OKX cancel order: %w", err)
	}
	if resp.Code != "0" {
		return fmt.Errorf("OKX cancel order API error %s: %s", resp.Code, resp.Msg)
	}
	return nil
}

// CancelAllOpenOrders implements exchange.OpenOrdersCanceller.
// Cancels all pending/live orders for the given symbol on OKX (SWAP or Spot).
// Used by live.Engine on startup (clean-slate) to clear orphaned orders.
func (b *OrderBroker) CancelAllOpenOrders(ctx context.Context, symbol string) error {
	instID := b.toInstID(symbol)

	// First, fetch all open orders for this instrument.
	path := fmt.Sprintf("/api/v5/trade/orders-pending?instId=%s", instID)
	var listResp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			OrdID string `json:"ordId"`
		} `json:"data"`
	}
	if err := b.get(ctx, path, &listResp); err != nil {
		return fmt.Errorf("OKX list open orders: %w", err)
	}
	if len(listResp.Data) == 0 {
		return nil
	}

	// Cancel each order individually (OKX batch cancel requires explicit order IDs).
	type cancelItem struct {
		InstID string `json:"instId"`
		OrdID  string `json:"ordId"`
	}
	items := make([]cancelItem, 0, len(listResp.Data))
	for _, ord := range listResp.Data {
		items = append(items, cancelItem{InstID: instID, OrdID: ord.OrdID})
	}

	var cancelResp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := b.post(ctx, "/api/v5/trade/cancel-batch-orders", items, &cancelResp); err != nil {
		return fmt.Errorf("OKX cancel batch orders: %w", err)
	}
	if cancelResp.Code != "0" {
		return fmt.Errorf("OKX cancel batch orders API error %s: %s", cancelResp.Code, cancelResp.Msg)
	}
	return nil
}

// GetBalance returns the free balance for the given currency.
func (b *OrderBroker) GetBalance(ctx context.Context, asset string) (float64, error) {
	path := fmt.Sprintf("/api/v5/account/balance?ccy=%s", asset)

	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			Details []struct {
				Ccy       string `json:"ccy"`
				AvailBal  string `json:"availBal"`
			} `json:"details"`
		} `json:"data"`
	}

	if err := b.get(ctx, path, &resp); err != nil {
		return 0, fmt.Errorf("OKX get balance: %w", err)
	}
	if resp.Code != "0" {
		return 0, fmt.Errorf("OKX balance API error %s: %s", resp.Code, resp.Msg)
	}
	if len(resp.Data) == 0 {
		return 0, fmt.Errorf("OKX balance: empty response for %s", asset)
	}

	for _, d := range resp.Data[0].Details {
		if d.Ccy == asset {
			bal, err := strconv.ParseFloat(d.AvailBal, 64)
			if err != nil {
				return 0, fmt.Errorf("parse balance for %s: %w", asset, err)
			}
			return bal, nil
		}
	}
	return 0, fmt.Errorf("asset %s not found in OKX account", asset)
}

// fetchCtVal lazily fetches and caches the contract value (ctVal) for a SWAP instrument.
// ctVal is the number of base-asset units per contract (e.g. 0.01 BTC for BTC-USDT-SWAP).
func (b *OrderBroker) fetchCtVal(ctx context.Context, instID string) (float64, error) {
	if v, ok := b.ctValCache.Load(instID); ok {
		f, fOk := v.(float64)
		if !fOk {
			b.ctValCache.Delete(instID)
			return 0, fmt.Errorf("corrupted ctVal cache for %s", instID)
		}
		return f, nil
	}

	path := fmt.Sprintf("/api/v5/public/instruments?instType=SWAP&instId=%s", instID)

	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			CtVal string `json:"ctVal"`
		} `json:"data"`
	}

	// Public endpoint — use unauthenticated GET
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, okxAPIBase+path, nil)
	if err != nil {
		return 0, err
	}
	httpResp, err := b.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("OKX instruments request: %w", err)
	}
	defer httpResp.Body.Close()
	body, readErr := io.ReadAll(httpResp.Body)
	if readErr != nil {
		return 0, fmt.Errorf("OKX instruments read body: %w", readErr)
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("OKX instruments decode: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return 0, fmt.Errorf("OKX instruments error %s: %s", resp.Code, resp.Msg)
	}

	ctVal, err := strconv.ParseFloat(resp.Data[0].CtVal, 64)
	if err != nil {
		return 0, fmt.Errorf("parse ctVal: %w", err)
	}
	if ctVal <= 0 {
		return 0, fmt.Errorf("invalid ctVal %.8f for %s: must be positive", ctVal, instID)
	}

	b.ctValCache.Store(instID, ctVal)
	b.log.Info("fetched ctVal for SWAP instrument",
		zap.String("inst_id", instID), zap.Float64("ct_val", ctVal))
	return ctVal, nil
}

// toInstID converts a Binance-style symbol to the OKX instrument ID.
//
//	SWAP:  BTCUSDT → BTC-USDT-SWAP
//	Spot:  BTCUSDT → BTC-USDT
func (b *OrderBroker) toInstID(symbol string) string {
	if b.marketType == "swap" {
		return toOKXSWAPSymbol(symbol)
	}
	return toOKXSymbol(symbol)
}

// toOKXSWAPSymbol converts BTCUSDT → BTC-USDT-SWAP.
func toOKXSWAPSymbol(symbol string) string {
	if strings.HasSuffix(symbol, "USDT") {
		return symbol[:len(symbol)-4] + "-USDT-SWAP"
	}
	if strings.HasSuffix(symbol, "BTC") {
		return symbol[:len(symbol)-3] + "-BTC-SWAP"
	}
	return symbol + "-SWAP"
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

func (b *OrderBroker) post(ctx context.Context, path string, reqBody, respBody any) error {
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	sig := b.sign(ts, "POST", path, string(bodyBytes))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, okxAPIBase+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	b.setAuthHeaders(req, ts, sig)
	req.Header.Set("Content-Type", "application/json")

	return b.doRequest(req, respBody)
}

func (b *OrderBroker) get(ctx context.Context, path string, respBody any) error {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	sig := b.sign(ts, "GET", path, "")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, okxAPIBase+path, nil)
	if err != nil {
		return err
	}
	b.setAuthHeaders(req, ts, sig)

	return b.doRequest(req, respBody)
}

func (b *OrderBroker) setAuthHeaders(req *http.Request, ts, sig string) {
	req.Header.Set("OK-ACCESS-KEY", b.apiKey)
	req.Header.Set("OK-ACCESS-SIGN", sig)
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-PASSPHRASE", b.passphrase)
	if b.demo {
		req.Header.Set("x-simulated-trading", "1")
	}
}

func (b *OrderBroker) doRequest(req *http.Request, respBody any) error {
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return fmt.Errorf("OKX read response body: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("OKX HTTP %d: %s", resp.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, respBody); err != nil {
		return fmt.Errorf("OKX decode response: %w (body: %s)", err, string(body))
	}
	return nil
}

// sign computes the HMAC-SHA256 signature required by OKX private endpoints.
// prehash = timestamp + method + requestPath + body
func (b *OrderBroker) sign(ts, method, path, body string) string {
	prehash := ts + method + path + body
	mac := hmac.New(sha256.New, []byte(b.apiSecret))
	mac.Write([]byte(prehash))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// PlaceLimitOrder submits a limit order to OKX (SWAP or Spot).
// positionSide: "long", "short", or "" (net).
// Returns the exchange order ID.
func (b *OrderBroker) PlaceLimitOrder(ctx context.Context, symbol string, side exchange.OrderSide, positionSide string, qty, price float64, clientOrderID string) (string, error) {
	instID := b.toInstID(symbol)

	okxSide := "buy"
	if side == exchange.OrderSideSell {
		okxSide = "sell"
	}

	var sz string
	if b.marketType == "swap" {
		ctVal, err := b.fetchCtVal(ctx, instID)
		if err != nil {
			return "", fmt.Errorf("fetch ctVal: %w", err)
		}
		contracts := math.Floor(qty / ctVal)
		if contracts < 1 {
			return "", fmt.Errorf("qty %.8f too small for 1 contract (ctVal=%.8f)", qty, ctVal)
		}
		sz = strconv.FormatInt(int64(contracts), 10)
	} else {
		sz = fmt.Sprintf("%.8f", qty)
	}

	tdMode := "cash"
	if b.marketType == "swap" {
		tdMode = "cross"
	}

	type orderReq struct {
		InstID  string `json:"instId"`
		TdMode  string `json:"tdMode"`
		Side    string `json:"side"`
		PosSide string `json:"posSide,omitempty"`
		OrdType string `json:"ordType"`
		Sz      string `json:"sz"`
		Px      string `json:"px"`
		ClOrdId string `json:"clOrdId,omitempty"`
	}

	req := orderReq{
		InstID:  instID,
		TdMode:  tdMode,
		Side:    okxSide,
		OrdType: "limit",
		Sz:      sz,
		Px:      fmt.Sprintf("%.8f", price),
		ClOrdId: clientOrderID,
	}
	if positionSide != "" {
		req.PosSide = strings.ToLower(positionSide)
	}

	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			OrdID string `json:"ordId"`
			SCode string `json:"sCode"`
			SMsg  string `json:"sMsg"`
		} `json:"data"`
	}

	if err := b.post(ctx, "/api/v5/trade/order", req, &resp); err != nil {
		return "", fmt.Errorf("OKX limit order: %w", err)
	}
	if resp.Code != "0" {
		return "", fmt.Errorf("OKX limit order API error %s: %s", resp.Code, resp.Msg)
	}
	if len(resp.Data) == 0 || resp.Data[0].SCode != "0" {
		sMsg := ""
		if len(resp.Data) > 0 {
			sMsg = resp.Data[0].SMsg
		}
		return "", fmt.Errorf("OKX limit order rejected: %s", sMsg)
	}

	ordID := resp.Data[0].OrdID
	b.log.Info("OKX limit order placed",
		zap.String("ord_id", ordID),
		zap.String("inst_id", instID),
		zap.String("side", okxSide),
		zap.Float64("price", price),
	)
	return ordID, nil
}

// PlaceReduceOnlyLimitOrder places a GTC limit order with reduceOnly=true on OKX.
func (b *OrderBroker) PlaceReduceOnlyLimitOrder(ctx context.Context, symbol string, side exchange.OrderSide, positionSide string, qty, price float64, clientOrderID string) (string, error) {
	instID := b.toInstID(symbol)

	okxSide := "buy"
	if side == exchange.OrderSideSell {
		okxSide = "sell"
	}

	var sz string
	if b.marketType == "swap" {
		ctVal, err := b.fetchCtVal(ctx, instID)
		if err != nil {
			return "", fmt.Errorf("fetch ctVal: %w", err)
		}
		contracts := math.Floor(qty / ctVal)
		if contracts < 1 {
			return "", fmt.Errorf("qty %.8f too small for 1 contract (ctVal=%.8f)", qty, ctVal)
		}
		sz = strconv.FormatInt(int64(contracts), 10)
	} else {
		sz = fmt.Sprintf("%.8f", qty)
	}

	tdMode := "cash"
	if b.marketType == "swap" {
		tdMode = "cross"
	}

	type orderReq struct {
		InstID     string `json:"instId"`
		TdMode     string `json:"tdMode"`
		Side       string `json:"side"`
		PosSide    string `json:"posSide,omitempty"`
		OrdType    string `json:"ordType"`
		Sz         string `json:"sz"`
		Px         string `json:"px"`
		ClOrdId    string `json:"clOrdId,omitempty"`
		ReduceOnly string `json:"reduceOnly"`
	}

	req := orderReq{
		InstID:     instID,
		TdMode:     tdMode,
		Side:       okxSide,
		OrdType:    "limit",
		Sz:         sz,
		Px:         fmt.Sprintf("%.8f", price),
		ClOrdId:    clientOrderID,
		ReduceOnly: "true",
	}
	if positionSide != "" {
		req.PosSide = strings.ToLower(positionSide)
	}

	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			OrdID string `json:"ordId"`
			SCode string `json:"sCode"`
			SMsg  string `json:"sMsg"`
		} `json:"data"`
	}

	if err := b.post(ctx, "/api/v5/trade/order", req, &resp); err != nil {
		return "", fmt.Errorf("OKX reduce-only limit order: %w", err)
	}
	if resp.Code != "0" {
		return "", fmt.Errorf("OKX reduce-only limit order API error %s: %s", resp.Code, resp.Msg)
	}
	if len(resp.Data) == 0 || resp.Data[0].SCode != "0" {
		sMsg := ""
		if len(resp.Data) > 0 {
			sMsg = resp.Data[0].SMsg
		}
		return "", fmt.Errorf("OKX reduce-only limit order rejected: %s", sMsg)
	}

	ordID := resp.Data[0].OrdID
	b.log.Info("OKX reduce-only limit order placed",
		zap.String("ord_id", ordID),
		zap.String("inst_id", instID),
		zap.String("side", okxSide),
		zap.Float64("price", price),
		zap.Float64("qty", qty),
	)
	return ordID, nil
}

// PlaceStopMarketOrder places a stop-loss conditional order on OKX.
// When stopPrice is triggered, executes at market price (slOrdPx = "-1").
func (b *OrderBroker) PlaceStopMarketOrder(ctx context.Context, symbol string, side exchange.OrderSide, positionSide string, qty, stopPrice float64, clientOrderID string) (string, error) {
	instID := b.toInstID(symbol)

	okxSide := "buy"
	if side == exchange.OrderSideSell {
		okxSide = "sell"
	}

	var sz string
	if b.marketType == "swap" {
		ctVal, err := b.fetchCtVal(ctx, instID)
		if err != nil {
			return "", fmt.Errorf("fetch ctVal: %w", err)
		}
		contracts := math.Floor(qty / ctVal)
		if contracts < 1 {
			return "", fmt.Errorf("qty %.8f too small for 1 contract", qty)
		}
		sz = strconv.FormatInt(int64(contracts), 10)
	} else {
		sz = fmt.Sprintf("%.8f", qty)
	}

	tdMode := "cash"
	if b.marketType == "swap" {
		tdMode = "cross"
	}

	type algoReq struct {
		InstID          string `json:"instId"`
		TdMode          string `json:"tdMode"`
		Side            string `json:"side"`
		PosSide         string `json:"posSide,omitempty"`
		OrdType         string `json:"ordType"`
		Sz              string `json:"sz"`
		SlTriggerPx     string `json:"slTriggerPx"`
		SlOrdPx         string `json:"slOrdPx"`
		SlTriggerPxType string `json:"slTriggerPxType"`
		AlgoClOrdId     string `json:"algoClOrdId,omitempty"`
	}

	req := algoReq{
		InstID:          instID,
		TdMode:          tdMode,
		Side:            okxSide,
		OrdType:         "conditional",
		Sz:              sz,
		SlTriggerPx:     fmt.Sprintf("%.8f", stopPrice),
		SlOrdPx:         "-1", // market execution on trigger
		SlTriggerPxType: "last",
		AlgoClOrdId:     clientOrderID,
	}
	if positionSide != "" {
		req.PosSide = strings.ToLower(positionSide)
	}

	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			AlgoID string `json:"algoId"`
			SCode  string `json:"sCode"`
			SMsg   string `json:"sMsg"`
		} `json:"data"`
	}

	if err := b.post(ctx, "/api/v5/trade/order-algo", req, &resp); err != nil {
		return "", fmt.Errorf("OKX stop order: %w", err)
	}
	if resp.Code != "0" {
		return "", fmt.Errorf("OKX stop order API error %s: %s", resp.Code, resp.Msg)
	}
	if len(resp.Data) == 0 || resp.Data[0].SCode != "0" {
		sMsg := ""
		if len(resp.Data) > 0 {
			sMsg = resp.Data[0].SMsg
		}
		return "", fmt.Errorf("OKX stop order rejected: %s", sMsg)
	}

	algoID := resp.Data[0].AlgoID
	b.log.Info("OKX stop-market order placed",
		zap.String("algo_id", algoID),
		zap.String("inst_id", instID),
		zap.Float64("stop_price", stopPrice),
	)
	return algoID, nil
}

// PlaceTakeProfitMarketOrder places a take-profit conditional order on OKX.
// When triggerPrice is reached, executes at market price.
func (b *OrderBroker) PlaceTakeProfitMarketOrder(ctx context.Context, symbol string, side exchange.OrderSide, positionSide string, qty, triggerPrice float64, clientOrderID string) (string, error) {
	instID := b.toInstID(symbol)

	okxSide := "buy"
	if side == exchange.OrderSideSell {
		okxSide = "sell"
	}

	var sz string
	if b.marketType == "swap" {
		ctVal, err := b.fetchCtVal(ctx, instID)
		if err != nil {
			return "", fmt.Errorf("fetch ctVal: %w", err)
		}
		contracts := math.Floor(qty / ctVal)
		if contracts < 1 {
			return "", fmt.Errorf("qty %.8f too small for 1 contract", qty)
		}
		sz = strconv.FormatInt(int64(contracts), 10)
	} else {
		sz = fmt.Sprintf("%.8f", qty)
	}

	tdMode := "cash"
	if b.marketType == "swap" {
		tdMode = "cross"
	}

	type algoReq struct {
		InstID          string `json:"instId"`
		TdMode          string `json:"tdMode"`
		Side            string `json:"side"`
		PosSide         string `json:"posSide,omitempty"`
		OrdType         string `json:"ordType"`
		Sz              string `json:"sz"`
		TpTriggerPx     string `json:"tpTriggerPx"`
		TpOrdPx         string `json:"tpOrdPx"`
		TpTriggerPxType string `json:"tpTriggerPxType"`
		AlgoClOrdId     string `json:"algoClOrdId,omitempty"`
	}

	req := algoReq{
		InstID:          instID,
		TdMode:          tdMode,
		Side:            okxSide,
		OrdType:         "conditional",
		Sz:              sz,
		TpTriggerPx:     fmt.Sprintf("%.8f", triggerPrice),
		TpOrdPx:         "-1", // market execution on trigger
		TpTriggerPxType: "last",
		AlgoClOrdId:     clientOrderID,
	}
	if positionSide != "" {
		req.PosSide = strings.ToLower(positionSide)
	}

	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			AlgoID string `json:"algoId"`
			SCode  string `json:"sCode"`
			SMsg   string `json:"sMsg"`
		} `json:"data"`
	}

	if err := b.post(ctx, "/api/v5/trade/order-algo", req, &resp); err != nil {
		return "", fmt.Errorf("OKX take-profit order: %w", err)
	}
	if resp.Code != "0" {
		return "", fmt.Errorf("OKX take-profit order API error %s: %s", resp.Code, resp.Msg)
	}
	if len(resp.Data) == 0 || resp.Data[0].SCode != "0" {
		sMsg := ""
		if len(resp.Data) > 0 {
			sMsg = resp.Data[0].SMsg
		}
		return "", fmt.Errorf("OKX take-profit order rejected: %s", sMsg)
	}

	algoID := resp.Data[0].AlgoID
	b.log.Info("OKX take-profit order placed",
		zap.String("algo_id", algoID),
		zap.String("inst_id", instID),
		zap.Float64("trigger_price", triggerPrice),
	)
	return algoID, nil
}

// SetLeverage configures the leverage for a SWAP instrument on OKX.
// Uses cross-margin mode. Spot mode returns an error.
func (b *OrderBroker) SetLeverage(ctx context.Context, symbol string, leverage int) error {
	if b.marketType != "swap" {
		return fmt.Errorf("SetLeverage only supported for SWAP market type, got %q", b.marketType)
	}

	instID := b.toInstID(symbol)

	type leverageReq struct {
		InstID  string `json:"instId"`
		Lever   string `json:"lever"`
		MgnMode string `json:"mgnMode"`
	}

	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}

	if err := b.post(ctx, "/api/v5/account/set-leverage", leverageReq{
		InstID:  instID,
		Lever:   strconv.Itoa(leverage),
		MgnMode: "cross",
	}, &resp); err != nil {
		return fmt.Errorf("OKX set leverage: %w", err)
	}
	if resp.Code != "0" {
		return fmt.Errorf("OKX set leverage API error %s: %s", resp.Code, resp.Msg)
	}

	b.log.Info("OKX leverage configured",
		zap.String("inst_id", instID),
		zap.Int("leverage", leverage),
	)
	return nil
}

// CancelAlgoOrder cancels an OKX algo (conditional) order by algoID.
// Use for stop-loss / take-profit orders placed via PlaceStopMarketOrder / PlaceTakeProfitMarketOrder.
func (b *OrderBroker) CancelAlgoOrder(ctx context.Context, symbol, algoID string) error {
	instID := b.toInstID(symbol)

	type cancelReq struct {
		AlgoID string `json:"algoId"`
		InstID string `json:"instId"`
	}

	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}

	if err := b.post(ctx, "/api/v5/trade/cancel-algos", []cancelReq{{AlgoID: algoID, InstID: instID}}, &resp); err != nil {
		return fmt.Errorf("OKX cancel algo order: %w", err)
	}
	if resp.Code != "0" {
		return fmt.Errorf("OKX cancel algo order API error %s: %s", resp.Code, resp.Msg)
	}
	return nil
}

// GetMarginRatios implements exchange.MarginQuerier.
// Queries GET /api/v5/account/positions and returns maintenance margin ratios
// for all open positions. Positions with zero size are skipped.
func (b *OrderBroker) GetMarginRatios(ctx context.Context) ([]exchange.PositionMarginInfo, error) {
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			InstID   string `json:"instId"`
			PosSide  string `json:"posSide"`  // "long", "short", "net"
			Pos      string `json:"pos"`      // position size in contracts (non-zero when open)
			MgnRatio string `json:"mgnRatio"` // maintenance margin ratio; empty when no position
		} `json:"data"`
	}

	if err := b.get(ctx, "/api/v5/account/positions", &resp); err != nil {
		return nil, fmt.Errorf("get OKX positions: %w", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("OKX positions API error %s: %s", resp.Code, resp.Msg)
	}

	var result []exchange.PositionMarginInfo
	for _, d := range resp.Data {
		pos, errPos := strconv.ParseFloat(d.Pos, 64)
		if errPos != nil {
			b.log.Warn("GetMarginRatios: unparseable pos, skipping position",
				zap.String("inst_id", d.InstID), zap.String("raw", d.Pos), zap.Error(errPos))
			continue
		}
		if pos == 0 {
			continue // no open position
		}
		ratio, errRatio := strconv.ParseFloat(d.MgnRatio, 64)
		if errRatio != nil {
			b.log.Warn("GetMarginRatios: unparseable mgnRatio, skipping position — unknown margin state",
				zap.String("inst_id", d.InstID), zap.String("raw", d.MgnRatio), zap.Error(errRatio))
			continue
		}

		posSide := strings.ToUpper(d.PosSide) // "LONG", "SHORT", "NET"
		if posSide == "NET" {
			posSide = ""
		}

		// Strip the "-SWAP" / "-USDT-SWAP" suffix to normalise to "BTCUSDT" style
		symbol := strings.ReplaceAll(d.InstID, "-", "")
		symbol = strings.TrimSuffix(symbol, "SWAP")

		result = append(result, exchange.PositionMarginInfo{
			Symbol:       symbol,
			PositionSide: posSide,
			MarginRatio:  ratio,
			Size:         math.Abs(pos),
		})
	}
	return result, nil
}

// GetOrderStatus implements exchange.OrderStatusChecker.
// Queries GET /api/v5/trade/order once and returns the current state.
// Returned status values: "filled", "partially_filled", "live", "canceled".
func (b *OrderBroker) GetOrderStatus(ctx context.Context, symbol, orderID string) (string, exchange.OrderFill, error) {
	instID := b.toInstID(symbol)
	path := fmt.Sprintf("/api/v5/trade/order?instId=%s&ordId=%s", instID, orderID)

	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			OrdID  string `json:"ordId"`
			State  string `json:"state"`
			FillSz string `json:"fillSz"`
			AvgPx  string `json:"avgPx"`
			Fee    string `json:"fee"`
		} `json:"data"`
	}

	if err := b.get(ctx, path, &resp); err != nil {
		return "", exchange.OrderFill{}, fmt.Errorf("OKX get order status: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return "", exchange.OrderFill{}, fmt.Errorf("OKX order status API error %s: %s", resp.Code, resp.Msg)
	}

	d := resp.Data[0]

	var fillSz, avgPx, fee float64
	if errSz := parseFloatWarn(d.FillSz, &fillSz); errSz != nil {
		b.log.Warn("GetOrderStatus: unparseable fillSz",
			zap.String("order_id", orderID), zap.String("raw", d.FillSz), zap.Error(errSz))
	}
	if errPx := parseFloatWarn(d.AvgPx, &avgPx); errPx != nil {
		b.log.Warn("GetOrderStatus: unparseable avgPx",
			zap.String("order_id", orderID), zap.String("raw", d.AvgPx), zap.Error(errPx))
	}
	if errFee := parseFloatWarn(d.Fee, &fee); errFee != nil {
		b.log.Warn("GetOrderStatus: unparseable fee",
			zap.String("order_id", orderID), zap.String("raw", d.Fee), zap.Error(errFee))
	}
	if fee < 0 {
		fee = -fee // OKX returns fees as negative
	}

	// For SWAP orders, OKX returns fillSz in contracts. Convert to base-asset units.
	if b.marketType == "swap" && fillSz > 0 {
		instID := b.toInstID(symbol)
		ctVal, ctErr := b.fetchCtVal(ctx, instID)
		if ctErr != nil {
			return "", exchange.OrderFill{}, fmt.Errorf("GetOrderStatus: fetch ctVal for qty conversion: %w", ctErr)
		}
		fillSz *= ctVal
	}

	fill := exchange.OrderFill{
		ExchangeID: orderID,
		FilledQty:  fillSz,
		AvgPrice:   avgPx,
		Fee:        fee,
		Status:     d.State,
	}
	return d.State, fill, nil
}

// parseFloatWarn parses s into *dst. Returns nil (and leaves *dst at 0) for empty strings.
// Returns an error for non-empty unparseable strings.
func parseFloatWarn(s string, dst *float64) error {
	if s == "" {
		*dst = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*dst = 0
		return err
	}
	*dst = v
	return nil
}

// Compile-time interface checks.
var _ exchange.OrderClient = (*OrderBroker)(nil)
var _ exchange.MarginQuerier = (*OrderBroker)(nil)
var _ exchange.OrderStatusChecker = (*OrderBroker)(nil)
