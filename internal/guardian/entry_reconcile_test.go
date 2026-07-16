package guardian

import (
	"testing"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
)

// fakeLiveSource satisfies the livePositionSource subset of position.Syncer that
// the guardian reads from ctx.Extra["position_syncer"].
type fakeLiveSource struct{ long, short *position.StrategyPosition }

func (f fakeLiveSource) GetLong() *position.StrategyPosition  { return f.long }
func (f fakeLiveSource) GetShort() *position.StrategyPosition { return f.short }

func entryCfg() ProtectionConfig {
	return ProtectionConfig{StopMode: StopPct, StopValue: 0.03}
}

func shortPos(symbol string, qty, entry float64) *position.StrategyPosition {
	p := &position.StrategyPosition{}
	p.Symbol = symbol
	p.Side = "SHORT"
	p.Qty = qty
	p.EntryPrice = entry
	return p
}

// TestGuardian_EntryAdoptsExistingInsteadOfReopening is the regression test for
// "顺便帮我开仓 每次重启就重新市价开仓": on restart the EntryGuardian is re-created
// from stored config and unconditionally re-placed the market entry. It must
// instead detect the already-open position (via the syncer) and adopt it.
func TestGuardian_EntryAdoptsExistingInsteadOfReopening(t *testing.T) {
	g := NewEntryGuardian("ETHUSDT", SideShort, 0.1, entryCfg(), 14, zap.NewNop())
	b := &gBroker{}
	// Portfolio also reports the short so resyncPosition doesn't retire it; the
	// entry decision itself keys on the syncer.
	ctx := strategy.NewContext(&gPortfolio{qty: -0.1, avg: 2000}, b, zap.NewNop())
	ctx.Extra["position_syncer"] = fakeLiveSource{short: shortPos("ETHUSDT", 0.1, 2000)}

	g.OnBar(ctx, exchange.Kline{Open: 2000, High: 2005, Low: 1995, Close: 2000})

	if len(b.orders) != 0 {
		t.Fatalf("expected NO entry order (position already open on restart), got %d: %+v", len(b.orders), b.orders)
	}
	if g.Prot() == nil {
		t.Fatal("expected protection armed from the adopted position")
	}
	if g.Prot().Side != SideShort {
		t.Fatalf("expected adopted SHORT protection, got %s", g.Prot().Side)
	}
}

// TestGuardian_EntryWaitsForLiveBar is the regression test for "选了帮我开仓但没下单":
// the entry was placed on the first (warmup) bar, where the broker suppresses the
// order and entryPlaced latches — so it never really opened. The open must wait
// for a live bar (Warmup=false).
func TestGuardian_EntryWaitsForLiveBar(t *testing.T) {
	g := NewEntryGuardian("ETHUSDT", SideShort, 0.1, entryCfg(), 14, zap.NewNop())
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{}, b, zap.NewNop())

	// Warmup replay bar → must NOT place (would be suppressed + latched).
	g.OnBar(ctx, exchange.Kline{Open: 2000, High: 2005, Low: 1995, Close: 2000, Warmup: true})
	if len(b.orders) != 0 {
		t.Fatalf("expected NO entry during warmup, got %d: %+v", len(b.orders), b.orders)
	}
	// First live bar → now it opens.
	g.OnBar(ctx, exchange.Kline{Open: 2000, High: 2005, Low: 1995, Close: 2000, Warmup: false})
	if len(b.orders) != 1 {
		t.Fatalf("expected 1 entry order on the first live bar, got %d", len(b.orders))
	}
	if b.orders[0].PositionSide != strategy.PositionSideShort {
		t.Fatalf("expected OpenShort entry, got %+v", b.orders[0])
	}
}

// TestGuardian_EntryOpensPromptlyOnTickWhenLive verifies the instant-open path:
// once the engine signals it's live (past warmup), the entry is placed on the
// next real-time tick rather than waiting for the next closed bar.
func TestGuardian_EntryOpensPromptlyOnTickWhenLive(t *testing.T) {
	g := NewEntryGuardian("ETHUSDT", SideShort, 0.1, entryCfg(), 14, zap.NewNop())
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{}, b, zap.NewNop())

	// Not live yet → a tick must NOT open (order would be suppressed + latched).
	g.OnTick(ctx, 2000)
	if len(b.orders) != 0 {
		t.Fatalf("expected NO entry before engine is live, got %d", len(b.orders))
	}
	// Engine goes live → next tick opens immediately.
	ctx.Extra["engine_live"] = true
	g.OnTick(ctx, 2000)
	if len(b.orders) != 1 {
		t.Fatalf("expected 1 entry on the first live tick, got %d", len(b.orders))
	}
	if b.orders[0].Type != strategy.OrderMarket {
		t.Fatalf("expected a MARKET entry by default, got %s", b.orders[0].Type)
	}
}

// TestGuardian_LimitEntry verifies the entry is placed as a resting LIMIT order
// at the given price when configured, while the exit stays market.
func TestGuardian_LimitEntry(t *testing.T) {
	g := NewEntryGuardian("ETHUSDT", SideLong, 0.2, entryCfg(), 14, zap.NewNop())
	g.SetLimitEntry(1950)
	b := &gBroker{}
	ctx := strategy.NewContext(&gPortfolio{}, b, zap.NewNop())
	ctx.Extra["engine_live"] = true

	g.OnTick(ctx, 2000)
	if len(b.orders) != 1 {
		t.Fatalf("expected 1 limit entry, got %d", len(b.orders))
	}
	o := b.orders[0]
	if o.Type != strategy.OrderLimit || o.Price != 1950 {
		t.Fatalf("expected LIMIT @1950, got type=%s price=%v", o.Type, o.Price)
	}
	if o.Side != strategy.SideBuy || o.PositionSide != strategy.PositionSideLong {
		t.Fatalf("expected buy/long open, got %+v", o)
	}
}

// TestGuardian_EntryPlacesWhenFlat guards first-run behaviour: with no existing
// position, arm-with-entry must still place the entry once.
func TestGuardian_EntryPlacesWhenFlat(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  any
	}{
		{"no syncer (e.g. backtest)", nil},
		{"empty syncer", fakeLiveSource{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewEntryGuardian("ETHUSDT", SideShort, 0.1, entryCfg(), 14, zap.NewNop())
			b := &gBroker{}
			ctx := strategy.NewContext(&gPortfolio{}, b, zap.NewNop())
			if tc.src != nil {
				ctx.Extra["position_syncer"] = tc.src
			}
			g.OnBar(ctx, exchange.Kline{Open: 2000, High: 2005, Low: 1995, Close: 2000})
			if len(b.orders) != 1 {
				t.Fatalf("expected 1 entry order when flat, got %d: %+v", len(b.orders), b.orders)
			}
			if b.orders[0].PositionSide != strategy.PositionSideShort {
				t.Fatalf("expected OpenShort entry, got %+v", b.orders[0])
			}
		})
	}
}
