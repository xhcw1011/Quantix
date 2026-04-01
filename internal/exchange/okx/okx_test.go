package okx

import (
	"testing"
)

// ─── Symbol conversion helpers ────────────────────────────────────────────────

func TestToOKXSymbol(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"BTCUSDT", "BTC-USDT"},
		{"ETHUSDT", "ETH-USDT"},
		{"ETHBTC", "ETH-BTC"},
		{"SOLUSDT", "SOL-USDT"},
		{"UNKNOWN", "UNKNOWN"}, // no suffix match → passthrough
	}
	for _, tt := range tests {
		if got := toOKXSymbol(tt.in); got != tt.want {
			t.Errorf("toOKXSymbol(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestToOKXSWAPSymbol(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"BTCUSDT", "BTC-USDT-SWAP"},
		{"ETHUSDT", "ETH-USDT-SWAP"},
		{"ETHBTC", "ETH-BTC-SWAP"},
		{"XYZABC", "XYZABC-SWAP"}, // fallback
	}
	for _, tt := range tests {
		if got := toOKXSWAPSymbol(tt.in); got != tt.want {
			t.Errorf("toOKXSWAPSymbol(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestToOKXInterval(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"1m", "1m"},
		{"1h", "1H"},
		{"4h", "4H"},
		{"1d", "1D"},
		{"1w", "1W"},
		{"99x", "99x"}, // unknown → passthrough
	}
	for _, tt := range tests {
		if got := toOKXInterval(tt.in); got != tt.want {
			t.Errorf("toOKXInterval(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ─── parseFloatWarn ───────────────────────────────────────────────────────────

func TestParseFloatWarn(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		var v float64
		if err := parseFloatWarn("1.5", &v); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v != 1.5 {
			t.Fatalf("got %v, want 1.5", v)
		}
	})

	t.Run("empty string returns 0 no error", func(t *testing.T) {
		var v float64
		v = 42 // pre-set to verify it gets zeroed
		if err := parseFloatWarn("", &v); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v != 0 {
			t.Fatalf("got %v, want 0", v)
		}
	})

	t.Run("garbage returns error", func(t *testing.T) {
		var v float64
		v = 42
		if err := parseFloatWarn("abc", &v); err == nil {
			t.Fatal("expected error for garbage input")
		}
		if v != 0 {
			t.Fatalf("got %v, want 0 on error", v)
		}
	})

	t.Run("negative", func(t *testing.T) {
		var v float64
		if err := parseFloatWarn("-0.0034", &v); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v != -0.0034 {
			t.Fatalf("got %v, want -0.0034", v)
		}
	})
}
