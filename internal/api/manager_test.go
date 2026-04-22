package api

import (
	"os"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	// Register at least one strategy so the registry gate passes.
	_ "github.com/Quantix/quantix/internal/strategy/macross"
)

func newTestManager(liveEnabled bool) *EngineManager {
	cfg := &config.Config{
		Live: config.LiveConfig{Enabled: liveEnabled},
	}
	return NewEngineManager(nil, nil, config.SMTPConfig{}, nil, cfg, nil, zap.NewNop())
}

func TestStart_LiveBlockedWithoutEnvConfirm(t *testing.T) {
	// Ensure env var is unset.
	os.Unsetenv("QUANTIX_LIVE_CONFIRM")

	m := newTestManager(true)
	req := StartRequest{
		Mode:        "live",
		ConfirmLive: true,
		StrategyID:  "macross",
		Symbol:      "BTCUSDT",
		Interval:    "1h",
	}

	_, err := m.Start(1, req)
	if err == nil {
		t.Fatal("expected error when QUANTIX_LIVE_CONFIRM is unset, got nil")
	}
	if !strings.Contains(err.Error(), "QUANTIX_LIVE_CONFIRM") {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
}

func TestStart_LiveAllowedWithEnvConfirm(t *testing.T) {
	// Set env var — Start will pass the env gate. With nil store, the
	// credential lookup panics, so we recover and verify that the error
	// is NOT the env gate (i.e. we got past it).
	t.Setenv("QUANTIX_LIVE_CONFIRM", "true")

	m := newTestManager(true)
	req := StartRequest{
		Mode:         "live",
		ConfirmLive:  true,
		StrategyID:   "macross",
		Symbol:       "BTCUSDT",
		Interval:     "1h",
		CredentialID: 999,
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				// Panic from nil store means we passed all pre-flight gates.
				t.Logf("passed env gate, panicked downstream (expected): %v", r)
			}
		}()
		_, err := m.Start(1, req)
		if err != nil && strings.Contains(err.Error(), "QUANTIX_LIVE_CONFIRM") {
			t.Fatalf("should have passed env gate but got: %s", err.Error())
		}
	}()
}

func TestStart_PaperUnaffectedByEnvConfirm(t *testing.T) {
	// Env var unset — paper mode must not be blocked.
	os.Unsetenv("QUANTIX_LIVE_CONFIRM")

	m := newTestManager(false) // live disabled, paper should still work
	req := StartRequest{
		Mode:         "paper",
		StrategyID:   "macross",
		Symbol:       "BTCUSDT",
		Interval:     "1h",
		CredentialID: 999,
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				// Panic from nil store means we passed all pre-flight gates.
				t.Logf("passed pre-flight gates, panicked downstream (expected): %v", r)
			}
		}()
		_, err := m.Start(1, req)
		if err != nil && strings.Contains(err.Error(), "QUANTIX_LIVE_CONFIRM") {
			t.Fatalf("paper mode should bypass env gate but got: %s", err.Error())
		}
	}()
}
