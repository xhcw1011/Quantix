// One-off: read-only check of ETHUSDT state on credential-id=3/user-id=4
// (Binance Futures Demo), plus a targeted cancel of one specific algo order
// (set CANCEL_ALGO_ID) left over by an earlier exploratory diagnostic run.
// Read-only unless CANCEL_ALGO_ID is set.
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

	bcfg := cfg.Exchange.Binance
	bcfg.Demo = cred.Demo
	if !bcfg.Demo {
		bcfg.Testnet = cred.Demo
	}
	exchange.ApplyBinanceNetworkMode(bcfg)
	client := binance.NewClient(k, s)

	fmt.Println("--- ETHUSDT positions ---")
	risk, err := client.NewGetPositionRiskService().Do(ctx)
	if err != nil {
		die("position risk: %v", err)
	}
	for _, p := range risk {
		if p.Symbol == "ETHUSDT" {
			fmt.Printf("  posSide=%s amt=%s entry=%s mark=%s pnl=%s\n",
				p.PositionSide, p.PositionAmt, p.EntryPrice, p.MarkPrice, p.UnRealizedProfit)
		}
	}

	fmt.Println("\n--- ETHUSDT open orders ---")
	openOrders, err := client.NewListOpenOrdersService().Symbol("ETHUSDT").Do(ctx)
	if err != nil {
		die("open orders: %v", err)
	}
	fmt.Printf("  %d open orders\n", len(openOrders))
	for _, o := range openOrders {
		fmt.Printf("    id=%d side=%s posSide=%s type=%s qty=%s price=%s\n",
			o.OrderID, o.Side, o.PositionSide, o.Type, o.OrigQuantity, o.Price)
	}

	fmt.Println("\n--- ETHUSDT open algo orders ---")
	algoOrders, err := client.NewListOpenAlgoOrdersService().Do(ctx)
	if err != nil {
		die("open algo orders: %v", err)
	}
	fmt.Printf("  %d open algo orders\n", len(algoOrders))
	for _, o := range algoOrders {
		fmt.Printf("    algoId=%v sym=%v type=%v side=%v posSide=%v trigger=%v status=%v closePos=%v\n",
			o.AlgoId, o.Symbol, o.OrderType, o.Side, o.PositionSide, o.TriggerPrice, o.AlgoStatus, o.ClosePosition)
	}

	if target := os.Getenv("CANCEL_ALGO_ID"); target != "" {
		fmt.Printf("\n--- cancelling algo id=%s ---\n", target)
		var id int64
		fmt.Sscanf(target, "%d", &id)
		if _, err := client.NewCancelAlgoOrderService().AlgoID(id).Do(ctx); err != nil {
			fmt.Printf("  cancel ERROR: %v\n", err)
		} else {
			fmt.Println("  cancelled OK")
		}
	}
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
