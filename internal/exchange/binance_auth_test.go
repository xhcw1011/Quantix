package exchange

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	binance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/common"
	futures "github.com/adshao/go-binance/v2/futures"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

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

// TestBinanceRESTBaseURLRouting locks the per-instance REST host mapping. A wrong
// host here means a "live" order routed to demo (or vice versa) — real money.
func TestBinanceRESTBaseURLRouting(t *testing.T) {
	tests := []struct {
		name          string
		demo, testnet bool
		futures, spot string
	}{
		{"live", false, false, "https://fapi.binance.com", "https://api.binance.com"},
		{"testnet", false, true, "https://testnet.binancefuture.com", "https://testnet.binance.vision"},
		{"demo", true, false, "https://testnet.binancefuture.com", "https://demo-api.binance.com"},
		// demo takes priority when both set (matches ApplyBinanceNetworkMode).
		{"demo+testnet", true, true, "https://testnet.binancefuture.com", "https://demo-api.binance.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BinanceFuturesRESTBaseURL(tt.demo, tt.testnet); got != tt.futures {
				t.Errorf("futures: got %q, want %q", got, tt.futures)
			}
			if got := BinanceSpotRESTBaseURL(tt.demo, tt.testnet); got != tt.spot {
				t.Errorf("spot: got %q, want %q", got, tt.spot)
			}
		})
	}
}

// TestServeBinanceFuturesWSSetsModeBeforeConnect proves the helper sets the
// package-global network flags to the requested mode *before* running connect —
// which is the moment go-binance captures the WS endpoint. This is what lets a
// live and a demo engine dial the right endpoints concurrently.
func TestServeBinanceFuturesWSSetsModeBeforeConnect(t *testing.T) {
	tests := []struct {
		name          string
		demo, testnet bool
		wantDemo      bool
		wantTestnet   bool
	}{
		{"live", false, false, false, false},
		{"testnet", false, true, false, true},
		{"demo", true, false, true, false},
		{"demo priority", true, true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sawDemo, sawTestnet bool
			doneC := make(chan struct{})
			close(doneC)
			_, _, err := ServeBinanceFuturesWS(tt.demo, tt.testnet, zap.NewNop(), func() (chan struct{}, chan struct{}, error) {
				sawDemo = futures.UseDemo
				sawTestnet = futures.UseTestnet
				return doneC, nil, nil
			})
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if sawDemo != tt.wantDemo || sawTestnet != tt.wantTestnet {
				t.Errorf("flags at connect: demo=%v testnet=%v, want demo=%v testnet=%v",
					sawDemo, sawTestnet, tt.wantDemo, tt.wantTestnet)
			}
		})
	}
}

// TestServeBinanceFuturesWSLogsDialTiming proves ServeBinanceFuturesWS logs the
// lock-wait duration, dial duration, and the current active-connection gauge on
// every call — the diagnostic added to investigate the production WS reconnect
// storm (is time being lost waiting for binanceWSModeMu, or in the dial itself?).
func TestServeBinanceFuturesWSLogsDialTiming(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	log := zap.New(core)

	doneC := make(chan struct{})
	close(doneC) // already "closed" so the gauge decrements immediately, no leaked goroutine across tests

	_, _, err := ServeBinanceFuturesWS(false, false, log, func() (chan struct{}, chan struct{}, error) {
		return doneC, make(chan struct{}), nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	entries := logs.FilterMessage("binance futures ws dial").All()
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 'binance futures ws dial' log entry, got %d", len(entries))
	}
	fields := entries[0].ContextMap()
	if _, ok := fields["lock_wait"]; !ok {
		t.Errorf("expected a lock_wait field, got fields %v", fields)
	}
	if _, ok := fields["dial"]; !ok {
		t.Errorf("expected a dial field, got fields %v", fields)
	}
	if _, ok := fields["active_connections"]; !ok {
		t.Errorf("expected an active_connections field, got fields %v", fields)
	}
}

// TestServeBinanceFuturesWSTracksActiveConnectionCount proves the gauge
// increments on a successful connect and decrements once that connection's
// doneC closes — this is what lets the diagnostic log line answer "how many
// concurrent futures WS connections does this process actually have open."
func TestServeBinanceFuturesWSTracksActiveConnectionCount(t *testing.T) {
	before := ActiveBinanceFuturesWSConnections()

	doneC := make(chan struct{})
	_, _, err := ServeBinanceFuturesWS(false, false, zap.NewNop(), func() (chan struct{}, chan struct{}, error) {
		return doneC, make(chan struct{}), nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	if got := ActiveBinanceFuturesWSConnections(); got != before+1 {
		t.Fatalf("after connect: got %d active connections, want %d", got, before+1)
	}

	close(doneC) // simulate the stream ending

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ActiveBinanceFuturesWSConnections() == before {
			return // decremented as expected
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("active connection count did not decrement back to %d after doneC closed, still %d", before, ActiveBinanceFuturesWSConnections())
}

// TestServeBinanceSpotWSSetsModeBeforeConnect is the Spot counterpart.
func TestServeBinanceSpotWSSetsModeBeforeConnect(t *testing.T) {
	tests := []struct {
		name          string
		demo, testnet bool
		wantDemo      bool
		wantTestnet   bool
	}{
		{"live", false, false, false, false},
		{"testnet", false, true, false, true},
		{"demo", true, false, true, false},
		{"demo priority", true, true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sawDemo, sawTestnet bool
			_, _, err := ServeBinanceSpotWS(tt.demo, tt.testnet, func() (chan struct{}, chan struct{}, error) {
				sawDemo = binance.UseDemo
				sawTestnet = binance.UseTestnet
				return nil, nil, nil
			})
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if sawDemo != tt.wantDemo || sawTestnet != tt.wantTestnet {
				t.Errorf("flags at connect: demo=%v testnet=%v, want demo=%v testnet=%v",
					sawDemo, sawTestnet, tt.wantDemo, tt.wantTestnet)
			}
		})
	}
}
