// Package binance_futures provides an order broker for Binance USDM Futures (testnet and live).
package binance_futures

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"sync"
	"time"

	goBinance "github.com/adshao/go-binance/v2/futures"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/exchange"
)

// OrderBroker submits USDM Futures orders to Binance (testnet or live).
// It implements exchange.OrderClient.
//
// Position mode: always uses Hedge Mode (LONG/SHORT position sides).
// If your account is in One-Way mode, call SetPositionModeHedge before trading.
type OrderBroker struct {
	client *goBinance.Client
	log    *zap.Logger

	// demo/testnet pin this broker's network mode so its user-data WS stream
	// binds to the right endpoint via exchange.ServeBinanceFuturesWS, even when a
	// live broker and a demo broker run in the same process. See binance_auth.go.
	demo    bool
	testnet bool

	// Per-symbol LOT_SIZE/PRICE_FILTER precision, loaded lazily from
	// exchangeInfo on first order. Guards against -1111 "precision over maximum"
	// rejections for symbols other than the ones the formatting was hardcoded to.
	mu            sync.RWMutex
	filters       map[string]symbolFilter
	filtersLoaded bool
}

// symbolFilterFor returns the cached precision filter for symbol, loading
// exchangeInfo once on first use. On load failure it returns a zero filter,
// whose qtyStr/priceStr fall back to safe default precision.
func (b *OrderBroker) symbolFilterFor(ctx context.Context, symbol string) symbolFilter {
	b.mu.RLock()
	loaded := b.filtersLoaded
	f := b.filters[symbol]
	b.mu.RUnlock()
	if loaded {
		return f
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.filtersLoaded {
		b.loadFiltersLocked(ctx)
	}
	return b.filters[symbol]
}

// loadFiltersLocked fetches exchangeInfo and caches every symbol's step/tick
// precision. Caller must hold b.mu. On error it logs and leaves filtersLoaded
// false so the next order retries the fetch.
func (b *OrderBroker) loadFiltersLocked(ctx context.Context) {
	info, err := b.client.NewExchangeInfoService().Do(ctx)
	if err != nil {
		b.log.Warn("exchangeInfo fetch failed — order precision uses fallback formatting", zap.Error(err))
		return
	}
	m := make(map[string]symbolFilter, len(info.Symbols))
	for _, s := range info.Symbols {
		var f symbolFilter
		if lf := s.LotSizeFilter(); lf != nil {
			f.stepSize, f.qtyDecimals = parseStep(lf.StepSize)
		}
		if pf := s.PriceFilter(); pf != nil {
			f.tickSize, f.priceDecimals = parseStep(pf.TickSize)
		}
		m[s.Symbol] = f
	}
	b.filters = m
	b.filtersLoaded = true
	b.log.Info("loaded Binance futures symbol filters", zap.Int("symbols", len(m)))
}

// NewOrderBroker creates a Binance Futures OrderBroker.
//
// Safety gates:
//   - testnet=true  → hits testnet.binancefuture.com (safe, no real money)
//   - testnet=false → requires env QUANTIX_LIVE_CONFIRM=true (real money)
func NewOrderBroker(apiKey, apiSecret string, testnet bool, log *zap.Logger) (*OrderBroker, error) {
	return NewOrderBrokerWithConfig(apiKey, apiSecret, testnet, config.BinanceConfig{Testnet: testnet}, log)
}

// NewOrderBrokerWithConfig creates a Binance Futures OrderBroker with optional private-key auth.
// Network mode: cfg.Demo → demo-api.binance.com; cfg.Testnet → testnet.binancefuture.com; else live.
func NewOrderBrokerWithConfig(apiKey, apiSecret string, testnet bool, cfg config.BinanceConfig, log *zap.Logger) (*OrderBroker, error) {
	if !cfg.Demo {
		cfg.Testnet = testnet
	}
	exchange.ApplyBinanceNetworkMode(cfg)

	if cfg.Demo {
		log.Info("Binance Futures order broker: DEMO mode",
			zap.String("endpoint", "demo-api.binance.com"))
	} else if cfg.Testnet {
		log.Info("Binance Futures order broker: TESTNET mode")
	} else {
		if os.Getenv("QUANTIX_LIVE_CONFIRM") != "true" {
			return nil, fmt.Errorf(
				"Binance Futures live mode requires QUANTIX_LIVE_CONFIRM=true env var; " +
					"set it explicitly to confirm real-money trading")
		}
		log.Warn("Binance Futures order broker: LIVE mode — REAL MONEY AT RISK")
	}

	client := goBinance.NewClient(apiKey, apiSecret)

	// Pin the REST base URL per-instance instead of relying on the shared global
	// network flags (ApplyBinanceNetworkMode above sets them process-wide). This
	// lets a live engine and a demo engine coexist: each client's REST calls go to
	// the right host regardless of what another engine set the globals to. WS
	// streams are handled separately via exchange.ServeBinanceFuturesWS.
	client.BaseURL = exchange.BinanceFuturesRESTBaseURL(cfg.Demo, cfg.Testnet)

	// Apply private-key auth if configured
	if err := exchange.ConfigureBinanceFuturesAuth(client, cfg); err != nil {
		return nil, fmt.Errorf("binance futures auth config: %w", err)
	}

	// Verify connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.NewGetAccountService().Do(ctx); err != nil {
		return nil, fmt.Errorf("Binance Futures API connectivity check failed: %w", err)
	}

	log.Info("Binance Futures order broker ready", zap.String("key_type", client.KeyType))
	return &OrderBroker{client: client, log: log, demo: cfg.Demo, testnet: cfg.Testnet}, nil
}

// PlaceMarketOrder submits a market order. qty is in base-asset units (e.g. BTC).
// positionSide: "LONG", "SHORT", or "" (one-way / BOTH mode).
func (b *OrderBroker) PlaceMarketOrder(ctx context.Context, symbol string, side exchange.OrderSide, positionSide string, qty float64, clientOrderID string) (exchange.OrderFill, error) {
	binSide := toBinanceSide(side)
	f := b.symbolFilterFor(ctx, symbol)

	svc := b.client.NewCreateOrderService().
		Symbol(symbol).
		Side(binSide).
		Type(goBinance.OrderTypeMarket).
		Quantity(f.qtyStr(qty))

	if ps := toFuturesPositionSide(positionSide); ps != "" {
		svc = svc.PositionSide(ps)
	}
	if clientOrderID != "" {
		svc = svc.NewClientOrderID(clientOrderID)
	}

	result, err := svc.Do(ctx)
	if err != nil {
		return exchange.OrderFill{}, fmt.Errorf("binance futures market order: %w", err)
	}

	fill, parseErr := b.parseFill(result)
	if parseErr != nil {
		return exchange.OrderFill{}, fmt.Errorf("binance futures market order parse fill: %w", parseErr)
	}
	b.log.Info("Binance Futures market order placed",
		zap.String("exchange_id", fill.ExchangeID),
		zap.String("symbol", symbol),
		zap.String("side", string(side)),
		zap.Float64("qty", fill.FilledQty),
		zap.Float64("avg_price", fill.AvgPrice),
	)
	return fill, nil
}

// PlaceLimitOrder submits a limit order with GTC time-in-force.
// positionSide: "LONG", "SHORT", or "" (BOTH for one-way mode).
func (b *OrderBroker) PlaceLimitOrder(ctx context.Context, symbol string, side exchange.OrderSide, positionSide string, qty, price float64, clientOrderID string) (string, error) {
	f := b.symbolFilterFor(ctx, symbol)
	qtyStr := f.qtyStr(qty)
	priceStr := f.priceStr(price)

	svc := b.client.NewCreateOrderService().
		Symbol(symbol).
		Side(toBinanceSide(side)).
		Type(goBinance.OrderTypeLimit).
		TimeInForce(goBinance.TimeInForceTypeGTC).
		Quantity(qtyStr).
		Price(priceStr)

	if ps := toFuturesPositionSide(positionSide); ps != "" {
		svc = svc.PositionSide(ps)
	}
	if clientOrderID != "" {
		svc = svc.NewClientOrderID(clientOrderID)
	}

	b.log.Info("Binance Futures limit order attempt",
		zap.String("symbol", symbol), zap.String("side", string(side)),
		zap.String("position_side", positionSide),
		zap.String("qty", qtyStr), zap.String("price", priceStr))

	result, err := svc.Do(ctx)
	if err != nil {
		return "", fmt.Errorf("binance futures limit order: %w", err)
	}

	ordID := strconv.FormatInt(result.OrderID, 10)
	b.log.Info("Binance Futures limit order placed",
		zap.String("order_id", ordID),
		zap.String("symbol", symbol),
		zap.Float64("price", price),
	)
	return ordID, nil
}

// PlaceReduceOnlyLimitOrder places a GTC limit order with ReduceOnly=true.
// Used for staged take-profit orders that close an existing position.
func (b *OrderBroker) PlaceReduceOnlyLimitOrder(ctx context.Context, symbol string, side exchange.OrderSide, positionSide string, qty, price float64, clientOrderID string) (string, error) {
	f := b.symbolFilterFor(ctx, symbol)
	svc := b.client.NewCreateOrderService().
		Symbol(symbol).
		Side(toBinanceSide(side)).
		Type(goBinance.OrderTypeLimit).
		TimeInForce(goBinance.TimeInForceTypeGTC).
		Quantity(f.qtyStr(qty)).
		Price(f.priceStr(price))

	// Hedge mode: positionSide implies the side to reduce; reduceOnly conflicts with it.
	// One-way mode: no positionSide, use reduceOnly to prevent accidental position opening.
	if ps := toFuturesPositionSide(positionSide); ps != "" {
		svc = svc.PositionSide(ps)
	} else {
		svc = svc.ReduceOnly(true)
	}
	if clientOrderID != "" {
		svc = svc.NewClientOrderID(clientOrderID)
	}

	result, err := svc.Do(ctx)
	if err != nil {
		return "", fmt.Errorf("binance futures reduce-only limit order: %w", err)
	}

	ordID := strconv.FormatInt(result.OrderID, 10)
	b.log.Info("Binance Futures reduce-only limit order placed",
		zap.String("order_id", ordID),
		zap.String("symbol", symbol),
		zap.Float64("price", price),
		zap.Float64("qty", qty),
	)
	return ordID, nil
}

// PlaceStopMarketOrder places a STOP_MARKET algo order that fires when stopPrice is reached.
// Uses the Algo Order API (/fapi/v1/algoOrder) which Binance requires for conditional orders.
func (b *OrderBroker) PlaceStopMarketOrder(ctx context.Context, symbol string, side exchange.OrderSide, positionSide string, qty, stopPrice float64, clientOrderID string) (string, error) {
	f := b.symbolFilterFor(ctx, symbol)
	svc := b.client.NewCreateAlgoOrderService().
		Symbol(symbol).
		Side(toBinanceSide(side)).
		Type(goBinance.AlgoOrderTypeStopMarket).
		TriggerPrice(f.priceStr(stopPrice)).
		Quantity(f.qtyStr(qty))

	if ps := toFuturesPositionSide(positionSide); ps != "" {
		svc = svc.PositionSide(ps)
	} else {
		svc = svc.ReduceOnly(true)
	}
	if clientOrderID != "" {
		svc = svc.ClientAlgoId(clientOrderID)
	}

	result, err := svc.Do(ctx)
	if err != nil {
		return "", fmt.Errorf("binance futures stop-market order: %w", err)
	}

	ordID := strconv.FormatInt(result.AlgoId, 10)
	b.log.Info("Binance Futures stop-market algo order placed",
		zap.String("algo_id", ordID),
		zap.String("symbol", symbol),
		zap.Float64("stop_price", stopPrice),
	)
	return ordID, nil
}

// PlaceTakeProfitMarketOrder places a TAKE_PROFIT_MARKET algo order that fires when triggerPrice is reached.
// Uses the Algo Order API (/fapi/v1/algoOrder).
func (b *OrderBroker) PlaceTakeProfitMarketOrder(ctx context.Context, symbol string, side exchange.OrderSide, positionSide string, qty, triggerPrice float64, clientOrderID string) (string, error) {
	f := b.symbolFilterFor(ctx, symbol)
	svc := b.client.NewCreateAlgoOrderService().
		Symbol(symbol).
		Side(toBinanceSide(side)).
		Type(goBinance.AlgoOrderTypeTakeProfitMarket).
		TriggerPrice(f.priceStr(triggerPrice)).
		Quantity(f.qtyStr(qty))

	if ps := toFuturesPositionSide(positionSide); ps != "" {
		svc = svc.PositionSide(ps)
	} else {
		svc = svc.ReduceOnly(true)
	}
	if clientOrderID != "" {
		svc = svc.ClientAlgoId(clientOrderID)
	}

	result, err := svc.Do(ctx)
	if err != nil {
		return "", fmt.Errorf("binance futures take-profit-market order: %w", err)
	}

	ordID := strconv.FormatInt(result.AlgoId, 10)
	b.log.Info("Binance Futures take-profit algo order placed",
		zap.String("algo_id", ordID),
		zap.String("symbol", symbol),
		zap.Float64("trigger_price", triggerPrice),
	)
	return ordID, nil
}

// SetLeverage configures the leverage for a USDM futures symbol.
func (b *OrderBroker) SetLeverage(ctx context.Context, symbol string, leverage int) error {
	_, err := b.client.NewChangeLeverageService().
		Symbol(symbol).
		Leverage(leverage).
		Do(ctx)
	if err != nil {
		return fmt.Errorf("binance futures set leverage: %w", err)
	}
	b.log.Info("Binance Futures leverage configured",
		zap.String("symbol", symbol),
		zap.Int("leverage", leverage),
	)
	return nil
}

// CancelOrder cancels a live futures order by exchange order ID (numeric string).
// Tries normal order cancel first; on failure tries algo order cancel (SL/TP are algo orders).
func (b *OrderBroker) CancelOrder(ctx context.Context, symbol, exchangeID string) error {
	xID, err := strconv.ParseInt(exchangeID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid exchange order ID %q: %w", exchangeID, err)
	}
	// Try normal order cancel first.
	_, err = b.client.NewCancelOrderService().
		Symbol(symbol).
		OrderID(xID).
		Do(ctx)
	if err == nil {
		return nil
	}
	// If normal cancel fails, try algo order cancel (SL/TP placed via algo API).
	_, algoErr := b.client.NewCancelAlgoOrderService().AlgoID(xID).Do(ctx)
	if algoErr == nil {
		return nil
	}
	// Return original error (more informative).
	return fmt.Errorf("binance futures cancel order: %w (algo cancel also failed: %v)", err, algoErr)
}

// ListOpenOrders implements exchange.OpenOrdersLister.
// Returns all currently-resting orders on Binance USDM Futures for the given symbol
// (limit + algo). Used by live.Engine on restart to adopt orphan exchange orders.
func (b *OrderBroker) ListOpenOrders(ctx context.Context, symbol string) ([]exchange.OpenOrderInfo, error) {
	orders, err := b.client.NewListOpenOrdersService().Symbol(symbol).Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance futures list open orders: %w", err)
	}
	res := make([]exchange.OpenOrderInfo, 0, len(orders))
	for _, o := range orders {
		qty, _ := strconv.ParseFloat(o.OrigQuantity, 64)
		filled, _ := strconv.ParseFloat(o.ExecutedQuantity, 64)
		price, _ := strconv.ParseFloat(o.Price, 64)
		stopPx, _ := strconv.ParseFloat(o.StopPrice, 64)
		res = append(res, exchange.OpenOrderInfo{
			ExchangeID:    strconv.FormatInt(o.OrderID, 10),
			ClientOrderID: o.ClientOrderID,
			Symbol:        o.Symbol,
			Side:          string(o.Side),
			PositionSide:  string(o.PositionSide),
			Type:          string(o.Type),
			Status:        string(o.Status),
			Qty:           qty,
			FilledQty:     filled,
			Price:         price,
			StopPrice:     stopPx,
			ReduceOnly:    o.ReduceOnly,
		})
	}
	return res, nil
}

// CancelAllOpenOrders implements exchange.OpenOrdersCanceller.
// Cancels all open orders for the given symbol on Binance USDM Futures.
// Used by live.Engine on startup (clean-slate) to clear orphaned orders.
func (b *OrderBroker) CancelAllOpenOrders(ctx context.Context, symbol string) error {
	// Cancel normal orders (LIMIT, etc.)
	err := b.client.NewCancelAllOpenOrdersService().Symbol(symbol).Do(ctx)
	if err != nil {
		b.log.Warn("cancel normal open orders failed (may be none)", zap.String("symbol", symbol), zap.Error(err))
	}
	// Cancel algo orders (SL/TP stop-market, take-profit-market)
	algoErr := b.client.NewCancelAllAlgoOpenOrdersService().Symbol(symbol).Do(ctx)
	if algoErr != nil {
		b.log.Warn("cancel algo open orders failed (may be none)", zap.String("symbol", symbol), zap.Error(algoErr))
	}
	return nil // best-effort: either or both may have no orders to cancel
}

// GetBalance returns the available balance for the given asset (e.g. "USDT").
func (b *OrderBroker) GetBalance(ctx context.Context, asset string) (float64, error) {
	balances, err := b.client.NewGetBalanceService().Do(ctx)
	if err != nil {
		return 0, fmt.Errorf("binance futures get balance: %w", err)
	}
	for _, bal := range balances {
		if bal.Asset == asset {
			avail, err := strconv.ParseFloat(bal.AvailableBalance, 64)
			if err != nil {
				return 0, fmt.Errorf("parse available balance for %s: %w", asset, err)
			}
			return avail, nil
		}
	}
	return 0, fmt.Errorf("asset %s not found in Binance Futures account", asset)
}

// GetEquity returns the true account equity for the given asset.
// Equity = wallet balance + unrealized PnL (from exchange, not local calculation).
func (b *OrderBroker) GetEquity(ctx context.Context, asset string) (float64, error) {
	balances, err := b.client.NewGetBalanceService().Do(ctx)
	if err != nil {
		return 0, fmt.Errorf("binance futures get equity: %w", err)
	}
	for _, bal := range balances {
		if bal.Asset == asset {
			walletBal, err := strconv.ParseFloat(bal.Balance, 64)
			if err != nil {
				return 0, fmt.Errorf("parse balance for %s: %w", asset, err)
			}
			crossPnl, _ := strconv.ParseFloat(bal.CrossUnPnl, 64)
			return walletBal + crossPnl, nil
		}
	}
	return 0, fmt.Errorf("asset %s not found in Binance Futures account", asset)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func toBinanceSide(side exchange.OrderSide) goBinance.SideType {
	if side == exchange.OrderSideSell {
		return goBinance.SideTypeSell
	}
	return goBinance.SideTypeBuy
}

// toFuturesPositionSide converts "LONG"/"SHORT"/"" to the Binance futures type.
// Returns empty string for "" (one-way / BOTH mode).
func toFuturesPositionSide(positionSide string) goBinance.PositionSideType {
	switch positionSide {
	case "LONG":
		return goBinance.PositionSideTypeLong
	case "SHORT":
		return goBinance.PositionSideTypeShort
	default:
		return "" // let the exchange use one-way mode (BOTH)
	}
}

// parseFill extracts fill details from a Binance Futures CreateOrderResponse.
// Returns an error if critical fields (ExecutedQuantity, AvgPrice) cannot be parsed,
// preventing silent zero-fill events that would cause phantom untracked positions.
func (b *OrderBroker) parseFill(r *goBinance.CreateOrderResponse) (exchange.OrderFill, error) {
	// A successful CreateOrder response (svc.Do returned no error) means the order
	// was ACCEPTED by the exchange — it must NOT be reported as a failure. MARKET
	// orders frequently return empty ExecutedQuantity/AvgPrice here because the fill
	// is reported asynchronously via the user-data stream; the old code errored on
	// the empty AvgPrice, so a successful close looked "failed" → retries → the
	// engine kept thinking the position was open while the exchange had closed it
	// (orphans / desync). Parse leniently: empty/unparseable → 0; the real fill
	// price & qty arrive via the user-data stream (engine.onUserDataFill).
	qty, _ := strconv.ParseFloat(r.ExecutedQuantity, 64)
	avgPrice, _ := strconv.ParseFloat(r.AvgPrice, 64)
	// Best-effort avg price from cumulative quote when the immediate response only
	// has executedQty (avoids a 0 price on an order that did fill synchronously).
	if avgPrice == 0 && qty > 0 {
		if cq, err := strconv.ParseFloat(r.CumQuote, 64); err == nil && cq > 0 {
			avgPrice = cq / qty
		}
	}
	return exchange.OrderFill{
		ExchangeID: strconv.FormatInt(r.OrderID, 10),
		FilledQty:  qty,
		AvgPrice:   avgPrice,
		Status:     string(r.Status),
	}, nil
}

// GetMarginRatios implements exchange.MarginQuerier.
// Uses NewListPositionRiskService (GET /fapi/v2/positionRisk) and approximates
// the maintenance margin ratio as the fractional distance between mark price
// and liquidation price. Positions with zero size are skipped.
func (b *OrderBroker) GetMarginRatios(ctx context.Context) ([]exchange.PositionMarginInfo, error) {
	risks, err := b.client.NewGetPositionRiskService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance futures position risk: %w", err)
	}

	var result []exchange.PositionMarginInfo
	for _, r := range risks {
		posAmt, err := strconv.ParseFloat(r.PositionAmt, 64)
		if err != nil {
			b.log.Warn("failed to parse positionAmt", zap.String("symbol", r.Symbol), zap.String("raw", r.PositionAmt), zap.Error(err))
			continue
		}
		if posAmt == 0 {
			continue
		}
		markPrice, err := strconv.ParseFloat(r.MarkPrice, 64)
		if err != nil {
			b.log.Warn("failed to parse markPrice", zap.String("symbol", r.Symbol), zap.String("raw", r.MarkPrice), zap.Error(err))
			continue
		}
		liqPrice, _ := strconv.ParseFloat(r.LiquidationPrice, 64)

		// Approximate margin ratio as distance-to-liquidation / mark price.
		// Higher ratio = further from liquidation = safer.
		// Negative values indicate the position is already past liquidation price
		// (underwater) — we intentionally do NOT clip to 0 so the margin monitor
		// can alert on this dangerous condition.
		var marginRatio float64
		if markPrice > 0 && liqPrice > 0 {
			if posAmt > 0 { // long
				marginRatio = (markPrice - liqPrice) / markPrice
			} else { // short
				marginRatio = (liqPrice - markPrice) / markPrice
			}
		}

		posSide := string(r.PositionSide)
		if posSide == "BOTH" {
			posSide = ""
		}

		result = append(result, exchange.PositionMarginInfo{
			Symbol:       r.Symbol,
			PositionSide: posSide,
			MarginRatio:  marginRatio,
			Size:         math.Abs(posAmt),
		})
	}
	return result, nil
}

// GetPositions returns open positions with SIGNED amounts (+ long / − short), so the
// direction survives in one-way mode (where PositionSide is "" and GetMarginRatios'
// abs'd Size would lose it). Used by the cross-sectional rebalancer's position syncer.
func (b *OrderBroker) GetPositions(ctx context.Context) ([]exchange.PositionInfo, error) {
	risks, err := b.client.NewGetPositionRiskService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance futures position risk: %w", err)
	}
	var out []exchange.PositionInfo
	for _, r := range risks {
		posAmt, err := strconv.ParseFloat(r.PositionAmt, 64)
		if err != nil || posAmt == 0 {
			continue
		}
		markPrice, _ := strconv.ParseFloat(r.MarkPrice, 64)
		entry, _ := strconv.ParseFloat(r.EntryPrice, 64)
		upnl, _ := strconv.ParseFloat(r.UnRealizedProfit, 64)
		posSide := string(r.PositionSide)
		if posSide == "BOTH" {
			posSide = ""
		}
		out = append(out, exchange.PositionInfo{
			Symbol: r.Symbol, PositionSide: posSide, Amt: posAmt, MarkPrice: markPrice,
			EntryPrice: entry, UnrealizedPnl: upnl,
		})
	}
	return out, nil
}

// GetIncome returns account income records since sinceMs (fees / funding / realized PnL)
// — for attributing the live P&L into its real components.
func (b *OrderBroker) GetIncome(ctx context.Context, sinceMs int64) ([]exchange.IncomeItem, error) {
	hist, err := b.client.NewGetIncomeHistoryService().StartTime(sinceMs).Limit(1000).Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance income history: %w", err)
	}
	out := make([]exchange.IncomeItem, 0, len(hist))
	for _, h := range hist {
		amt, _ := strconv.ParseFloat(h.Income, 64)
		out = append(out, exchange.IncomeItem{Type: h.IncomeType, Symbol: h.Symbol, Amount: amt, TimeMs: h.Time})
	}
	return out, nil
}

// GetBookTicker returns the current best bid/ask for a symbol — for pricing maker limit
// orders tight to the touch.
func (b *OrderBroker) GetBookTicker(ctx context.Context, symbol string) (bid, ask float64, err error) {
	ts, err := b.client.NewListBookTickersService().Symbol(symbol).Do(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("binance futures book ticker: %w", err)
	}
	if len(ts) == 0 {
		return 0, 0, fmt.Errorf("no book ticker for %s", symbol)
	}
	bid, _ = strconv.ParseFloat(ts[0].BidPrice, 64)
	ask, _ = strconv.ParseFloat(ts[0].AskPrice, 64)
	if bid <= 0 || ask <= 0 {
		return 0, 0, fmt.Errorf("invalid book ticker for %s: bid=%v ask=%v", symbol, bid, ask)
	}
	return bid, ask, nil
}

// GetOrderStatus implements exchange.OrderStatusChecker.
// Queries the Binance Futures order endpoint for the current status.
// Returned status values: "NEW", "PARTIALLY_FILLED", "FILLED", "CANCELED", "EXPIRED".
func (b *OrderBroker) GetOrderStatus(ctx context.Context, symbol, orderID string) (string, exchange.OrderFill, error) {
	xID, err := strconv.ParseInt(orderID, 10, 64)
	if err != nil {
		return "", exchange.OrderFill{}, fmt.Errorf("invalid order ID %q: %w", orderID, err)
	}

	// Try normal order first.
	result, err := b.client.NewGetOrderService().
		Symbol(symbol).
		OrderID(xID).
		Do(ctx)
	if err == nil {
		qty, _ := strconv.ParseFloat(result.ExecutedQuantity, 64)
		avgPrice, _ := strconv.ParseFloat(result.AvgPrice, 64)
		fill := exchange.OrderFill{
			ExchangeID: orderID,
			FilledQty:  qty,
			AvgPrice:   avgPrice,
			Status:     string(result.Status),
		}
		return string(result.Status), fill, nil
	}

	// Normal order not found — try algo order (SL/TP placed via Algo API).
	algoResult, algoErr := b.client.NewGetAlgoOrderService().AlgoID(xID).Do(ctx)
	if algoErr != nil {
		return "", exchange.OrderFill{}, fmt.Errorf("binance futures get order: %w (algo: %v)", err, algoErr)
	}

	// Algo statuses: NEW (pending), CANCELED, REJECTED, EXPIRED (triggered → sub-order created)
	algoStatus := string(algoResult.AlgoStatus)
	fill := exchange.OrderFill{
		ExchangeID: orderID,
		Status:     algoStatus,
	}

	// When algo triggers, it creates a sub-order (actualOrderId).
	// If the sub-order exists and is filled, treat the algo as FILLED.
	if algoResult.ActualOrderId != "" && algoResult.ActualOrderId != "0" {
		actualPrice, _ := strconv.ParseFloat(algoResult.ActualPrice, 64)
		actualQty, _ := strconv.ParseFloat(algoResult.Quantity, 64)
		fill.Status = "FILLED"
		fill.AvgPrice = actualPrice
		fill.FilledQty = actualQty
	} else if algoStatus == string(goBinance.AlgoOrderStatusTypeNew) {
		fill.Status = "NEW"
	} else if algoStatus == string(goBinance.AlgoOrderStatusTypeCanceled) {
		fill.Status = "CANCELED"
	}

	return fill.Status, fill, nil
}

// ─── User Data Stream (real-time order/fill notifications) ────────────────────

// SubscribeUserData starts the Binance Futures User Data Stream.
// It creates a listenKey, connects the WebSocket, and pushes ORDER_TRADE_UPDATE
// events to the handler. Auto-renews the listenKey every 30 minutes.
// Blocks until ctx is cancelled.
// SubscribeUserData implements exchange.UserDataSubscriber.
func (b *OrderBroker) SubscribeUserData(ctx context.Context, handler func(fill exchange.OrderFill, clientOrderID string, status string), accountHandler func(walletBalance float64, crossUnPnl float64, reason string), positionHandler func(symbol, side string, qty, entryPrice float64)) {
	firstConnect := true
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		listenKey, err := b.client.NewStartUserStreamService().Do(ctx)
		if err != nil {
			b.log.Error("user data stream: create listenKey failed", zap.Error(err))
			time.Sleep(5 * time.Second)
			continue
		}
		if firstConnect {
			b.log.Info("user data stream: listenKey created")
		} else {
			b.log.Debug("user data stream: listenKey created (reconnect)")
		}

		// Keepalive ticker: renew every 30 minutes
		keepalive := time.NewTicker(30 * time.Minute)

		doneC, stopC, err := exchange.ServeBinanceFuturesWS(b.demo, b.testnet, b.log, func() (chan struct{}, chan struct{}, error) {
			return goBinance.WsUserDataServe(listenKey, func(event *goBinance.WsUserDataEvent) {
				if event == nil {
					return
				}

				// ACCOUNT_UPDATE: balance + position changes
				if event.Event == goBinance.UserDataEventTypeAccountUpdate {
					reason := string(event.AccountUpdate.Reason)
					if accountHandler != nil {
						for _, bal := range event.AccountUpdate.Balances {
							if bal.Asset == "USDT" {
								wb, _ := strconv.ParseFloat(bal.Balance, 64)
								accountHandler(wb, 0, reason) // unrealized PnL sourced from position updates, not balance
								break
							}
						}
					}
					if positionHandler != nil {
						for _, p := range event.AccountUpdate.Positions {
							amt, _ := strconv.ParseFloat(p.Amount, 64)
							ep, _ := strconv.ParseFloat(p.EntryPrice, 64)
							side := string(p.Side)
							if side == "" || side == "BOTH" {
								if amt > 0 {
									side = "LONG"
								} else if amt < 0 {
									side = "SHORT"
								} else {
									// amt == 0 (position closed) in BOTH mode:
									// notify both sides so syncer can detect which was open
									positionHandler(p.Symbol, "LONG", 0, ep)
									positionHandler(p.Symbol, "SHORT", 0, ep)
									continue
								}
							}
							positionHandler(p.Symbol, side, amt, ep)
						}
					}
					return
				}

				// ORDER_TRADE_UPDATE: fill notifications
				if event.Event != goBinance.UserDataEventTypeOrderTradeUpdate {
					return
				}
				o := event.OrderTradeUpdate

				// Use LastFilledQty (incremental for this event), NOT AccumulatedFilledQty
				// which is cumulative and would cause double-counting on partial fills.
				qty, _ := strconv.ParseFloat(o.LastFilledQty, 64)
				lastPrice, _ := strconv.ParseFloat(o.LastFilledPrice, 64)
				avgPrice, _ := strconv.ParseFloat(o.AveragePrice, 64)
				if lastPrice > 0 {
					avgPrice = lastPrice
				} // prefer exact fill price for this event
				commission, _ := strconv.ParseFloat(o.Commission, 64)
				if commission < 0 {
					commission = -commission
				}

				fill := exchange.OrderFill{
					ExchangeID:   strconv.FormatInt(o.ID, 10),
					FilledQty:    qty,
					AvgPrice:     avgPrice,
					Fee:          commission,
					Status:       string(o.Status),
					Symbol:       o.Symbol,
					Side:         string(o.Side),
					PositionSide: string(o.PositionSide),
					IsReduceOnly: o.IsReduceOnly,
				}

				handler(fill, o.ClientOrderID, string(o.Status))
			}, func(err error) {
				// Binance disconnects idle UDS every ~60s — this is normal, not an error
				b.log.Debug("user data stream: ws event", zap.Error(err))
			})
		})

		if err != nil {
			b.log.Error("user data stream: connect failed", zap.Error(err))
			keepalive.Stop()
			time.Sleep(5 * time.Second)
			continue
		}

		if firstConnect {
			b.log.Info("user data stream: connected")
		} else {
			b.log.Debug("user data stream: reconnected")
		}
		firstConnect = false

		// Wait for disconnect or ctx cancel
	loop:
		for {
			select {
			case <-ctx.Done():
				close(stopC)
				keepalive.Stop()
				return
			case <-doneC:
				b.log.Debug("user data stream: reconnecting")
				keepalive.Stop()
				break loop
			case <-keepalive.C:
				if err := b.client.NewKeepaliveUserStreamService().ListenKey(listenKey).Do(ctx); err != nil {
					b.log.Warn("user data stream: keepalive failed", zap.Error(err))
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
}

// Compile-time interface checks.
var _ exchange.OrderClient = (*OrderBroker)(nil)
var _ exchange.MarginQuerier = (*OrderBroker)(nil)
var _ exchange.OrderStatusChecker = (*OrderBroker)(nil)
