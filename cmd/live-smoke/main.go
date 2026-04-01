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
	xbinance "github.com/Quantix/quantix/internal/exchange/binance"
	"github.com/Quantix/quantix/internal/live"
	"github.com/Quantix/quantix/internal/logger"
	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/strategy"
)

type liveOrderResult struct {
	OrderID    string  `json:"order_id"`
	ExchangeID string  `json:"exchange_id"`
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side"`
	Qty        float64 `json:"qty"`
	Price      float64 `json:"price"`
	Fee        float64 `json:"fee"`
	Status     string  `json:"status"`
	Realized   float64 `json:"realized"`
}

type livePositionResult struct {
	Symbol       string  `json:"symbol"`
	Qty          float64 `json:"qty"`
	AvgEntry     float64 `json:"avg_entry"`
	TotalFee     float64 `json:"total_fee"`
	RealizedPnL  float64 `json:"realized_pnl"`
	PositionOpen bool    `json:"position_open"`
}

type liveSmokeResult struct {
	Mode      string              `json:"mode"`
	Testnet   bool                `json:"testnet"`
	Cash      float64             `json:"cash"`
	Equity    float64             `json:"equity"`
	Single    *liveOrderResult    `json:"single,omitempty"`
	Buy       *liveOrderResult    `json:"buy,omitempty"`
	Sell      *liveOrderResult    `json:"sell,omitempty"`
	Position  *livePositionResult `json:"position,omitempty"`
	Timestamp string              `json:"timestamp"`
}

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to config file")
	symbol := flag.String("symbol", "BTCUSDT", "spot symbol to trade")
	qty := flag.Float64("qty", 0.00010, "base-asset quantity for market order")
	roundtrip := flag.Bool("roundtrip", false, "place buy then sell through live broker/OMS")
	pauseMs := flag.Int("pause-ms", 500, "pause between roundtrip legs in milliseconds")
	jsonOut := flag.Bool("json", false, "print machine-readable JSON output")
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

	orderClient, err := xbinance.NewOrderBrokerWithConfig(
		cfg.Exchange.Binance.APIKey,
		cfg.Exchange.Binance.APISecret,
		cfg.Exchange.Binance.Testnet,
		cfg.Exchange.Binance,
		log,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "LIVE_SMOKE_AUTH_FAILED: %v\n", err)
		os.Exit(2)
	}

	o := oms.New(oms.ModeLive, log)
	pm := oms.NewPositionManager()
	broker := live.New(orderClient, o, pm, nil, log)

	syncCtx, syncCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer syncCancel()
	if err := broker.SyncBalance(syncCtx, "USDT"); err != nil {
		fmt.Fprintf(os.Stderr, "LIVE_SMOKE_SYNC_FAILED: %v\n", err)
		os.Exit(3)
	}

	result := liveSmokeResult{
		Testnet:   cfg.Exchange.Binance.Testnet,
		Cash:      broker.Cash(),
		Equity:    broker.Equity(),
		Timestamp: time.Now().Format(time.RFC3339),
	}

	if *roundtrip {
		result.Mode = "roundtrip"
		buyEvt, buyRealized, err := placeViaLiveBroker(o, pm, broker, strategy.OrderRequest{
			Symbol: *symbol,
			Side:   strategy.SideBuy,
			Type:   strategy.OrderMarket,
			Qty:    *qty,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "LIVE_SMOKE_ROUNDTRIP_BUY_FAILED: %v\n", err)
			os.Exit(4)
		}
		result.Buy = toLiveOrderResult(buyEvt, buyRealized)

		time.Sleep(time.Duration(*pauseMs) * time.Millisecond)

		sellEvt, sellRealized, err := placeViaLiveBroker(o, pm, broker, strategy.OrderRequest{
			Symbol: *symbol,
			Side:   strategy.SideSell,
			Type:   strategy.OrderMarket,
			Qty:    buyEvt.Fill.Qty,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "LIVE_SMOKE_ROUNDTRIP_SELL_FAILED: %v\n", err)
			os.Exit(5)
		}
		result.Sell = toLiveOrderResult(sellEvt, sellRealized)
		result.Cash = broker.Cash()
		result.Equity = broker.Equity()
		result.Position = currentPosition(pm, *symbol)

		if *jsonOut {
			mustPrintJSON(result)
			return
		}

		fmt.Printf("LIVE_SMOKE_SYNC_OK cash=%.8f equity=%.8f\n", broker.Cash(), broker.Equity())
		fmt.Printf("LIVE_SMOKE_ROUNDTRIP_BUY_OK order_id=%s exchange_id=%s qty=%.8f price=%.8f status=%s realized=%.8f\n",
			buyEvt.Order.ID, buyEvt.Order.ExchangeID, buyEvt.Fill.Qty, buyEvt.Fill.Price, buyEvt.Order.Status, buyRealized)
		fmt.Printf("LIVE_SMOKE_ROUNDTRIP_SELL_OK order_id=%s exchange_id=%s qty=%.8f price=%.8f status=%s realized=%.8f\n",
			sellEvt.Order.ID, sellEvt.Order.ExchangeID, sellEvt.Fill.Qty, sellEvt.Fill.Price, sellEvt.Order.Status, sellRealized)
		fmt.Printf("LIVE_SMOKE_ROUNDTRIP_DONE symbol=%s qty=%.8f buy_price=%.8f sell_price=%.8f total_realized=%.8f\n",
			*symbol, sellEvt.Fill.Qty, buyEvt.Fill.Price, sellEvt.Fill.Price, buyRealized+sellRealized)
		printPositionAndState(pm, broker, *symbol)
		return
	}

	result.Mode = "single"
	evt, realized, err := placeViaLiveBroker(o, pm, broker, strategy.OrderRequest{
		Symbol: *symbol,
		Side:   strategy.SideBuy,
		Type:   strategy.OrderMarket,
		Qty:    *qty,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "LIVE_SMOKE_ORDER_FAILED: %v\n", err)
		os.Exit(6)
	}
	result.Single = toLiveOrderResult(evt, realized)
	result.Cash = broker.Cash()
	result.Equity = broker.Equity()
	result.Position = currentPosition(pm, *symbol)

	if *jsonOut {
		mustPrintJSON(result)
		return
	}

	fmt.Printf("LIVE_SMOKE_SYNC_OK cash=%.8f equity=%.8f\n", broker.Cash(), broker.Equity())
	fmt.Printf("LIVE_SMOKE_FILL_OK order_id=%s exchange_id=%s symbol=%s side=%s qty=%.8f price=%.8f fee=%.8f status=%s realized=%.8f\n",
		evt.Order.ID,
		evt.Order.ExchangeID,
		evt.Fill.Symbol,
		evt.Fill.Side,
		evt.Fill.Qty,
		evt.Fill.Price,
		evt.Fill.Fee,
		evt.Order.Status,
		realized,
	)
	printPositionAndState(pm, broker, *symbol)
}

func placeViaLiveBroker(o *oms.OMS, pm *oms.PositionManager, broker *live.Broker, req strategy.OrderRequest) (oms.FillEvent, float64, error) {
	fillDone := make(chan struct{})
	var evt oms.FillEvent
	var realized float64

	go func() {
		select {
		case got := <-o.Fills():
			evt = got
			realized = pm.ApplyFill(got.Fill)
			close(fillDone)
		case <-time.After(30 * time.Second):
		}
	}()

	orderID := broker.PlaceOrder(req)
	if orderID == "" {
		return oms.FillEvent{}, 0, fmt.Errorf("empty order id")
	}
	fmt.Printf("LIVE_SMOKE_ORDER_SUBMITTED order_id=%s side=%s qty=%.8f\n", orderID, req.Side, req.Qty)

	select {
	case <-fillDone:
		return evt, realized, nil
	case <-time.After(30 * time.Second):
		return oms.FillEvent{}, 0, fmt.Errorf("no fill event received within 30s")
	}
}

func printPositionAndState(pm *oms.PositionManager, broker *live.Broker, symbol string) {
	pos, ok := pm.Position(symbol)
	if ok {
		fmt.Printf("LIVE_SMOKE_POSITION_OK symbol=%s qty=%.8f avg_entry=%.8f total_fee=%.8f realized_pnl=%.8f\n",
			pos.Symbol, pos.Qty, pos.AvgEntryPrice, pos.TotalFee, pos.RealizedPnL)
	} else {
		fmt.Println("LIVE_SMOKE_POSITION_EMPTY")
	}
	fmt.Printf("LIVE_SMOKE_BROKER_STATE cash=%.8f equity=%.8f\n", broker.Cash(), broker.Equity())
}

func toLiveOrderResult(evt oms.FillEvent, realized float64) *liveOrderResult {
	return &liveOrderResult{
		OrderID:    evt.Order.ID,
		ExchangeID: evt.Order.ExchangeID,
		Symbol:     evt.Fill.Symbol,
		Side:       string(evt.Fill.Side),
		Qty:        evt.Fill.Qty,
		Price:      evt.Fill.Price,
		Fee:        evt.Fill.Fee,
		Status:     string(evt.Order.Status),
		Realized:   realized,
	}
}

func currentPosition(pm *oms.PositionManager, symbol string) *livePositionResult {
	pos, ok := pm.Position(symbol)
	if !ok {
		return &livePositionResult{Symbol: symbol, PositionOpen: false}
	}
	return &livePositionResult{
		Symbol:       pos.Symbol,
		Qty:          pos.Qty,
		AvgEntry:     pos.AvgEntryPrice,
		TotalFee:     pos.TotalFee,
		RealizedPnL:  pos.RealizedPnL,
		PositionOpen: true,
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

var _ zap.Field
