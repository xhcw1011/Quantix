// Package main provides a Binance USDM Futures testnet smoke test.
// It verifies: connectivity, balance, leverage, market order, and optionally a roundtrip.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/exchange/binance_futures"
	"github.com/Quantix/quantix/internal/logger"
)

type orderResult struct {
	ExchangeID string  `json:"exchange_id"`
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side"`
	PosSide    string  `json:"position_side"`
	Qty        float64 `json:"qty"`
	AvgPrice   float64 `json:"avg_price"`
	Status     string  `json:"status"`
	Fee        float64 `json:"fee"`
}

type smokeResult struct {
	Mode      string       `json:"mode"`
	Testnet   bool         `json:"testnet"`
	Balance   float64      `json:"usdt_balance"`
	Leverage  int          `json:"leverage"`
	Single    *orderResult `json:"single,omitempty"`
	Buy       *orderResult `json:"buy,omitempty"`
	Sell      *orderResult `json:"sell,omitempty"`
	Realized  float64      `json:"realized,omitempty"`
	Timestamp string       `json:"timestamp"`
}

func main() {
	cfgPath := flag.String("config", "config/config.futures.yaml", "path to config file")
	symbol := flag.String("symbol", "BTCUSDT", "futures symbol")
	qty := flag.Float64("qty", 0.002, "base-asset quantity")
	leverage := flag.Int("leverage", 5, "leverage to set")
	side := flag.String("side", "buy", "order side: buy|sell")
	posSide := flag.String("pos-side", "", "position side: LONG|SHORT|\"\" (one-way)")
	roundtrip := flag.Bool("roundtrip", false, "open+close roundtrip")
	pauseMs := flag.Int("pause-ms", 2000, "pause between roundtrip legs (ms)")
	jsonOut := flag.Bool("json", false, "JSON output")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	log, err := logger.New(cfg.App.Env, cfg.App.LogLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync() //nolint:errcheck

	// Create Binance Futures broker
	broker, err := binance_futures.NewOrderBrokerWithConfig(
		cfg.Exchange.Binance.APIKey,
		cfg.Exchange.Binance.APISecret,
		cfg.Exchange.Binance.Testnet,
		cfg.Exchange.Binance,
		log,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FUTURES_AUTH_FAILED: %v\n", err)
		os.Exit(2)
	}
	fmt.Println("FUTURES_AUTH_OK")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Check balance
	usdt, err := broker.GetBalance(ctx, "USDT")
	if err != nil {
		fmt.Fprintf(os.Stderr, "FUTURES_BALANCE_FAILED: %v\n", err)
		os.Exit(3)
	}
	fmt.Printf("FUTURES_BALANCE_OK USDT=%.4f\n", usdt)

	// Set leverage
	if err := broker.SetLeverage(ctx, *symbol, *leverage); err != nil {
		fmt.Fprintf(os.Stderr, "FUTURES_LEVERAGE_FAILED: %v\n", err)
		os.Exit(4)
	}
	fmt.Printf("FUTURES_LEVERAGE_OK symbol=%s leverage=%dx\n", *symbol, *leverage)

	// Check margin ratios
	margins, err := broker.GetMarginRatios(ctx)
	if err != nil {
		fmt.Printf("FUTURES_MARGIN_WARN: %v\n", err)
	} else {
		fmt.Printf("FUTURES_MARGIN_OK positions_with_risk=%d\n", len(margins))
		for _, m := range margins {
			fmt.Printf("  %s [%s] size=%.6f ratio=%.4f\n", m.Symbol, m.PositionSide, m.Size, m.MarginRatio)
		}
	}

	result := smokeResult{
		Testnet:   cfg.Exchange.Binance.Testnet,
		Balance:   usdt,
		Leverage:  *leverage,
		Timestamp: time.Now().Format(time.RFC3339),
	}

	if *roundtrip {
		result.Mode = "roundtrip"
		openSide := exchange.OrderSideBuy
		closeSide := exchange.OrderSideSell
		if *posSide == "SHORT" {
			openSide = exchange.OrderSideSell
			closeSide = exchange.OrderSideBuy
		}

		buyFill, err := placeAndWaitFill(ctx, broker, log, *symbol, openSide, *posSide, *qty)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FUTURES_OPEN_FAILED: %v\n", err)
			os.Exit(5)
		}
		result.Buy = fillToResult(*symbol, openSide, *posSide, buyFill)
		fmt.Printf("FUTURES_OPEN_OK exchange_id=%s side=%s qty=%.6f price=%.2f\n",
			buyFill.ExchangeID, openSide, buyFill.FilledQty, buyFill.AvgPrice)

		time.Sleep(time.Duration(*pauseMs) * time.Millisecond)

		sellFill, err := placeAndWaitFill(ctx, broker, log, *symbol, closeSide, *posSide, buyFill.FilledQty)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FUTURES_CLOSE_FAILED: %v\n", err)
			os.Exit(6)
		}
		result.Sell = fillToResult(*symbol, closeSide, *posSide, sellFill)
		result.Realized = (sellFill.AvgPrice - buyFill.AvgPrice) * sellFill.FilledQty
		if *posSide == "SHORT" {
			result.Realized = (buyFill.AvgPrice - sellFill.AvgPrice) * sellFill.FilledQty
		}
		result.Realized -= buyFill.Fee + sellFill.Fee

		fmt.Printf("FUTURES_CLOSE_OK exchange_id=%s side=%s qty=%.6f price=%.2f\n",
			sellFill.ExchangeID, closeSide, sellFill.FilledQty, sellFill.AvgPrice)
		fmt.Printf("FUTURES_ROUNDTRIP_DONE realized=%.6f\n", result.Realized)
	} else {
		result.Mode = "single"
		var orderSide exchange.OrderSide
		switch *side {
		case "buy", "BUY":
			orderSide = exchange.OrderSideBuy
		case "sell", "SELL":
			orderSide = exchange.OrderSideSell
		default:
			fmt.Fprintf(os.Stderr, "invalid side %q\n", *side)
			os.Exit(7)
		}

		fill, err := placeAndWaitFill(ctx, broker, log, *symbol, orderSide, *posSide, *qty)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FUTURES_ORDER_FAILED: %v\n", err)
			os.Exit(8)
		}
		result.Single = fillToResult(*symbol, orderSide, *posSide, fill)
		fmt.Printf("FUTURES_ORDER_OK exchange_id=%s symbol=%s side=%s qty=%.6f price=%.2f status=%s\n",
			fill.ExchangeID, *symbol, orderSide, fill.FilledQty, fill.AvgPrice, fill.Status)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result) //nolint:errcheck
	}
}

// placeAndWaitFill places a market order and polls GetOrderStatus until filled.
// This is a smoke-test convenience — in production, fills arrive via WS user data stream.
func placeAndWaitFill(ctx context.Context, broker *binance_futures.OrderBroker, log *zap.Logger, symbol string, side exchange.OrderSide, posSide string, qty float64) (exchange.OrderFill, error) {
	fill, err := broker.PlaceMarketOrder(ctx, symbol, side, posSide, qty, "")
	if err != nil {
		return exchange.OrderFill{}, err
	}

	// If already filled (live often returns FILLED immediately), return
	if fill.FilledQty > 0 && (fill.Status == "FILLED" || fill.Status == "filled") {
		return fill, nil
	}

	// Poll until filled (testnet may return NEW with qty=0)
	log.Info("order submitted, polling for fill...", zap.String("exchange_id", fill.ExchangeID))
	for i := 0; i < 10; i++ {
		select {
		case <-ctx.Done():
			return fill, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}

		status, polledFill, err := broker.GetOrderStatus(ctx, symbol, fill.ExchangeID)
		if err != nil {
			log.Warn("poll failed", zap.Int("attempt", i+1), zap.Error(err))
			continue
		}
		if status == "FILLED" || status == "filled" {
			log.Info("order filled", zap.String("exchange_id", fill.ExchangeID),
				zap.Float64("qty", polledFill.FilledQty), zap.Float64("price", polledFill.AvgPrice))
			return polledFill, nil
		}
		log.Info("order not yet filled", zap.String("status", status), zap.Int("attempt", i+1))
	}
	return fill, fmt.Errorf("order %s not filled after polling", fill.ExchangeID)
}

func fillToResult(symbol string, side exchange.OrderSide, posSide string, fill exchange.OrderFill) *orderResult {
	return &orderResult{
		ExchangeID: fill.ExchangeID, Symbol: symbol, Side: string(side),
		PosSide: posSide, Qty: fill.FilledQty, AvgPrice: fill.AvgPrice,
		Status: fill.Status, Fee: fill.Fee,
	}
}
