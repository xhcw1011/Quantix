package aistrat

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/strategymock"
)

// ─── manageGrid flow tests ──────────────────────────────────────────────────
// Covers layer placement decisions: distance trigger, MaxLayers cap (including
// qty-derived count for sync reset case), regime filter, adverse_momentum guard.

// newGridTestStrategy returns an AIStrategy ready to exercise manageGrid.
// All config values use grid defaults from code.
func newGridTestStrategy(bars int, midPrice float64) (*AIStrategy, *strategymock.FakeBroker, *strategy.Context) {
	broker := &strategymock.FakeBroker{}
	portfolio := &strategymock.MockPortfolioView{}
	ctx := strategy.NewContext(portfolio, broker, zap.NewNop())

	s := &AIStrategy{
		cfg: Config{
			Symbol:           "ETHUSDT",
			PrimaryInterval:  "5m",
			ATRPeriod:        14,
			GridMaxLayers:    3,
			GridQtyRatio:     0.5,
			GridLayerSpacing: 10.0,
			GridMinTPDist:    5.0,
			GridMaxTPDist:    12.0,
			GridFixedSLDist:  50.0,
			GridTPPct:        0.004,
			BBPeriod:         20,
			BBStdDev:         2.0,
			Leverage:         10,
			MinHoldBars:      3,
			MinSLDistPct:     0.008,
		},
		log:            zap.NewNop(),
		barsByInterval: map[string][]exchange.Kline{"5m": stableBars(bars, midPrice)},
		lastRegime:     RegimeRange,
		lastTrendDir:   0, // neutral — allows both sides
	}
	return s, broker, ctx
}

// Helper: strategy with bars already stable at currentPrice (no recent shock).
// posState.entryPrice is independent of bar prices — simulates "price settled
// at current level after prior drop/rise".
func newGridTestStrategyAt(currentPrice float64) (*AIStrategy, *strategymock.FakeBroker, *strategy.Context) {
	s, broker, ctx := newGridTestStrategy(21, currentPrice)
	return s, broker, ctx
}

func TestManageGrid_LongPlacesLayer1OnPriceDrop(t *testing.T) {
	s, broker, ctx := newGridTestStrategyAt(2339.0)
	// Bars stable at 2339 (avoids adverse_momentum shock). posState entry 2350.

	p := &posState{
		side:       "LONG",
		mode:       modeRange,
		entryPrice: 2350.0,
		initQty:    0.1,
		remainQty:  0.1,
		filled:     true,
	}
	s.longPos = p

	bar := s.barsByInterval["5m"][len(s.barsByInterval["5m"])-1]
	s.manageGrid(ctx, bar, p, &s.longPos)

	require.Len(t, broker.Placed, 1, "layer 1 should be placed")
	got := broker.Placed[0]
	assert.Equal(t, strategy.SideBuy, got.Side)
	assert.Equal(t, strategy.PositionSideLong, got.PositionSide)
	assert.Equal(t, strategy.OrderLimit, got.Type)
	assert.InDelta(t, 0.05, got.Qty, 0.001, "layer qty = 0.1 * 0.5 ratio")
	assert.Equal(t, 2339.0, got.Price)
	assert.Len(t, p.gridOrders, 1, "gridOrders array updated")
}

func TestManageGrid_LongDoesNotPlaceWhenPriceAboveLayer1(t *testing.T) {
	s, broker, ctx := newGridTestStrategyAt(2345.0) // only -$5 from entry

	p := &posState{
		side: "LONG", mode: modeRange,
		entryPrice: 2350.0,
		initQty: 0.1, remainQty: 0.1, filled: true,
	}
	s.longPos = p

	bar := s.barsByInterval["5m"][len(s.barsByInterval["5m"])-1]
	s.manageGrid(ctx, bar, p, &s.longPos)

	assert.Len(t, broker.Placed, 0, "no layer — price not far enough from entry")
}

func TestManageGrid_LongPlacesLayer3_ButNotLayer4(t *testing.T) {
	s, broker, ctx := newGridTestStrategyAt(2319.0) // past layer 3 trigger (2320)

	// posState simulates "layer 1 and 2 already filled via qty" (sync reset case).
	p := &posState{
		side: "LONG", mode: modeRange,
		entryPrice: 2350.0,
		initQty:   0.1,
		remainQty: 0.2, // = 0.1 + 2×0.05 (two layers filled historically)
		filled:    true,
	}
	s.longPos = p

	bar := s.barsByInterval["5m"][len(s.barsByInterval["5m"])-1]
	s.manageGrid(ctx, bar, p, &s.longPos)

	// qty-derived filled count = (0.2 - 0.1) / 0.05 = 2. Next layer = 3.
	// Layer 3 distance = 10 * 3 = 30. refPrice(2350) - 30 = 2320. Price 2319 ≤ 2320 ✓
	require.Len(t, broker.Placed, 1, "layer 3 should be placed (2 filled via qty + 1 new)")
	assert.Equal(t, 2319.0, broker.Placed[0].Price)
}

func TestManageGrid_BlocksWhenMaxLayersReachedViaQty(t *testing.T) {
	s, broker, ctx := newGridTestStrategyAt(2310.0) // would trigger layer 4 if not capped

	// All 3 layers already filled via qty (post sync reset).
	// Exact scenario that caused 4/23 0.385 qty runaway before fix.
	p := &posState{
		side: "LONG", mode: modeRange,
		entryPrice: 2350.0,
		initQty:   0.1,
		remainQty: 0.25, // = 0.1 + 3×0.05 (3 layers filled)
		filled:    true,
	}
	s.longPos = p

	bar := s.barsByInterval["5m"][len(s.barsByInterval["5m"])-1]
	s.manageGrid(ctx, bar, p, &s.longPos)

	assert.Len(t, broker.Placed, 0,
		"MaxLayers=3 reached via qty — no more layers placed")
}

func TestManageGrid_BlocksWhenRegimeNotRange(t *testing.T) {
	s, broker, ctx := newGridTestStrategyAt(2339.0)
	s.lastRegime = RegimeStrongTrend

	p := &posState{
		side: "LONG", mode: modeRange,
		entryPrice: 2350.0, initQty: 0.1, remainQty: 0.1, filled: true,
	}
	s.longPos = p

	bar := s.barsByInterval["5m"][len(s.barsByInterval["5m"])-1]
	s.manageGrid(ctx, bar, p, &s.longPos)

	assert.Len(t, broker.Placed, 0, "regime != RANGE → no layer added")
}

func TestManageGrid_BlocksLongWhenTrendDirOpposing(t *testing.T) {
	s, broker, ctx := newGridTestStrategyAt(2339.0)
	s.lastTrendDir = -1 // bearish drift opposes LONG

	p := &posState{
		side: "LONG", mode: modeRange,
		entryPrice: 2350.0, initQty: 0.1, remainQty: 0.1, filled: true,
	}
	s.longPos = p

	bar := s.barsByInterval["5m"][len(s.barsByInterval["5m"])-1]
	s.manageGrid(ctx, bar, p, &s.longPos)

	assert.Len(t, broker.Placed, 0, "bearish trend_dir → no LONG layer add")
}

func TestManageGrid_BlocksOnAdverseMomentum(t *testing.T) {
	s, broker, ctx := newGridTestStrategy(19, 2350.0)
	// Add a big down bar that triggers single_bar_shock
	s.barsByInterval["5m"] = append(s.barsByInterval["5m"], makeBar(2350, 2351, 2335, 2338))

	p := &posState{
		side: "LONG", mode: modeRange,
		entryPrice: 2350.0, initQty: 0.1, remainQty: 0.1, filled: true,
	}
	s.longPos = p

	bar := s.barsByInterval["5m"][len(s.barsByInterval["5m"])-1]
	s.manageGrid(ctx, bar, p, &s.longPos)

	assert.Len(t, broker.Placed, 0, "急跌 bar → adverse_momentum 拦 layer")
}

func TestManageGrid_ShortPlacesLayerOnPriceRise(t *testing.T) {
	s, broker, ctx := newGridTestStrategyAt(2361.0) // past SHORT layer 1 trigger (2360)

	p := &posState{
		side: "SHORT", mode: modeRange,
		entryPrice: 2350.0, initQty: 0.1, remainQty: 0.1, filled: true,
	}
	s.shortPos = p

	bar := s.barsByInterval["5m"][len(s.barsByInterval["5m"])-1]
	s.manageGrid(ctx, bar, p, &s.shortPos)

	require.Len(t, broker.Placed, 1)
	assert.Equal(t, strategy.SideSell, broker.Placed[0].Side)
	assert.Equal(t, strategy.PositionSideShort, broker.Placed[0].PositionSide)
	assert.Equal(t, 2361.0, broker.Placed[0].Price)
}

// ─── tickManage flow tests ──────────────────────────────────────────────────
// Covers fixed_sl trigger path and TP hit path.

func TestTickManage_FixedSLLongTriggersMarketClose(t *testing.T) {
	s, broker, ctx := newGridTestStrategy(20, 2350.0)

	p := &posState{
		side: "LONG", mode: modeRange,
		entryPrice: 2350.0,
		initQty: 0.1, remainQty: 0.1,
		barsHeld: 10, // > MinHoldBars
		filled:   true,
	}
	s.longPos = p

	// Price dropped to 2299 (51 below entry, past fixed_sl=50)
	s.tickManage(ctx, 2299.0, p, &s.longPos)

	require.Len(t, broker.Placed, 1, "fixed_sl should trigger close order")
	got := broker.Placed[0]
	assert.Equal(t, strategy.SideSell, got.Side, "LONG close = SELL")
	assert.Equal(t, strategy.PositionSideLong, got.PositionSide)
	assert.InDelta(t, 0.1, got.Qty, 0.001)
	// Note: placeCloseOrder uses market by default (Type default = "" which
	// maps to market in closePos's useMarket=true path). Verify in req.
	assert.Nil(t, s.longPos, "position state cleared after close")
}

func TestTickManage_FixedSLShortTriggersMarketClose(t *testing.T) {
	s, broker, ctx := newGridTestStrategy(20, 2350.0)

	p := &posState{
		side: "SHORT", mode: modeRange,
		entryPrice: 2350.0,
		initQty: 0.1, remainQty: 0.1,
		barsHeld: 10,
		filled:   true,
	}
	s.shortPos = p

	// Price rose to 2401 (51 above entry, past fixed_sl=50)
	s.tickManage(ctx, 2401.0, p, &s.shortPos)

	require.Len(t, broker.Placed, 1, "fixed_sl should trigger close order")
	got := broker.Placed[0]
	assert.Equal(t, strategy.SideBuy, got.Side, "SHORT close = BUY")
	assert.Equal(t, strategy.PositionSideShort, got.PositionSide)
	assert.Nil(t, s.shortPos, "position state cleared after close")
}

func TestTickManage_PriceWithinFixedSLDoesNotClose(t *testing.T) {
	s, broker, ctx := newGridTestStrategy(20, 2350.0)

	p := &posState{
		side: "LONG", mode: modeRange,
		entryPrice: 2350.0,
		initQty: 0.1, remainQty: 0.1,
		barsHeld: 10, filled: true,
	}
	s.longPos = p

	// Price at 2310 ($40 below, still within fixed_sl=50)
	s.tickManage(ctx, 2310.0, p, &s.longPos)

	assert.Len(t, broker.Placed, 0, "within fixed_sl — no close")
	assert.NotNil(t, s.longPos, "position still alive")
}

func TestTickManage_TPTriggersCloseForLong(t *testing.T) {
	s, broker, ctx := newGridTestStrategy(20, 2350.0)

	p := &posState{
		side: "LONG", mode: modeRange,
		entryPrice: 2350.0,
		takeProfit: 2358.0, // $8 TP
		initQty: 0.1, remainQty: 0.1,
		barsHeld: 10, filled: true,
	}
	s.longPos = p

	// Price at 2360 (above TP)
	s.tickManage(ctx, 2360.0, p, &s.longPos)

	require.Len(t, broker.Placed, 1, "TP hit should trigger close")
	assert.Equal(t, strategy.SideSell, broker.Placed[0].Side)
	assert.Nil(t, s.longPos)
}

func TestTickManage_MinHoldBarsGuard(t *testing.T) {
	s, broker, ctx := newGridTestStrategy(20, 2350.0)

	p := &posState{
		side: "LONG", mode: modeRange,
		entryPrice: 2350.0,
		initQty: 0.1, remainQty: 0.1,
		barsHeld: 1, // < MinHoldBars=3
		filled:   true,
	}
	s.longPos = p

	// Price dropped past fixed_sl threshold, but too early
	s.tickManage(ctx, 2290.0, p, &s.longPos)

	assert.Len(t, broker.Placed, 0, "MinHoldBars not met — skip tick management")
	assert.NotNil(t, s.longPos)
}
