// close-positions is a one-shot tool that closes all open positions and
// cancels all open orders for a given symbol on the demo Binance Futures
// account. Used during strategy switchover (aistrat → composite).
//
// Usage:
//   QUANTIX_ENCRYPTION_KEY=... close-positions -credential-id 3 -user-id 4 -symbol ETHUSDT
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
	credID := flag.Int("credential-id", 3, "exchange credential id in DB")
	userID := flag.Int("user-id", 4, "user id owning the credential")
	symbol := flag.String("symbol", "ETHUSDT", "symbol to clean up")
	side := flag.String("side", "", "filter PositionSide: LONG, SHORT, or empty for both. Empty also cancels all open orders; non-empty does not touch open orders.")
	cfgPath := flag.String("config", "config/config.yaml", "config file")
	dryRun := flag.Bool("dry-run", false, "print what would be done, don't execute")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil { fail("config: %v", err) }

	log, err := logger.New(cfg.App.Env, cfg.App.LogLevel, "")
	if err != nil { fail("logger: %v", err) }
	defer log.Sync() //nolint:errcheck

	encKey := os.Getenv("QUANTIX_ENCRYPTION_KEY")
	if encKey == "" {
		fail("QUANTIX_ENCRYPTION_KEY env var required")
	}
	keyBytes, err := hex.DecodeString(encKey)
	if err != nil { fail("decode encryption key: %v", err) }
	enc, err := api.NewEncryptor(keyBytes)
	if err != nil { fail("encryptor: %v", err) }

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil { fail("db: %v", err) }
	defer store.Close()

	cred, err := store.GetCredentialByID(ctx, *credID, *userID)
	if err != nil { fail("get credential: %v", err) }
	if cred.Exchange != "binance" {
		fail("only binance supported, got %s", cred.Exchange)
	}

	apiKey, err := enc.Decrypt(cred.APIKey)
	if err != nil { fail("decrypt api_key: %v", err) }
	apiSecret, err := enc.Decrypt(cred.APISecret)
	if err != nil { fail("decrypt api_secret: %v", err) }

	broker, err := binance_futures.NewOrderBrokerWithConfig(apiKey, apiSecret, cred.Demo, cfg.Exchange.Binance, log)
	if err != nil { fail("broker: %v", err) }

	// 1. List positions for our symbol via existing helper.
	positions, err := broker.GetMarginRatios(ctx)
	if err != nil { fail("get positions: %v", err) }

	var ours []exchange.PositionMarginInfo
	for _, p := range positions {
		if p.Symbol != *symbol {
			continue
		}
		if *side != "" && p.PositionSide != *side {
			continue
		}
		ours = append(ours, p)
	}

	if len(ours) == 0 {
		fmt.Printf("[%s] no open positions to close — clean already\n", *symbol)
	} else {
		for _, p := range ours {
			// PositionSide=LONG → close via SELL; SHORT → close via BUY.
			// "" (one-way mode) → ambiguous; default to SELL but flag.
			closeSide := exchange.OrderSideSell
			if p.PositionSide == "SHORT" {
				closeSide = exchange.OrderSideBuy
			}
			fmt.Printf("[%s] CLOSE position: PositionSide=%q size=%f → %s market\n",
				*symbol, p.PositionSide, p.Size, closeSide)
			if *dryRun { continue }

			fill, err := broker.PlaceMarketOrder(ctx, *symbol, closeSide, p.PositionSide, p.Size,
				fmt.Sprintf("close-%d", time.Now().UnixNano()))
			if err != nil {
				fmt.Printf("  ❌ failed: %v\n", err)
				continue
			}
			fmt.Printf("  ✓ filled qty=%f price=%f\n", fill.FilledQty, fill.AvgPrice)
		}
	}

	// 2. Cancel all open orders (staged TPs, etc.).
	// Skip when side filter is set — don't touch the other side's protective orders.
	if *side == "" {
		fmt.Printf("[%s] canceling all open orders\n", *symbol)
		if !*dryRun {
			if err := broker.CancelAllOpenOrders(ctx, *symbol); err != nil {
				fmt.Printf("  ⚠ cancel: %v\n", err)
			} else {
				fmt.Printf("  ✓ all open orders cancelled\n")
			}
		}
	} else {
		fmt.Printf("[%s] -side=%s set → preserving open orders (won't touch the other side's TP/SL)\n", *symbol, *side)
	}

	// 3. Verify.
	positions2, err := broker.GetMarginRatios(ctx)
	if err != nil {
		fmt.Printf("verify: %v\n", err)
		return
	}
	stillOpen := 0
	for _, p := range positions2 {
		if p.Symbol == *symbol {
			fmt.Printf("⚠ position still non-zero: PositionSide=%q size=%f\n", p.PositionSide, p.Size)
			stillOpen++
		}
	}
	if stillOpen == 0 {
		fmt.Printf("[%s] cleanup complete — all positions closed\n", *symbol)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
