package exchange

import (
	"os"
	"path/filepath"
	"testing"

	binance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/common"

	"github.com/Quantix/quantix/internal/config"
)

func TestResolveKeyType(t *testing.T) {
	tests := []struct {
		name   string
		cfg    config.BinanceConfig
		expect string
	}{
		{"empty defaults to HMAC", config.BinanceConfig{}, common.KeyTypeHmac},
		{"explicit HMAC", config.BinanceConfig{KeyType: "HMAC"}, common.KeyTypeHmac},
		{"explicit RSA", config.BinanceConfig{KeyType: "RSA"}, common.KeyTypeRsa},
		{"explicit ED25519", config.BinanceConfig{KeyType: "ED25519"}, common.KeyTypeEd25519},
		{"case insensitive", config.BinanceConfig{KeyType: "rsa"}, common.KeyTypeRsa},
		{"auto-detect RSA from path", config.BinanceConfig{PrivateKeyPath: "/some/key.pem"}, common.KeyTypeRsa},
		{"unknown falls back to HMAC", config.BinanceConfig{KeyType: "UNKNOWN"}, common.KeyTypeHmac},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveKeyType(tt.cfg)
			if got != tt.expect {
				t.Errorf("resolveKeyType() = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestConfigureBinanceAuth_HMAC_NoOp(t *testing.T) {
	client := binance.NewClient("key", "secret")
	err := ConfigureBinanceAuth(client, config.BinanceConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.KeyType != common.KeyTypeHmac {
		t.Errorf("KeyType should remain HMAC, got %q", client.KeyType)
	}
	if client.SecretKey != "secret" {
		t.Errorf("SecretKey should remain unchanged, got %q", client.SecretKey)
	}
}

func TestConfigureBinanceAuth_RSA_LoadsPEM(t *testing.T) {
	// Create a temp PEM file
	dir := t.TempDir()
	pemPath := filepath.Join(dir, "test.pem")
	pemContent := "-----BEGIN RSA PRIVATE KEY-----\nMIIBogIB...\n-----END RSA PRIVATE KEY-----"
	if err := os.WriteFile(pemPath, []byte(pemContent), 0600); err != nil {
		t.Fatal(err)
	}

	client := binance.NewClient("key", "secret")
	err := ConfigureBinanceAuth(client, config.BinanceConfig{
		KeyType:        "RSA",
		PrivateKeyPath: pemPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.KeyType != common.KeyTypeRsa {
		t.Errorf("KeyType = %q, want %q", client.KeyType, common.KeyTypeRsa)
	}
	if client.SecretKey != pemContent {
		t.Errorf("SecretKey should be PEM content")
	}
}

func TestConfigureBinanceAuth_MissingFile(t *testing.T) {
	client := binance.NewClient("key", "secret")
	err := ConfigureBinanceAuth(client, config.BinanceConfig{
		KeyType:        "RSA",
		PrivateKeyPath: "/nonexistent/key.pem",
	})
	if err == nil {
		t.Fatal("expected error for missing PEM file")
	}
}

func TestConfigureBinanceAuth_InvalidPEM(t *testing.T) {
	dir := t.TempDir()
	pemPath := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(pemPath, []byte("not a pem file"), 0600); err != nil {
		t.Fatal(err)
	}

	client := binance.NewClient("key", "secret")
	err := ConfigureBinanceAuth(client, config.BinanceConfig{
		KeyType:        "RSA",
		PrivateKeyPath: pemPath,
	})
	if err == nil {
		t.Fatal("expected error for invalid PEM content")
	}
}

func TestConfigureBinanceAuth_MissingPath(t *testing.T) {
	client := binance.NewClient("key", "secret")
	err := ConfigureBinanceAuth(client, config.BinanceConfig{
		KeyType: "RSA",
	})
	if err == nil {
		t.Fatal("expected error when RSA specified without private_key_path")
	}
}
