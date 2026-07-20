// One-off: cancel a single resting order on a Binance USDM Futures account.
//
// Use case: an orphan protective STOP_MARKET left resting after a position was
// closed manually (the exchange does not auto-cancel a non-closePosition stop,
// and a manual close bypasses the bot's cancel path). The bot has no cancel-order
// API, so this tool loads the credential from the server DB, builds the same
// binance_futures broker the engine uses (correct demo/testnet endpoint), lists
// the open orders, cancels the target order ID, then re-lists to verify.
//
// Never prints decrypted secrets.
//
//	go run ./cmd/binance-cancel-order -cred 3 -user 4 -symbol BTCUSDT -order 1000000140287953
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
	"github.com/Quantix/quantix/internal/exchange/binance_futures"
	"github.com/Quantix/quantix/internal/logger"
)

func main() {
	credID := flag.Int("cred", 3, "credential_id")
	userID := flag.Int("user", 4, "user_id")
	symbol := flag.String("symbol", "BTCUSDT", "symbol")
	orderID := flag.String("order", "", "exchange order ID to cancel (required)")
	cfgPath := flag.String("config", "/opt/quantix/config/config.yaml", "config path")
	flag.Parse()
	if *orderID == "" {
		die("-order is required")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		die("config: %v", err)
	}
	log, _ := logger.New("production", "info", "")
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
		die("load credential (cred=%d user=%d): %v", *credID, *userID, err)
	}
	if cred.Exchange != "binance" {
		die("credential %d is exchange %q, not binance", *credID, cred.Exchange)
	}
	apiKey, err := enc.Decrypt(cred.APIKey)
	if err != nil {
		die("decrypt key: %v", err)
	}
	apiSecret, err := enc.Decrypt(cred.APISecret)
	if err != nil {
		die("decrypt secret: %v", err)
	}

	broker, err := binance_futures.NewOrderBrokerWithConfig(apiKey, apiSecret, cred.Testnet,
		config.BinanceConfig{
			APIKey: apiKey, APISecret: apiSecret,
			Testnet: cred.Testnet, Demo: cred.Demo, MarketType: "futures",
		}, log)
	if err != nil {
		die("build broker: %v", err)
	}

	fmt.Printf("account: cred=%d user=%d demo=%v testnet=%v  symbol=%s\n",
		*credID, *userID, cred.Demo, cred.Testnet, *symbol)

	before, err := broker.ListOpenOrders(ctx, *symbol)
	if err != nil {
		die("list open orders: %v", err)
	}
	fmt.Printf("\nopen orders BEFORE (%d):\n", len(before))
	found := false
	for _, o := range before {
		mark := ""
		if o.ExchangeID == *orderID {
			mark = "  <-- target"
			found = true
		}
		fmt.Printf("  %-16s %-4s %-5s %-12s qty=%.4g stop=%.2f status=%s%s\n",
			o.ExchangeID, o.Side, o.PositionSide, o.Type, o.Qty, o.StopPrice, o.Status, mark)
	}
	if !found {
		fmt.Printf("\ntarget order %s is NOT in the open-order list — already gone. Nothing to do.\n", *orderID)
		return
	}

	fmt.Printf("\ncancelling %s ...\n", *orderID)
	if err := broker.CancelOrder(ctx, *symbol, *orderID); err != nil {
		die("cancel failed: %v", err)
	}
	fmt.Println("✓ cancel request accepted")

	time.Sleep(1 * time.Second)
	after, err := broker.ListOpenOrders(ctx, *symbol)
	if err != nil {
		die("re-list: %v", err)
	}
	for _, o := range after {
		if o.ExchangeID == *orderID {
			fmt.Printf("\n⚠ order %s still present after cancel — check manually\n", *orderID)
			os.Exit(1)
		}
	}
	fmt.Printf("\n✓ order %s is gone. open orders remaining: %d\n", *orderID, len(after))
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}
