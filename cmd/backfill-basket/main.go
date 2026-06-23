// backfill-basket pulls historical klines for a basket of symbols over a date
// range and upserts them, for multi-asset (cross-sectional momentum) research.
//
//	go run ./cmd/backfill-basket -interval 1d -start 2023-01-01 -end 2026-06-23 \
//	  -symbols BTCUSDT,ETHUSDT,SOLUSDT,...
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
	defSyms := "BTCUSDT,ETHUSDT,BNBUSDT,SOLUSDT,XRPUSDT,ADAUSDT,DOGEUSDT,AVAXUSDT,LINKUSDT,LTCUSDT,DOTUSDT,BCHUSDT,TRXUSDT,ETCUSDT,ATOMUSDT"
	symbolsCSV := flag.String("symbols", defSyms, "comma-separated symbols")
	interval := flag.String("interval", "1d", "kline interval (1d, 4h, 1h …)")
	startStr := flag.String("start", "2023-01-01", "start date YYYY-MM-DD")
	endStr := flag.String("end", "2026-06-23", "end date YYYY-MM-DD")
	flag.Parse()

	start, err := time.Parse("2006-01-02", *startStr)
	if err != nil {
		fmt.Println("parse -start:", err)
		os.Exit(1)
	}
	end, err := time.Parse("2006-01-02", *endStr)
	if err != nil {
		fmt.Println("parse -end:", err)
		os.Exit(1)
	}

	cfg, err := config.Load("config/config.yaml")
	if err != nil {
		fmt.Println("config:", err)
		os.Exit(1)
	}
	log, _ := logger.New("development", "warn", "")
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

	var symbols []string
	for _, s := range strings.Split(*symbolsCSV, ",") {
		if s = strings.TrimSpace(s); s != "" {
			symbols = append(symbols, s)
		}
	}

	for _, symbol := range symbols {
		cur := start
		total := 0
		for cur.Before(end) {
			klines, err := rest.GetKlinesBetween(ctx, symbol, *interval, cur, end, 1500)
			if err != nil {
				fmt.Printf("%-10s error: %v\n", symbol, err)
				break
			}
			if len(klines) == 0 {
				break
			}
			for i := range klines {
				klines[i].Symbol = symbol
				klines[i].Interval = *interval
			}
			if err := store.BulkUpsertKlines(ctx, klines); err != nil {
				log.Error("bulk upsert", zap.Error(err))
			}
			total += len(klines)
			next := klines[len(klines)-1].CloseTime.Add(time.Second)
			if !next.After(cur) {
				break // no forward progress — avoid infinite loop
			}
			cur = next
		}
		fmt.Printf("%-10s %s: %d bars\n", symbol, *interval, total)
	}
}
