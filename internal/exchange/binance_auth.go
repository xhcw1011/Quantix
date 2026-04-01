package exchange

import (
	"fmt"
	"os"
	"strings"

	binance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/common"
	futures "github.com/adshao/go-binance/v2/futures"

	"github.com/Quantix/quantix/internal/config"
)

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
