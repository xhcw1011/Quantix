package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange/factory"
	"github.com/Quantix/quantix/internal/logger"
)

func main() {
	symbolF := flag.String("symbol", "ETHUSDT", "symbol to backfill")
	intervalsF := flag.String("intervals", "5m,1m,15m", "comma-separated intervals")
	startF := flag.String("start", "2026-04-01", "start date YYYY-MM-DD (UTC)")
	endF := flag.String("end", "2026-04-04", "end date YYYY-MM-DD (UTC)")
	flag.Parse()

	start, err := time.Parse("2006-01-02", *startF)
	if err != nil {
		fmt.Println("start:", err)
		os.Exit(1)
	}
	end, err := time.Parse("2006-01-02", *endF)
	if err != nil {
		fmt.Println("end:", err)
		os.Exit(1)
	}

	cfg, err := config.Load("config/config.yaml")
	if err != nil {
		fmt.Println("config:", err)
		os.Exit(1)
	}
	log, _ := logger.New("development", "info", "")
	ctx := context.Background()

	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		fmt.Println("db:", err)
		os.Exit(1)
	}
	defer store.Close()

	rest, err := factory.NewRESTClient(cfg.Exchange, log)
	if err != nil {
		fmt.Println("rest:", err)
		os.Exit(1)
	}

	symbol := *symbolF
	intervals := strings.Split(*intervalsF, ",")

	for _, itv := range intervals {
		fmt.Printf("Backfilling %s %s...\n", symbol, itv)
		cur := start
		total := 0
		for cur.Before(end) {
			klines, err := rest.GetKlinesBetween(ctx, symbol, itv, cur, end, 1500)
			if err != nil {
				fmt.Printf("  error: %v\n", err)
				break
			}
			if len(klines) == 0 {
				break
			}
			for i := range klines {
				klines[i].Symbol = symbol
				klines[i].Interval = itv
			}
			if err := store.BulkUpsertKlines(ctx, klines); err != nil {
				log.Error("bulk upsert", zap.Error(err))
			}
			total += len(klines)
			cur = klines[len(klines)-1].CloseTime.Add(time.Second)
			fmt.Printf("  batch: %d bars (total: %d, up to %s)\n", len(klines), total, cur.Format("2006-01-02 15:04"))
		}
		fmt.Printf("  done: %d bars\n", total)
	}
}
