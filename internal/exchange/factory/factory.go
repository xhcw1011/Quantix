// Package factory constructs exchange clients based on configuration.
package factory

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/exchange"
	xbinance "github.com/Quantix/quantix/internal/exchange/binance"
	"github.com/Quantix/quantix/internal/exchange/binance_futures"
	"github.com/Quantix/quantix/internal/exchange/bybit"
	"github.com/Quantix/quantix/internal/exchange/okx"
)

// NewRESTClient creates a RESTClient for the configured exchange.
// Active values: "binance" (default / empty), "okx", "bybit".
// For Binance, market_type "futures" uses the USDM Futures endpoints; otherwise Spot.
func NewRESTClient(cfg config.ExchangeConfig, log *zap.Logger) (exchange.RESTClient, error) {
	switch cfg.Active {
	case "okx":
		return okx.NewRESTClient(log, cfg.OKX.MarketType), nil
	case "bybit":
		return bybit.NewRESTClient(log), nil
	case "binance", "":
		if cfg.Binance.MarketType == "futures" {
			return exchange.NewBinanceFuturesRESTClient(
				cfg.Binance.APIKey,
				cfg.Binance.APISecret,
				cfg.Binance.Testnet,
				cfg.Binance,
				log,
			)
		}
		return exchange.NewBinanceRESTClientWithConfig(
			cfg.Binance.APIKey,
			cfg.Binance.APISecret,
			cfg.Binance.Testnet,
			cfg.Binance,
			log,
		)
	default:
		return nil, fmt.Errorf("unknown exchange %q; supported: binance, okx, bybit", cfg.Active)
	}
}

// NewOrderClient creates an OrderClient for order execution on the configured exchange.
// For Binance Spot:    cfg.Active="binance", cfg.Binance.MarketType="" or "spot"
// For Binance Futures: cfg.Active="binance", cfg.Binance.MarketType="futures"
// For OKX SWAP/Spot:  cfg.Active="okx"
func NewOrderClient(cfg config.ExchangeConfig, log *zap.Logger) (exchange.OrderClient, error) {
	switch cfg.Active {
	case "okx":
		return okx.NewOrderBroker(
			cfg.OKX.APIKey, cfg.OKX.APISecret, cfg.OKX.Passphrase,
			cfg.OKX.Demo, cfg.OKX.MarketType, log)
	case "binance", "":
		if cfg.Binance.MarketType == "futures" {
			return binance_futures.NewOrderBrokerWithConfig(
				cfg.Binance.APIKey, cfg.Binance.APISecret, cfg.Binance.Testnet, cfg.Binance, log)
		}
		return xbinance.NewOrderBrokerWithConfig(
			cfg.Binance.APIKey, cfg.Binance.APISecret, cfg.Binance.Testnet, cfg.Binance, log)
	case "bybit":
		return nil, fmt.Errorf("bybit order execution is not yet implemented; use binance or okx for live/paper trading")
	default:
		return nil, fmt.Errorf("unsupported exchange for order client: %q; supported: binance, okx", cfg.Active)
	}
}

// NewWSClient creates a WSClient for the configured exchange.
// For Binance, market_type "futures" uses the USDM Futures WS streams; otherwise Spot.
func NewWSClient(cfg config.ExchangeConfig, wsCfg config.WSConfig, log *zap.Logger) (exchange.WSClient, error) {
	switch cfg.Active {
	case "okx":
		return okx.NewWSClient(log, cfg.OKX.MarketType), nil
	case "bybit":
		return bybit.NewWSClient(log), nil
	case "binance", "":
		if cfg.Binance.MarketType == "futures" {
			return exchange.NewBinanceFuturesWSClient(cfg.Binance, wsCfg, log), nil
		}
		return exchange.NewBinanceWSClient(cfg.Binance, wsCfg, log), nil
	default:
		return nil, fmt.Errorf("unknown exchange %q; supported: binance, okx, bybit", cfg.Active)
	}
}
