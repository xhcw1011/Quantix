package main

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	xfactory "github.com/Quantix/quantix/internal/exchange/factory"
)

func main() {
	log, _ := zap.NewProduction()
	cfg, _ := config.Load("config/config.yaml")

	ctx := context.Background()
	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		panic(err)
	}
	defer store.Close()

	excCfg := config.ExchangeConfig{Active: "binance", Binance: cfg.Exchange.Binance}
	rest, err := xfactory.NewRESTClient(excCfg, log)
	if err != nil {
		panic(err)
	}

	for _, symbol := range []string{"BTCUSDT", "ETHUSDT"} {
		for _, interval := range []string{"1h", "15m"} {
			klines, err := rest.GetKlines(ctx, symbol, interval, 1000)
			if err != nil {
				fmt.Printf("FAIL %s %s: %v\n", symbol, interval, err)
				continue
			}
			count := 0
			for _, k := range klines {
				if err := store.UpsertKline(ctx, k); err == nil {
					count++
				}
			}
			fmt.Printf("OK %s %s: %d/%d bars\n", symbol, interval, count, len(klines))
			time.Sleep(300 * time.Millisecond)
		}
	}
}
