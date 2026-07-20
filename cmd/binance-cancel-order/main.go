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
	"strings"
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

	// Best-effort display of the regular open orders. NOTE: this endpoint does NOT
	// include conditional/algo orders (STOP_MARKET placed via the algo API lives in
	// a different ID space, e.g. 1000000...), so a protective stop may be absent
	// here even while resting — which is exactly why we do NOT gate the cancel on it.
	if before, err := broker.ListOpenOrders(ctx, *symbol); err == nil {
		fmt.Printf("\nregular open orders (%d, excludes conditional/algo):\n", len(before))
		for _, o := range before {
			fmt.Printf("  %-16s %-4s %-5s %-12s qty=%.4g stop=%.2f status=%s\n",
				o.ExchangeID, o.Side, o.PositionSide, o.Type, o.Qty, o.StopPrice, o.Status)
		}
	}

	// Attempt the cancel directly. CancelOrder tries a normal cancel first, then an
	// algo cancel — so it covers both regular and conditional/algo orders.
	fmt.Printf("\ncancelling %s (normal → algo fallback) ...\n", *orderID)
	err = broker.CancelOrder(ctx, *symbol, *orderID)
	if err == nil {
		fmt.Printf("\n✓ order %s cancelled.\n", *orderID)
		return
	}
	if isAlreadyGone(err) {
		fmt.Printf("\n✓ order %s not found on the exchange (already cancelled/filled) — nothing to do.\n", *orderID)
		return
	}
	die("cancel failed: %v", err)
}

// isAlreadyGone reports whether the cancel error means the order no longer exists
// on the exchange (Binance -2011 "Unknown order sent." / -2013 "Order does not
// exist."), i.e. there is nothing left to cancel.
func isAlreadyGone(err error) bool {
	s := err.Error()
	return strings.Contains(s, "-2011") || strings.Contains(s, "-2013") ||
		strings.Contains(s, "Unknown order") || strings.Contains(s, "does not exist")
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}
