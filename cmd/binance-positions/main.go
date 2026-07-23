// One-off read-only: print the REAL signed positions for a Binance futures
// credential straight from the exchange (GetPositions), to resolve a bot-vs-exchange
// desync. Never trades. Never prints secrets.
//
//	go run ./cmd/binance-positions -cred 6 -user 4 -symbol BTCUSDT
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Quantix/quantix/internal/api"
	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/exchange/binance_futures"
	"github.com/Quantix/quantix/internal/logger"
)

func main() {
	credID := flag.Int("cred", 6, "credential_id")
	userID := flag.Int("user", 4, "user_id")
	symbol := flag.String("symbol", "", "filter symbol (empty = all)")
	cfgPath := flag.String("config", "/opt/quantix/config/config.yaml", "config path")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		die("config: %v", err)
	}
	log, _ := logger.New("production", "error", "")
	defer log.Sync()

	kB, err := hex.DecodeString(os.Getenv("QUANTIX_ENCRYPTION_KEY"))
	if err != nil || len(kB) == 0 {
		die("QUANTIX_ENCRYPTION_KEY missing/invalid")
	}
	enc, err := api.NewEncryptor(kB)
	if err != nil {
		die("encryptor: %v", err)
	}
	ctx, cn := context.WithTimeout(context.Background(), 30*time.Second)
	defer cn()

	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		die("db: %v", err)
	}
	defer store.Close()

	cred, err := store.GetCredentialByID(ctx, *credID, *userID)
	if err != nil {
		die("load credential: %v", err)
	}
	apiKey, _ := enc.Decrypt(cred.APIKey)
	apiSecret, _ := enc.Decrypt(cred.APISecret)

	broker, err := binance_futures.NewOrderBrokerWithConfig(apiKey, apiSecret, cred.Testnet,
		config.BinanceConfig{APIKey: apiKey, APISecret: apiSecret, Testnet: cred.Testnet, Demo: cred.Demo, MarketType: "futures"}, log)
	if err != nil {
		die("build broker: %v", err)
	}

	pq, ok := interface{}(broker).(exchange.PositionQuerier)
	if !ok {
		die("broker does not implement PositionQuerier")
	}
	positions, err := pq.GetPositions(ctx)
	if err != nil {
		die("GetPositions: %v", err)
	}

	fmt.Printf("account: cred=%d user=%d demo=%v testnet=%v\n", *credID, *userID, cred.Demo, cred.Testnet)
	fmt.Println("── signed positions from the exchange (Amt: +long / -short) ──")
	found := false
	for _, p := range positions {
		if *symbol != "" && p.Symbol != *symbol {
			continue
		}
		if p.Amt == 0 {
			continue
		}
		found = true
		fmt.Printf("  %-12s posSide=%-5s Amt=%+.6f entry=%.2f mark=%.2f uPnL=%+.4f\n",
			p.Symbol, p.PositionSide, p.Amt, p.EntryPrice, p.MarkPrice, p.UnrealizedPnl)
	}
	if !found {
		fmt.Println("  (no open positions — account is FLAT)")
	}
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}
