package main

import (
	"fmt"
	"os"

	"github.com/Quantix/quantix/internal/config"
	xbinance "github.com/Quantix/quantix/internal/exchange/binance"
	"github.com/Quantix/quantix/internal/logger"
)

func main() {
	cfg, err := config.Load("config/config.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	log, err := logger.New(cfg.App.Env, cfg.App.LogLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync() //nolint:errcheck

	broker, err := xbinance.NewOrderBrokerWithConfig(
		cfg.Exchange.Binance.APIKey,
		cfg.Exchange.Binance.APISecret,
		cfg.Exchange.Binance.Testnet,
		cfg.Exchange.Binance,
		log,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "AUTH_CHECK_FAILED: %v\n", err)
		os.Exit(2)
	}

	_ = broker
	fmt.Println("AUTH_CHECK_OK")
}
