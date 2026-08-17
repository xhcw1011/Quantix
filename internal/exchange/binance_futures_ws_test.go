package exchange

import (
	"testing"
	"time"
)

// TestEffectiveStaleTimeout reproduces a 2026-08-17 real incident: the demo
// (testnet) BTCUSDT 15m kline WS stream got stuck in an endless 30s
// teardown/reconnect loop. Root cause confirmed via an isolated diagnostic —
// a fresh, standalone connection to Binance's demo/testnet kline endpoint
// received ZERO messages (not even intermediate, non-final ticks) over a
// clean 3-minute window, while the same test against production received
// 337 messages in the same window. Testnet has far sparser trading activity
// than production, so its kline WS can legitimately go minutes with no
// pushes at all even on a perfectly healthy connection — the 30s stale
// timeout (correctly tight for production, where a 30s silence really does
// mean a dead connection) tears down a healthy-but-quiet testnet connection
// before it ever gets a chance, and every reconnect is just as quiet as the
// one it replaced, so it never escapes the loop.
func TestEffectiveStaleTimeout(t *testing.T) {
	cases := []struct {
		name            string
		base            time.Duration
		demo, testnet   bool
		want            time.Duration
	}{
		{"production: unchanged", 30 * time.Second, false, false, 30 * time.Second},
		{"demo: widened to the minimum", 30 * time.Second, true, false, 5 * time.Minute},
		{"testnet: widened to the minimum", 30 * time.Second, false, true, 5 * time.Minute},
		{"demo with an already-generous configured base: base wins", 10 * time.Minute, true, false, 10 * time.Minute},
		{"production with a configured base below the demo minimum: base is NOT widened (production only widens for demo/testnet)", 10 * time.Second, false, false, 10 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := effectiveStaleTimeout(c.base, c.demo, c.testnet)
			if got != c.want {
				t.Errorf("effectiveStaleTimeout(%v, demo=%v, testnet=%v) = %v, want %v", c.base, c.demo, c.testnet, got, c.want)
			}
		})
	}
}
