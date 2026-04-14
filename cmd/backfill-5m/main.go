// Backfill 5m and 15m klines from Binance Futures into DB.
// Loops backward from now to fill any gaps.
// Usage: go run ./cmd/backfill-5m
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
)

func main() {
	log, _ := zap.NewProduction()
	cfg, err := config.Load("config/config.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	ctx := context.Background()

	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	bcfg := cfg.Exchange.Binance
	client, err := exchange.NewBinanceFuturesRESTClient(bcfg.APIKey, bcfg.APISecret, bcfg.Testnet, bcfg, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exchange: %v\n", err)
		os.Exit(1)
	}

	symbol := "ETHUSDT"
	intervals := []string{"5m", "15m"}
	daysBack := 15 // fill 15 days of history

	for _, interval := range intervals {
		fmt.Printf("\n=== Backfilling %s %s (%d days) ===\n", symbol, interval, daysBack)
		totalInserted := 0

		// Walk backward in 1000-bar chunks
		end := time.Now()
		start := end.Add(-time.Duration(daysBack) * 24 * time.Hour)
		cursor := start

		for cursor.Before(end) {
			chunkEnd := cursor.Add(1000 * intervalDuration(interval))
			if chunkEnd.After(end) {
				chunkEnd = end
			}

			klines, err := client.GetKlinesBetween(ctx, symbol, interval, cursor, chunkEnd, 1000)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  fetch error: %v\n", err)
				time.Sleep(2 * time.Second) // rate limit
				cursor = chunkEnd
				continue
			}
			if len(klines) == 0 {
				cursor = chunkEnd
				continue
			}

			fmt.Printf("  %s → %s: %d bars\n",
				klines[0].OpenTime.Format("01-02 15:04"),
				klines[len(klines)-1].OpenTime.Format("01-02 15:04"),
				len(klines))

			if err := store.BulkUpsertKlines(ctx, klines); err != nil {
				fmt.Fprintf(os.Stderr, "  upsert: %v\n", err)
			}
			totalInserted += len(klines)

			// Move cursor past the last bar we received
			cursor = klines[len(klines)-1].OpenTime.Add(intervalDuration(interval))
			time.Sleep(200 * time.Millisecond) // rate limit
		}
		fmt.Printf("  Total: %d bars for %s\n", totalInserted, interval)
	}

	fmt.Println("\nDone")
}

func intervalDuration(interval string) time.Duration {
	switch interval {
	case "1m":
		return time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	default:
		return 5 * time.Minute
	}
}
