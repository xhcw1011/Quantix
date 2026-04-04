package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/exchange"
	xbinance "github.com/Quantix/quantix/internal/exchange/binance"
	"github.com/Quantix/quantix/internal/logger"
)

type orderResult struct {
	ExchangeID string  `json:"exchange_id"`
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side"`
	Qty        float64 `json:"qty"`
	AvgPrice   float64 `json:"avg_price"`
	Status     string  `json:"status"`
	Fee        float64 `json:"fee"`
}

type binanceSmokeResult struct {
	Mode      string       `json:"mode"`
	Testnet   bool         `json:"testnet"`
	Balance   float64      `json:"usdt_balance"`
	Single    *orderResult `json:"single,omitempty"`
	Buy       *orderResult `json:"buy,omitempty"`
	Sell      *orderResult `json:"sell,omitempty"`
	Realized  float64      `json:"realized,omitempty"`
	Timestamp string       `json:"timestamp"`
}

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to config file")
	symbol := flag.String("symbol", "BTCUSDT", "spot symbol to trade")
	qty := flag.Float64("qty", 0.00010, "base-asset quantity for market order")
	side := flag.String("side", "buy", "order side: buy|sell")
	roundtrip := flag.Bool("roundtrip", false, "place buy then sell using the same quantity")
	pauseMs := flag.Int("pause-ms", 500, "pause between roundtrip legs in milliseconds")
	jsonOut := flag.Bool("json", false, "print machine-readable JSON output")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	log, err := logger.New(cfg.App.Env, cfg.App.LogLevel, cfg.App.LogDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync() //nolint:errcheck

	broker, err := xbinance.NewOrderBrokerWithConfig(
		cfg.Exchange.Binance.APIKey,
		cfg.Exchange.Binance.APISecret,
		cfg.Exchange.Binance.Testnet,
		cfg.Exchange.Binance,
		log,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "SMOKE_AUTH_FAILED: %v\n", err)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	usdt, err := broker.GetBalance(ctx, "USDT")
	if err != nil {
		fmt.Fprintf(os.Stderr, "SMOKE_BALANCE_FAILED: %v\n", err)
		os.Exit(3)
	}

	result := binanceSmokeResult{
		Testnet:   cfg.Exchange.Binance.Testnet,
		Balance:   usdt,
		Timestamp: time.Now().Format(time.RFC3339),
	}

	if *roundtrip {
		result.Mode = "roundtrip"
		buyFill, err := placeOrder(broker, *symbol, exchange.OrderSideBuy, *qty)
		if err != nil {
			fmt.Fprintf(os.Stderr, "SMOKE_ROUNDTRIP_BUY_FAILED: %v\n", err)
			os.Exit(4)
		}
		result.Buy = toOrderResult(*symbol, exchange.OrderSideBuy, buyFill)

		time.Sleep(time.Duration(*pauseMs) * time.Millisecond)

		sellFill, err := placeOrder(broker, *symbol, exchange.OrderSideSell, buyFill.FilledQty)
		if err != nil {
			fmt.Fprintf(os.Stderr, "SMOKE_ROUNDTRIP_SELL_FAILED: %v\n", err)
			os.Exit(5)
		}
		result.Sell = toOrderResult(*symbol, exchange.OrderSideSell, sellFill)
		result.Realized = (sellFill.AvgPrice-buyFill.AvgPrice)*sellFill.FilledQty - buyFill.Fee - sellFill.Fee

		if *jsonOut {
			mustPrintJSON(result)
			return
		}

		fmt.Printf("SMOKE_BALANCE_OK USDT=%.8f\n", usdt)
		fmt.Printf("SMOKE_ROUNDTRIP_BUY_OK exchange_id=%s qty=%.8f avg_price=%.8f status=%s fee=%.8f\n",
			buyFill.ExchangeID, buyFill.FilledQty, buyFill.AvgPrice, buyFill.Status, buyFill.Fee)
		fmt.Printf("SMOKE_ROUNDTRIP_SELL_OK exchange_id=%s qty=%.8f avg_price=%.8f status=%s fee=%.8f\n",
			sellFill.ExchangeID, sellFill.FilledQty, sellFill.AvgPrice, sellFill.Status, sellFill.Fee)
		fmt.Printf("SMOKE_ROUNDTRIP_DONE symbol=%s qty=%.8f buy_avg=%.8f sell_avg=%.8f realized=%.8f\n",
			*symbol, sellFill.FilledQty, buyFill.AvgPrice, sellFill.AvgPrice, result.Realized)
		return
	}

	var orderSide exchange.OrderSide
	switch *side {
	case "buy", "BUY", "Buy":
		orderSide = exchange.OrderSideBuy
	case "sell", "SELL", "Sell":
		orderSide = exchange.OrderSideSell
	default:
		fmt.Fprintf(os.Stderr, "invalid side %q; use buy|sell\n", *side)
		os.Exit(6)
	}

	result.Mode = "single"
	fill, err := placeOrder(broker, *symbol, orderSide, *qty)
	if err != nil {
		fmt.Fprintf(os.Stderr, "SMOKE_ORDER_FAILED: %v\n", err)
		os.Exit(7)
	}
	result.Single = toOrderResult(*symbol, orderSide, fill)

	if *jsonOut {
		mustPrintJSON(result)
		return
	}

	fmt.Printf("SMOKE_BALANCE_OK USDT=%.8f\n", usdt)
	fmt.Printf("SMOKE_ORDER_OK exchange_id=%s symbol=%s side=%s qty=%.8f avg_price=%.8f status=%s fee=%.8f\n",
		fill.ExchangeID, *symbol, orderSide, fill.FilledQty, fill.AvgPrice, fill.Status, fill.Fee)
}

func placeOrder(broker *xbinance.OrderBroker, symbol string, side exchange.OrderSide, qty float64) (exchange.OrderFill, error) {
	orderCtx, orderCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer orderCancel()
	return broker.PlaceMarketOrder(orderCtx, symbol, side, "", qty, "")
}

func toOrderResult(symbol string, side exchange.OrderSide, fill exchange.OrderFill) *orderResult {
	return &orderResult{
		ExchangeID: fill.ExchangeID,
		Symbol:     symbol,
		Side:       string(side),
		Qty:        fill.FilledQty,
		AvgPrice:   fill.AvgPrice,
		Status:     fill.Status,
		Fee:        fill.Fee,
	}
}

func mustPrintJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "json encode: %v\n", err)
		os.Exit(10)
	}
}
