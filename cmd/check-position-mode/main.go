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

	// 3b. Multi-Assets Margin mode
	mam, err := client.NewGetMultiAssetModeService().Do(ctx)
	if err != nil {
		fmt.Printf("Multi-Assets Mode: ERROR %v\n", err)
	} else {
		fmt.Printf("Multi-Assets Mode (MAM): %v\n", mam.MultiAssetsMargin)
	}

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

	fmt.Println("\n--- Open orders on ETHUSDT ---")
	openOrders, err := client.NewListOpenOrdersService().Symbol("ETHUSDT").Do(ctx)
	if err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Printf("  %d open orders\n", len(openOrders))
		for _, o := range openOrders {
			fmt.Printf("    id=%d side=%s posSide=%s type=%s qty=%s price=%s reduceOnly=%v\n",
				o.OrderID, o.Side, o.PositionSide, o.Type, o.OrigQuantity, o.Price, o.ReduceOnly)
		}
	}

	fmt.Println("\n--- Account info ---")
	acct, err := client.NewGetAccountService().Do(ctx)
	if err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Printf("  canTrade=%v canDeposit=%v canWithdraw=%v feeTier=%v\n",
			acct.CanTrade, acct.CanDeposit, acct.CanWithdraw, acct.FeeTier)
		fmt.Printf("  totalWalletBalance=%v availableBalance=%v\n",
			acct.TotalWalletBalance, acct.AvailableBalance)
	}

	fmt.Println("\n--- variant H: BUY posSide=SHORT qty=0.001 (close SHORT side) ---")
	if _, err := client.NewCreateOrderService().
		Symbol("ETHUSDT").Side(binance.SideTypeBuy).Type(binance.OrderTypeMarket).
		PositionSide(binance.PositionSideTypeShort).Quantity("0.001").Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Println("  OK")
	}

	fmt.Println("\n--- variant I: BUY posSide=LONG qty=0.012 (add to LONG) ---")
	if _, err := client.NewCreateOrderService().
		Symbol("ETHUSDT").Side(binance.SideTypeBuy).Type(binance.OrderTypeMarket).
		PositionSide(binance.PositionSideTypeLong).Quantity("0.012").Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Println("  OK")
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

	fmt.Println("\n--- variant K: BUY no posSide qty=0.012 (one-way entry test) ---")
	if r, err := client.NewCreateOrderService().
		Symbol("ETHUSDT").Side(binance.SideTypeBuy).Type(binance.OrderTypeMarket).
		Quantity("0.012").Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Printf("  OK — orderID=%d. Cancelling not possible for market — already filled.\n", r.OrderID)
	}

	fmt.Println("\n--- variant J: Re-set hedge mode (toggle reset) ---")
	if err := client.NewChangePositionModeService().DualSide(true).Do(ctx); err != nil {
		fmt.Printf("  Reset ERROR: %v\n", err)
	} else {
		fmt.Println("  Reset OK — re-applied DualSide(true)")
	}

	fmt.Println("\n--- variant L: ChangePositionMode(false) — set to one-way ---")
	if err := client.NewChangePositionModeService().DualSide(false).Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Println("  OK — succeeded setting to one-way (means current was already one-way)")
	}
	fmt.Println("--- variant J2: retry SELL posSide=LONG qty=0.012 after reset ---")
	if _, err := client.NewCreateOrderService().
		Symbol("ETHUSDT").Side(binance.SideTypeSell).Type(binance.OrderTypeMarket).
		PositionSide(binance.PositionSideTypeLong).Quantity("0.012").Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Println("  OK")
	}
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
