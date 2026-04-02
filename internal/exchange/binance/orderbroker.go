// Package binance provides an order broker for Binance Spot (testnet and live).
package binance

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	goBinance "github.com/adshao/go-binance/v2"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/exchange"
)

// OrderBroker submits Spot orders to Binance (testnet or live).
// It implements exchange.OrderClient.
type OrderBroker struct {
	client *goBinance.Client
	log    *zap.Logger
}

// NewOrderBroker creates an OrderBroker.
//
// Safety gates:
//   - testnet=true  → hits testnet.binance.vision (safe, no real money)
//   - testnet=false → requires env QUANTIX_LIVE_CONFIRM=true (real money)
func NewOrderBroker(apiKey, apiSecret string, testnet bool, log *zap.Logger) (*OrderBroker, error) {
	return NewOrderBrokerWithConfig(apiKey, apiSecret, testnet, config.BinanceConfig{Testnet: testnet}, log)
}

// NewOrderBrokerWithConfig creates an OrderBroker with optional private-key auth.
// Network mode is determined by cfg.Demo / cfg.Testnet flags.
func NewOrderBrokerWithConfig(apiKey, apiSecret string, testnet bool, cfg config.BinanceConfig, log *zap.Logger) (*OrderBroker, error) {
	// Ensure config flags are consistent with the explicit testnet param.
	if !cfg.Demo {
		cfg.Testnet = testnet
	}
	exchange.ApplyBinanceNetworkMode(cfg)

	if cfg.Demo {
		log.Info("Binance Spot order broker: DEMO mode",
			zap.String("endpoint", "demo-api.binance.com"))
	} else if cfg.Testnet {
		log.Info("Binance Spot order broker: TESTNET mode",
			zap.String("endpoint", "testnet.binance.vision"))
	} else {
		if os.Getenv("QUANTIX_LIVE_CONFIRM") != "true" {
			return nil, fmt.Errorf(
				"Binance live mode requires QUANTIX_LIVE_CONFIRM=true env var; " +
					"set it explicitly to confirm real-money trading")
		}
		log.Warn("Binance Spot order broker: LIVE mode — REAL MONEY AT RISK")
	}

	client := goBinance.NewClient(apiKey, apiSecret)

	// Apply private-key auth if configured
	if err := exchange.ConfigureBinanceAuth(client, cfg); err != nil {
		return nil, fmt.Errorf("binance auth config: %w", err)
	}

	// Verify connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.NewGetAccountService().Do(ctx); err != nil {
		return nil, fmt.Errorf("binance API connectivity check failed: %w", err)
	}

	log.Info("Binance order broker ready", zap.String("key_type", client.KeyType))
	return &OrderBroker{client: client, log: log}, nil
}

// PlaceMarketOrder submits a market order and returns fill details.
// positionSide and clientOrderID are ignored for spot.
func (b *OrderBroker) PlaceMarketOrder(ctx context.Context, symbol string, side exchange.OrderSide, positionSide string, qty float64, clientOrderID string) (exchange.OrderFill, error) {
	result, err := b.client.NewCreateOrderService().
		Symbol(symbol).
		Side(toBinanceSide(side)).
		Type(goBinance.OrderTypeMarket).
		Quantity(fmt.Sprintf("%.8f", qty)).
		Do(ctx)
	if err != nil {
		return exchange.OrderFill{}, fmt.Errorf("binance create order: %w", err)
	}

	fill := b.parseFill(result)
	b.log.Info("Binance market order filled",
		zap.String("exchange_id", fill.ExchangeID),
		zap.String("symbol", symbol),
		zap.String("side", string(side)), //nolint:gosec
		zap.Float64("qty", fill.FilledQty),
		zap.Float64("avg_price", fill.AvgPrice),
		zap.Float64("fee", fill.Fee),
	)
	return fill, nil
}

// CancelOrder cancels a Binance order by exchangeID (numeric string).
func (b *OrderBroker) CancelOrder(ctx context.Context, symbol, exchangeID string) error {
	xID, err := strconv.ParseInt(exchangeID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid exchange order ID %q: %w", exchangeID, err)
	}
	_, err = b.client.NewCancelOrderService().
		Symbol(symbol).
		OrderID(xID).
		Do(ctx)
	if err != nil {
		return fmt.Errorf("binance cancel order: %w", err)
	}
	return nil
}

// GetBalance returns the free balance for the given asset.
func (b *OrderBroker) GetBalance(ctx context.Context, asset string) (float64, error) {
	acct, err := b.client.NewGetAccountService().Do(ctx)
	if err != nil {
		return 0, fmt.Errorf("binance get account: %w", err)
	}
	for _, bal := range acct.Balances {
		if bal.Asset == asset {
			free, err := strconv.ParseFloat(bal.Free, 64)
			if err != nil {
				return 0, fmt.Errorf("parse balance for %s: %w", asset, err)
			}
			return free, nil
		}
	}
	return 0, fmt.Errorf("asset %s not found in Binance account", asset)
}

// parseFill extracts fill details from a Binance CreateOrderResponse.
// Computes VWAP average price and sums fees across all partial fills.
func (b *OrderBroker) parseFill(r *goBinance.CreateOrderResponse) exchange.OrderFill {
	qty, err := strconv.ParseFloat(r.ExecutedQuantity, 64)
	if err != nil {
		b.log.Warn("failed to parse ExecutedQuantity", zap.String("raw", r.ExecutedQuantity), zap.Error(err))
	}

	// VWAP average price from fills
	var totalQty, totalValue, totalFee float64
	for _, f := range r.Fills {
		fQty, err := strconv.ParseFloat(f.Quantity, 64)
		if err != nil {
			b.log.Warn("failed to parse fill quantity", zap.String("raw", f.Quantity), zap.Error(err))
			continue
		}
		fPrice, err := strconv.ParseFloat(f.Price, 64)
		if err != nil {
			b.log.Warn("failed to parse fill price", zap.String("raw", f.Price), zap.Error(err))
			continue
		}
		fFee, _ := strconv.ParseFloat(f.Commission, 64)
		totalQty += fQty
		totalValue += fQty * fPrice
		totalFee += fFee
	}

	var avgPrice float64
	if totalQty > 0 {
		avgPrice = totalValue / totalQty
	} else {
		// Fallback: use the order-level price field
		avgPrice, _ = strconv.ParseFloat(r.Price, 64)
	}

	return exchange.OrderFill{
		ExchangeID: strconv.FormatInt(r.OrderID, 10),
		FilledQty:  qty,
		AvgPrice:   avgPrice,
		Fee:        totalFee,
		Status:     string(r.Status),
	}
}

// PlaceLimitOrder submits a GTC limit order on Binance Spot.
// positionSide is ignored for spot (no hedge mode).
func (b *OrderBroker) PlaceLimitOrder(ctx context.Context, symbol string, side exchange.OrderSide, _ string, qty, price float64, clientOrderID string) (string, error) {
	svc := b.client.NewCreateOrderService().
		Symbol(symbol).
		Side(toBinanceSide(side)).
		Type(goBinance.OrderTypeLimit).
		TimeInForce(goBinance.TimeInForceTypeGTC).
		Quantity(fmt.Sprintf("%.8f", qty)).
		Price(fmt.Sprintf("%.8f", price))
	if clientOrderID != "" {
		svc = svc.NewClientOrderID(clientOrderID)
	}
	result, err := svc.Do(ctx)
	if err != nil {
		return "", fmt.Errorf("binance spot limit order: %w", err)
	}
	ordID := strconv.FormatInt(result.OrderID, 10)
	b.log.Info("Binance Spot limit order placed",
		zap.String("order_id", ordID), zap.String("symbol", symbol), zap.Float64("price", price))
	return ordID, nil
}

// PlaceReduceOnlyLimitOrder on spot is identical to PlaceLimitOrder (spot has no reduce-only concept).
func (b *OrderBroker) PlaceReduceOnlyLimitOrder(ctx context.Context, symbol string, side exchange.OrderSide, positionSide string, qty, price float64, clientOrderID string) (string, error) {
	return b.PlaceLimitOrder(ctx, symbol, side, positionSide, qty, price, clientOrderID)
}

// PlaceStopMarketOrder places a STOP_LOSS_LIMIT order on Binance Spot.
// Binance Spot has no STOP_MARKET type; STOP_LOSS_LIMIT with limit == stop
// provides market-like execution at the trigger price.
// positionSide is ignored for spot.
func (b *OrderBroker) PlaceStopMarketOrder(ctx context.Context, symbol string, side exchange.OrderSide, _ string, qty, stopPrice float64, clientOrderID string) (string, error) {
	svc := b.client.NewCreateOrderService().
		Symbol(symbol).
		Side(toBinanceSide(side)).
		Type("STOP_LOSS_LIMIT").
		TimeInForce(goBinance.TimeInForceTypeGTC).
		Quantity(fmt.Sprintf("%.8f", qty)).
		StopPrice(fmt.Sprintf("%.8f", stopPrice)).
		Price(fmt.Sprintf("%.8f", stopPrice))
	if clientOrderID != "" {
		svc = svc.NewClientOrderID(clientOrderID)
	}
	result, err := svc.Do(ctx)
	if err != nil {
		return "", fmt.Errorf("binance spot stop-loss-limit order: %w", err)
	}
	ordID := strconv.FormatInt(result.OrderID, 10)
	b.log.Info("Binance Spot stop-loss-limit order placed",
		zap.String("order_id", ordID), zap.String("symbol", symbol), zap.Float64("stop_price", stopPrice))
	return ordID, nil
}

// PlaceTakeProfitMarketOrder places a TAKE_PROFIT_LIMIT order on Binance Spot.
// Binance Spot has no TAKE_PROFIT_MARKET type; TAKE_PROFIT_LIMIT with limit == trigger
// provides market-like execution at the trigger price.
// positionSide is ignored for spot.
func (b *OrderBroker) PlaceTakeProfitMarketOrder(ctx context.Context, symbol string, side exchange.OrderSide, _ string, qty, triggerPrice float64, clientOrderID string) (string, error) {
	svc := b.client.NewCreateOrderService().
		Symbol(symbol).
		Side(toBinanceSide(side)).
		Type("TAKE_PROFIT_LIMIT").
		TimeInForce(goBinance.TimeInForceTypeGTC).
		Quantity(fmt.Sprintf("%.8f", qty)).
		StopPrice(fmt.Sprintf("%.8f", triggerPrice)).
		Price(fmt.Sprintf("%.8f", triggerPrice))
	if clientOrderID != "" {
		svc = svc.NewClientOrderID(clientOrderID)
	}
	result, err := svc.Do(ctx)
	if err != nil {
		return "", fmt.Errorf("binance spot take-profit-limit order: %w", err)
	}
	ordID := strconv.FormatInt(result.OrderID, 10)
	b.log.Info("Binance Spot take-profit-limit order placed",
		zap.String("order_id", ordID), zap.String("symbol", symbol), zap.Float64("trigger_price", triggerPrice))
	return ordID, nil
}

// SetLeverage is not supported by Binance Spot broker.
func (b *OrderBroker) SetLeverage(_ context.Context, _ string, _ int) error {
	return fmt.Errorf("SetLeverage not supported by Binance Spot broker")
}

// GetOrderStatus implements exchange.OrderStatusChecker.
// Queries the Binance Spot order endpoint for the current status.
// Returned status values: "NEW", "PARTIALLY_FILLED", "FILLED", "CANCELED", "EXPIRED".
// Average price is computed from CummulativeQuoteQuantity / ExecutedQuantity
// because Spot orders do not carry an AvgPrice field.
func (b *OrderBroker) GetOrderStatus(ctx context.Context, symbol, orderID string) (string, exchange.OrderFill, error) {
	xID, err := strconv.ParseInt(orderID, 10, 64)
	if err != nil {
		return "", exchange.OrderFill{}, fmt.Errorf("invalid order ID %q: %w", orderID, err)
	}
	result, err := b.client.NewGetOrderService().
		Symbol(symbol).
		OrderID(xID).
		Do(ctx)
	if err != nil {
		return "", exchange.OrderFill{}, fmt.Errorf("binance spot get order: %w", err)
	}
	qty, err := strconv.ParseFloat(result.ExecutedQuantity, 64)
	if err != nil {
		return "", exchange.OrderFill{}, fmt.Errorf("parse ExecutedQuantity %q: %w", result.ExecutedQuantity, err)
	}
	var avgPrice float64
	if qty > 0 {
		cqq, parseErr := strconv.ParseFloat(result.CummulativeQuoteQuantity, 64)
		if parseErr == nil && cqq > 0 {
			avgPrice = cqq / qty
		}
	}
	fill := exchange.OrderFill{
		ExchangeID: orderID,
		FilledQty:  qty,
		AvgPrice:   avgPrice,
		Status:     string(result.Status),
	}
	return string(result.Status), fill, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// toBinanceSide converts an exchange.OrderSide to a goBinance SideType.
func toBinanceSide(side exchange.OrderSide) goBinance.SideType {
	if side == exchange.OrderSideSell {
		return goBinance.SideTypeSell
	}
	return goBinance.SideTypeBuy
}

// Compile-time interface checks.
var _ exchange.OrderClient = (*OrderBroker)(nil)
var _ exchange.OrderStatusChecker = (*OrderBroker)(nil)
