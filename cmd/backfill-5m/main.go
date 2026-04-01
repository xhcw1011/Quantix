// Quick tool to backfill 5m klines into DB for backtesting.
// Usage: go run ./cmd/backfill-5m
package main

import (
	"context"
	"fmt"
	"os"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
)

func main() {
	log, _ := zap.NewProduction()
	cfg, _ := config.Load("config/config.yaml")
	ctx := context.Background()

	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	client, _ := exchange.NewBinanceRESTClient(cfg.Exchange.Binance.APIKey, cfg.Exchange.Binance.APISecret, cfg.Exchange.Binance.Testnet, log)

	// Fetch 5m klines (max 1000 bars = ~3.5 days)
	klines, err := client.GetKlines(ctx, "ETHUSDT", "5m", 1000)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Fetched %d 5m bars: %s → %s\n", len(klines),
		klines[0].OpenTime.Format("01-02 15:04"),
		klines[len(klines)-1].OpenTime.Format("01-02 15:04"))

	if err := store.BulkUpsertKlines(ctx, klines); err != nil {
		fmt.Fprintf(os.Stderr, "upsert: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Done — 5m klines stored in DB")
}
