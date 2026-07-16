package exchange

import (
	"fmt"
	"os"
	"strings"
	"sync"

	binance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/common"
	futures "github.com/adshao/go-binance/v2/futures"

	"github.com/Quantix/quantix/internal/config"
)

// Binance REST base URLs, mirrored from go-binance's package constants so we can
// pin each client's BaseURL per-instance instead of relying on the shared global
// network flags. This lets a live engine and a demo engine coexist.
const (
	binanceFuturesRESTMainURL = "https://fapi.binance.com"
	binanceFuturesRESTDemoURL = "https://testnet.binancefuture.com" // futures demo == testnet endpoint

	binanceSpotRESTMainURL    = "https://api.binance.com"
	binanceSpotRESTTestnetURL = "https://testnet.binance.vision"
	binanceSpotRESTDemoURL    = "https://demo-api.binance.com" // spot demo != testnet
)

// binanceWSModeMu serializes the "set global network mode → dial WS" critical
// section. go-binance resolves the WS endpoint from the package-global
// UseTestnet/UseDemo flags at the moment Ws*Serve is called (the endpoint string
// is captured synchronously, before the dial goroutine runs). Those flags are
// shared across every Futures client in the process, so two engines on different
// networks — one live, one demo — would otherwise race: engine A sets demo,
// engine B sets live, then engine A dials and connects to the wrong endpoint.
// Holding this lock across "set flags + Ws*Serve" makes each subscription bind to
// the correct URL; the established socket keeps that endpoint for its lifetime.
var binanceWSModeMu sync.Mutex

// ServeBinanceFuturesWS sets the Futures package network mode to (demo/testnet)
// and dials a WS stream via connect() while holding a process-wide lock, so the
// stream binds to the right endpoint even when live and demo engines run
// concurrently. connect must perform the actual futures.Ws*Serve call and return
// its (doneC, stopC, err) — the endpoint is captured inside that call.
func ServeBinanceFuturesWS(demo, testnet bool, connect func() (chan struct{}, chan struct{}, error)) (chan struct{}, chan struct{}, error) {
	binanceWSModeMu.Lock()
	defer binanceWSModeMu.Unlock()
	if demo {
		futures.UseTestnet = false
		futures.UseDemo = true
	} else if testnet {
		futures.UseTestnet = true
		futures.UseDemo = false
	} else {
		futures.UseTestnet = false
		futures.UseDemo = false
	}
	return connect()
}

// BinanceFuturesRESTBaseURL returns the REST base URL for the given network mode,
// so callers can pin client.BaseURL per-instance.
func BinanceFuturesRESTBaseURL(demo, testnet bool) string {
	if demo || testnet {
		return binanceFuturesRESTDemoURL
	}
	return binanceFuturesRESTMainURL
}

// ServeBinanceSpotWS is the Spot-package counterpart of ServeBinanceFuturesWS:
// it sets binance.UseTestnet/UseDemo and dials a Spot WS stream via connect()
// under the same process-wide lock, so live and demo Spot streams bind to the
// correct endpoints when run concurrently.
func ServeBinanceSpotWS(demo, testnet bool, connect func() (chan struct{}, chan struct{}, error)) (chan struct{}, chan struct{}, error) {
	binanceWSModeMu.Lock()
	defer binanceWSModeMu.Unlock()
	if demo {
		binance.UseTestnet = false
		binance.UseDemo = true
	} else if testnet {
		binance.UseTestnet = true
		binance.UseDemo = false
	} else {
		binance.UseTestnet = false
		binance.UseDemo = false
	}
	return connect()
}

// BinanceSpotRESTBaseURL returns the Spot REST base URL for the given network
// mode, so callers can pin client.BaseURL per-instance. Unlike Futures, Spot's
// demo and testnet are distinct hosts.
func BinanceSpotRESTBaseURL(demo, testnet bool) string {
	if demo {
		return binanceSpotRESTDemoURL
	}
	if testnet {
		return binanceSpotRESTTestnetURL
	}
	return binanceSpotRESTMainURL
}

// ApplyBinanceNetworkMode sets the global UseTestnet/UseDemo flags for both
// Spot and Futures packages based on the config. Only one of testnet/demo
// should be true; if both are set, demo takes priority.
func ApplyBinanceNetworkMode(cfg config.BinanceConfig) {
	if cfg.Demo {
		binance.UseTestnet = false
		binance.UseDemo = true
		futures.UseTestnet = false
		futures.UseDemo = true
	} else if cfg.Testnet {
		binance.UseTestnet = true
		binance.UseDemo = false
		futures.UseTestnet = true
		futures.UseDemo = false
	} else {
		binance.UseTestnet = false
		binance.UseDemo = false
		futures.UseTestnet = false
		futures.UseDemo = false
	}
}

// ConfigureBinanceAuth sets KeyType and SecretKey on a Binance Spot client
// based on the BinanceConfig. For RSA/ED25519, it reads the PEM file and
// sets client.SecretKey to the PEM contents. For HMAC (default), no changes.
func ConfigureBinanceAuth(client *binance.Client, cfg config.BinanceConfig) error {
	kt := resolveKeyType(cfg)
	if kt == common.KeyTypeHmac {
		return nil
	}
	pem, err := loadPEM(cfg.PrivateKeyPath)
	if err != nil {
		return err
	}
	client.KeyType = kt
	client.SecretKey = pem
	return nil
}

// ConfigureBinanceFuturesAuth sets KeyType and SecretKey on a Binance Futures client.
func ConfigureBinanceFuturesAuth(client *futures.Client, cfg config.BinanceConfig) error {
	kt := resolveKeyType(cfg)
	if kt == common.KeyTypeHmac {
		return nil
	}
	pem, err := loadPEM(cfg.PrivateKeyPath)
	if err != nil {
		return err
	}
	client.KeyType = kt
	client.SecretKey = pem
	return nil
}

// resolveKeyType normalises the configured key type. Returns HMAC when empty
// or when private_key_path is not set. Auto-detects RSA if private_key_path
// is set but key_type is empty.
func resolveKeyType(cfg config.BinanceConfig) string {
	kt := strings.ToUpper(strings.TrimSpace(cfg.KeyType))
	switch kt {
	case common.KeyTypeRsa, common.KeyTypeEd25519:
		return kt
	case "", common.KeyTypeHmac:
		if cfg.PrivateKeyPath != "" {
			return common.KeyTypeRsa // auto-detect: PEM path present → RSA
		}
		return common.KeyTypeHmac
	default:
		return common.KeyTypeHmac
	}
}

// loadPEM reads a PEM file and returns its contents as a string.
func loadPEM(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("private_key_path is required for non-HMAC key types")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading private key PEM file %q: %w", path, err)
	}
	s := strings.TrimSpace(string(data))
	if !strings.Contains(s, "PRIVATE KEY") {
		return "", fmt.Errorf("file %q does not look like a PEM private key", path)
	}
	return s, nil
}
