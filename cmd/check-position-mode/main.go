// One-off: check + force HEDGE mode on Binance Futures Demo for credential-id=3/user-id=4.
package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	binance "github.com/adshao/go-binance/v2/futures"
	delivery "github.com/adshao/go-binance/v2/delivery"

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

	// 3c. Check COIN-margined (CM) Futures account — since 2026-04-10 it must
	// stay in sync with UM. Stale CM positions/orders block UM order placement.
	fmt.Println("\n--- COIN-MARGINED (CM) Futures account ---")
	delivery.UseTestnet = cred.Demo
	cmClient := delivery.NewClient(k, s)
	cmMode, err := cmClient.NewGetPositionModeService().Do(ctx)
	if err != nil {
		fmt.Printf("  CM GetPositionMode ERROR: %v\n", err)
	} else {
		fmt.Printf("  CM DualSidePosition: %v\n", cmMode.DualSidePosition)
	}
	cmOpenOrders, err := cmClient.NewListOpenOrdersService().Do(ctx)
	if err != nil {
		fmt.Printf("  CM open orders ERROR: %v\n", err)
	} else {
		fmt.Printf("  CM open orders: %d\n", len(cmOpenOrders))
		for _, o := range cmOpenOrders {
			fmt.Printf("    sym=%s id=%d side=%s posSide=%s type=%s qty=%s price=%s\n",
				o.Symbol, o.OrderID, o.Side, o.PositionSide, o.Type, o.OrigQuantity, o.Price)
		}
	}
	cmRisk, err := cmClient.NewGetPositionRiskService().Do(ctx)
	if err != nil {
		fmt.Printf("  CM positionRisk ERROR: %v\n", err)
	} else {
		nonZero := 0
		for _, p := range cmRisk {
			if p.PositionAmt != "0" && p.PositionAmt != "0.000" {
				fmt.Printf("    sym=%s posSide=%s amt=%s entry=%s\n",
					p.Symbol, p.PositionSide, p.PositionAmt, p.EntryPrice)
				nonZero++
			}
		}
		fmt.Printf("  CM non-zero positions: %d\n", nonZero)
	}

	fmt.Println("\n--- Open ALGO orders ---")
	algoOrders, err := client.NewListOpenAlgoOrdersService().Do(ctx)
	if err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Printf("  %d open algo orders\n", len(algoOrders))
		for _, o := range algoOrders {
			fmt.Printf("    id=%v sym=%v orderType=%v side=%v posSide=%v triggerPrice=%v status=%v closePos=%v\n",
				o.AlgoId, o.Symbol, o.OrderType, o.Side, o.PositionSide, o.TriggerPrice, o.AlgoStatus, o.ClosePosition)
		}
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

	fmt.Println("\n--- variant M: ALGO STOP_MARKET + closePosition=true (force close LONG) ---")
	// Trigger price above current → fires immediately on next tick.
	// Uses /fapi/v1/algo/futures/newOrder which has different validation logic.
	if _, err := client.NewCreateAlgoOrderService().
		AlgoType(binance.OrderAlgoTypeConditional).
		Symbol("ETHUSDT").Side(binance.SideTypeSell).Type(binance.AlgoOrderTypeStopMarket).
		PositionSide(binance.PositionSideTypeLong).ClosePosition(true).
		TriggerPrice("2010").
		WorkingType(binance.WorkingTypeMarkPrice).
		Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Println("  OK — STOP_MARKET algo close placed")
	}

	fmt.Println("\n--- Algo order HISTORY (recent) ---")
	allAlgo, err := client.NewListAllAlgoOrdersService().Symbol("ETHUSDT").Do(ctx)
	if err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Printf("  %d algo orders in history\n", len(allAlgo))
		for _, o := range allAlgo {
			fmt.Printf("    id=%v type=%v side=%v posSide=%v trig=%v status=%v actualOrderId=%v\n",
				o.AlgoId, o.OrderType, o.Side, o.PositionSide, o.TriggerPrice, o.AlgoStatus, o.ActualOrderId)
		}
	}

	fmt.Println("\n--- variant P: cancel any stale algo orders first ---")
	algoOpen, _ := client.NewListOpenAlgoOrdersService().Do(ctx)
	for _, o := range algoOpen {
		fmt.Printf("  Cancelling algo id=%d\n", o.AlgoId)
		_, err := client.NewCancelAlgoOrderService().AlgoID(o.AlgoId).Do(ctx)
		if err != nil {
			fmt.Printf("    cancel ERROR: %v\n", err)
		}
	}

	fmt.Println("\n--- variant Q: STOP_MARKET trigger ABOVE current mark — fires immediately for SELL ---")
	// For SELL STOP_MARKET, Binance fires when mark price <= triggerPrice on a
	// fall. But if triggerPrice > current mark already (e.g. trigger=$2050,
	// mark=$1979), the condition "mark touched/crossed trigger from above" is
	// already satisfied per most exchanges. Test it.
	if r, err := client.NewCreateAlgoOrderService().
		AlgoType(binance.OrderAlgoTypeConditional).
		Symbol("ETHUSDT").Side(binance.SideTypeSell).Type(binance.AlgoOrderTypeStopMarket).
		PositionSide(binance.PositionSideTypeLong).ClosePosition(true).
		TriggerPrice("2050").
		WorkingType(binance.WorkingTypeMarkPrice).
		Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Printf("  OK: id=%v\n", r.AlgoId)
	}

	fmt.Println("\n--- variant O: ALGO TAKE_PROFIT_MARKET — trigger above current, fires now ---")
	// SELL TAKE_PROFIT_MARKET fires when price >= stopPrice. With stopPrice
	// slightly above current (e.g. $2021 vs current ~$2020), any upward tick
	// triggers immediate market close.
	if r, err := client.NewCreateAlgoOrderService().
		AlgoType(binance.OrderAlgoTypeConditional).
		Symbol("ETHUSDT").Side(binance.SideTypeSell).Type(binance.AlgoOrderTypeTakeProfitMarket).
		PositionSide(binance.PositionSideTypeLong).ClosePosition(true).
		TriggerPrice("2021").
		WorkingType(binance.WorkingTypeMarkPrice).
		Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Printf("  OK — order placed: %v\n", r)
	}

	fmt.Println("\n--- variant N: ALGO STOP_MARKET + closePosition=true (no posSide) ---")
	if _, err := client.NewCreateAlgoOrderService().
		AlgoType(binance.OrderAlgoTypeConditional).
		Symbol("ETHUSDT").Side(binance.SideTypeSell).Type(binance.AlgoOrderTypeStopMarket).
		ClosePosition(true).TriggerPrice("2010").
		WorkingType(binance.WorkingTypeMarkPrice).
		Do(ctx); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		fmt.Println("  OK — algo close placed (one-way style)")
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
