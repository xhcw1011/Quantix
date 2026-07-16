package api

import (
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

// Live mode requires the per-engine confirm_live flag — an early gate that runs
// before any credential/DB access.
func TestStart_LiveRequiresConfirmLive(t *testing.T) {
	m := newTestManager(true)
	_, err := m.Start(1, StartRequest{
		Mode: "live", ConfirmLive: false,
		StrategyID: "macross", Symbol: "BTCUSDT", Interval: "1h",
	})
	if err == nil || !strings.Contains(err.Error(), "confirm_live") {
		t.Fatalf("expected confirm_live error, got: %v", err)
	}
}

// With confirm_live set, Start passes the early pre-flight gates and proceeds to
// credential loading (which panics on the nil test store). The real-money master
// switch (live + a non-testnet/demo credential requires the user's
// live_trading_enabled preference) is enforced after the credential loads and is
// covered by integration, not this unit test.
func TestStart_LiveWithConfirmPassesEarlyGates(t *testing.T) {
	m := newTestManager(true)
	defer func() { _ = recover() }() // nil store panics downstream — that's fine here
	_, err := m.Start(1, StartRequest{
		Mode: "live", ConfirmLive: true,
		StrategyID: "macross", Symbol: "BTCUSDT", Interval: "1h", CredentialID: 999,
	})
	if err != nil && strings.Contains(err.Error(), "confirm_live") {
		t.Fatalf("should have passed the confirm_live gate, got: %s", err.Error())
	}
}

// Paper mode is never blocked by the live gates.
func TestStart_PaperBypassesLiveGate(t *testing.T) {
	m := newTestManager(false)
	defer func() { _ = recover() }()
	_, err := m.Start(1, StartRequest{
		Mode:       "paper",
		StrategyID: "macross", Symbol: "BTCUSDT", Interval: "1h", CredentialID: 999,
	})
	if err != nil && strings.Contains(err.Error(), "confirm_live") {
		t.Fatalf("paper mode should bypass live gates, got: %s", err.Error())
	}
}
