// One-off: check + force HEDGE mode on Binance Futures Demo for credential-id=3/user-id=4.
package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	binance "github.com/adshao/go-binance/v2/futures"

	"github.com/Quantix/quantix/internal/api"
	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/logger"
)

func main() {
	cfg, err := config.Load("/opt/quantix/config/config.yaml")
	if err != nil {
		die("config: %v", err)
	}
	log, _ := logger.New("production", "info", "")
	defer log.Sync()

	encKey := os.Getenv("QUANTIX_ENCRYPTION_KEY")
	if encKey == "" {
		die("QUANTIX_ENCRYPTION_KEY required")
	}
	keyBytes, err := hex.DecodeString(encKey)
	if err != nil {
		die("hex decode: %v", err)
	}
	enc, err := api.NewEncryptor(keyBytes)
	if err != nil {
		die("encryptor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		die("db: %v", err)
	}
	defer store.Close()

	cred, err := store.GetCredentialByID(ctx, 3, 4)
	if err != nil {
		die("get cred: %v", err)
	}
	k, err := enc.Decrypt(cred.APIKey)
	if err != nil {
		die("decrypt key: %v", err)
	}
	s, err := enc.Decrypt(cred.APISecret)
	if err != nil {
		die("decrypt secret: %v", err)
	}

	// Apply the same network-mode resolution the engine uses so we hit
	// demo-api.binance.com (not testnet.binancefuture.com) for demo credentials.
	bcfg := cfg.Exchange.Binance
	bcfg.Demo = cred.Demo
	if !bcfg.Demo {
		bcfg.Testnet = cred.Demo
	}
	exchange.ApplyBinanceNetworkMode(bcfg)
	client := binance.NewClient(k, s)

	// 1. Check current mode
	res, err := client.NewGetPositionModeService().Do(ctx)
	if err != nil {
		die("get position mode: %v", err)
	}
	fmt.Printf("Current dualSidePosition (hedge): %v\n", res.DualSidePosition)

	// 2. If not hedge, switch to hedge
	if !res.DualSidePosition {
		fmt.Println("Switching to HEDGE mode...")
		err := client.NewChangePositionModeService().DualSide(true).Do(ctx)
		if err != nil {
			die("change position mode: %v", err)
		}
		fmt.Println("✓ Switched to HEDGE")
	} else {
		fmt.Println("Already HEDGE mode — error -4061 was something else.")
	}

	// 3. Verify
	res2, err := client.NewGetPositionModeService().Do(ctx)
	if err != nil {
		die("verify: %v", err)
	}
	fmt.Printf("After: dualSidePosition = %v\n", res2.DualSidePosition)

	// 4. List actual positions on this endpoint
	fmt.Println("\n--- Positions on this endpoint ---")
	risk, err := client.NewGetPositionRiskService().Do(ctx)
	if err != nil {
		die("get position risk: %v", err)
	}
	for _, p := range risk {
		if p.Symbol == "ETHUSDT" {
			fmt.Printf("  symbol=%s posSide=%s amt=%s entry=%s mark=%s pnl=%s\n",
				p.Symbol, p.PositionSide, p.PositionAmt, p.EntryPrice, p.MarkPrice, p.UnRealizedProfit)
		}
	}

	// 5. Try variants to figure out what Binance accepts
	fmt.Println("\n--- variant A: SELL with posSide=LONG (engine's way) ---")
	if _, err := client.NewCreateOrderService().
		Symbol("ETHUSDT").Side(binance.SideTypeSell).Type(binance.OrderTypeMarket).
		PositionSide(binance.PositionSideTypeLong).Quantity("0.001").Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Println("  OK")
	}

	fmt.Println("\n--- variant B: SELL no posSide ---")
	if _, err := client.NewCreateOrderService().
		Symbol("ETHUSDT").Side(binance.SideTypeSell).Type(binance.OrderTypeMarket).
		Quantity("0.001").Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Println("  OK — account effectively in one-way mode")
	}

	fmt.Println("\n--- variant C: SELL posSide=BOTH ---")
	if _, err := client.NewCreateOrderService().
		Symbol("ETHUSDT").Side(binance.SideTypeSell).Type(binance.OrderTypeMarket).
		PositionSide(binance.PositionSideTypeBoth).Quantity("0.001").Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Println("  OK — account in one-way mode")
	}

	fmt.Println("\n--- variant D: SELL posSide=LONG + ReduceOnly ---")
	if _, err := client.NewCreateOrderService().
		Symbol("ETHUSDT").Side(binance.SideTypeSell).Type(binance.OrderTypeMarket).
		PositionSide(binance.PositionSideTypeLong).ReduceOnly(true).Quantity("0.001").Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Println("  OK")
	}

	fmt.Println("\n--- variant E: SELL posSide=BOTH + ReduceOnly qty=0.001 ---")
	if _, err := client.NewCreateOrderService().
		Symbol("ETHUSDT").Side(binance.SideTypeSell).Type(binance.OrderTypeMarket).
		PositionSide(binance.PositionSideTypeBoth).ReduceOnly(true).Quantity("0.001").Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Println("  OK — closing tiny qty succeeded")
	}

	fmt.Println("\n--- variant G: SELL no posSide qty=0.012 (above $20 notional) ---")
	if _, err := client.NewCreateOrderService().
		Symbol("ETHUSDT").Side(binance.SideTypeSell).Type(binance.OrderTypeMarket).
		Quantity("0.012").Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Println("  OK — order placed (one-way style)")
	}

	fmt.Println("\n--- variant F: SELL no posSide + ReduceOnly qty=0.001 ---")
	if _, err := client.NewCreateOrderService().
		Symbol("ETHUSDT").Side(binance.SideTypeSell).Type(binance.OrderTypeMarket).
		ReduceOnly(true).Quantity("0.001").Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Println("  OK — close worked without posSide")
	}
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
