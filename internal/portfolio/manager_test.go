package portfolio

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/exchange"

	_ "github.com/Quantix/quantix/internal/strategy/macross"      // register macross
	_ "github.com/Quantix/quantix/internal/strategy/meanreversion" // register meanreversion
)

func TestNewManager_InvalidFrac(t *testing.T) {
	_, err := New(Config{
		TotalCapital: 10000,
		Slots: []config.SlotConfig{
			{Strategy: "macross", Symbol: "BTCUSDT", Interval: "1h", FracCapital: 0.7,
				Params: map[string]any{"Symbol": "BTCUSDT"}},
			{Strategy: "macross", Symbol: "ETHUSDT", Interval: "1h", FracCapital: 0.7,
				Params: map[string]any{"Symbol": "ETHUSDT"}},
		},
	}, config.RiskConfig{MaxPositionPct: 0.1, MaxDrawdownPct: 0.15, MaxSingleLossPct: 0.02},
		nil, nil, nil, zap.NewNop())
	if err == nil {
		t.Error("expected error for total frac > 1.0")
	}
}

func TestNewManager_UnknownStrategy(t *testing.T) {
	_, err := New(Config{
		TotalCapital: 10000,
		Slots: []config.SlotConfig{
			{Strategy: "unknown_strategy", Symbol: "BTCUSDT", Interval: "1h", FracCapital: 0.5},
		},
	}, config.RiskConfig{MaxPositionPct: 0.1, MaxDrawdownPct: 0.15, MaxSingleLossPct: 0.02},
		nil, nil, nil, zap.NewNop())
	if err == nil {
		t.Error("expected error for unknown strategy")
	}
}

func TestNewManager_ValidConfig(t *testing.T) {
	mgr, err := New(Config{
		TotalCapital:   10000,
		FeeRate:        0.001,
		Slippage:       0.0005,
		StatusInterval: time.Second,
		Slots: []config.SlotConfig{
			{Strategy: "macross", Symbol: "BTCUSDT", Interval: "1h", FracCapital: 0.5,
				Params: map[string]any{"Symbol": "BTCUSDT", "FastPeriod": float64(10), "SlowPeriod": float64(30)}},
			{Strategy: "macross", Symbol: "ETHUSDT", Interval: "1h", FracCapital: 0.5,
				Params: map[string]any{"Symbol": "ETHUSDT", "FastPeriod": float64(10), "SlowPeriod": float64(30)}},
		},
	}, config.RiskConfig{MaxPositionPct: 0.1, MaxDrawdownPct: 0.15, MaxSingleLossPct: 0.02},
		nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(mgr.slots) != 2 {
		t.Errorf("expected 2 slots, got %d", len(mgr.slots))
	}
}

func TestManager_RouteKlines(t *testing.T) {
	mgr, err := New(Config{
		TotalCapital:   10000,
		FeeRate:        0.001,
		Slippage:       0.0005,
		StatusInterval: time.Hour,
		Slots: []config.SlotConfig{
			{Strategy: "macross", Symbol: "BTCUSDT", Interval: "1h", FracCapital: 0.6,
				Params: map[string]any{"Symbol": "BTCUSDT", "FastPeriod": float64(10), "SlowPeriod": float64(30)}},
		},
	}, config.RiskConfig{MaxPositionPct: 0.1, MaxDrawdownPct: 0.15, MaxSingleLossPct: 0.02},
		nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	klineCh := make(chan exchange.Kline, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Send a few klines, then cancel
	go func() {
		for i := 0; i < 5; i++ {
			klineCh <- exchange.Kline{
				Symbol: "BTCUSDT", Interval: "1h",
				Close: float64(50000 + i*100), Volume: 1,
			}
		}
	}()

	// Run should return with context cancellation (not panic or deadlock)
	err = mgr.Run(ctx, klineCh)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	summary := mgr.Summary()
	if summary == "" {
		t.Error("empty summary")
	}
	t.Log(summary)
}
