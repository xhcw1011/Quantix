package aistrat

import (
	"math"
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
)

// ─── weightedAvgEntry ────────────────────────────────────────────────────────

func TestWeightedAvgEntry_NoLayers(t *testing.T) {
	p := &posState{
		entryPrice: 2350.0,
		initQty:    0.1,
		remainQty:  0.1,
	}
	got := weightedAvgEntry(p)
	if got != 2350.0 {
		t.Errorf("no layers: want 2350.0, got %.4f", got)
	}
}

func TestWeightedAvgEntry_TrendModeNoGridOrders(t *testing.T) {
	// Trend positions have no gridOrders — should return base entry
	p := &posState{
		entryPrice: 2400.5,
		initQty:    0.05,
		remainQty:  0.05,
		gridOrders: nil,
	}
	got := weightedAvgEntry(p)
	if got != 2400.5 {
		t.Errorf("trend mode: want 2400.5, got %.4f", got)
	}
}

func TestWeightedAvgEntry_OneFilledLayer(t *testing.T) {
	// base 0.1 @2350, one layer 0.05 @2340 → avg = 2346.67
	p := &posState{
		entryPrice: 2350.0,
		initQty:    0.1,
		remainQty:  0.15,
		gridOrders: []*gridOrder{
			{entryPrice: 2340.0, qty: 0.05, filled: true},
		},
	}
	got := weightedAvgEntry(p)
	expected := (0.1*2350.0 + 0.05*2340.0) / 0.15
	if math.Abs(got-expected) > 0.01 {
		t.Errorf("one layer: want %.4f, got %.4f", expected, got)
	}
}

func TestWeightedAvgEntry_PendingLayersIgnored(t *testing.T) {
	// Pending (unfilled) layers don't contribute to avg entry
	p := &posState{
		entryPrice: 2350.0,
		initQty:    0.1,
		remainQty:  0.1, // hasn't grown because pending
		gridOrders: []*gridOrder{
			{entryPrice: 2340.0, qty: 0.05, filled: false}, // pending
		},
	}
	got := weightedAvgEntry(p)
	if got != 2350.0 {
		t.Errorf("pending layer: want 2350.0 (pending ignored), got %.4f", got)
	}
}

func TestWeightedAvgEntry_MultipleLayers(t *testing.T) {
	// base 0.1 @2400, layers at 2390, 2380, 2370, all filled
	p := &posState{
		entryPrice: 2400.0,
		initQty:    0.1,
		remainQty:  0.25,
		gridOrders: []*gridOrder{
			{entryPrice: 2390.0, qty: 0.05, filled: true},
			{entryPrice: 2380.0, qty: 0.05, filled: true},
			{entryPrice: 2370.0, qty: 0.05, filled: true},
		},
	}
	got := weightedAvgEntry(p)
	expected := (0.1*2400.0 + 0.05*2390.0 + 0.05*2380.0 + 0.05*2370.0) / 0.25
	if math.Abs(got-expected) > 0.01 {
		t.Errorf("multi-layer: want %.4f, got %.4f", expected, got)
	}
}

func TestWeightedAvgEntry_ZeroQtyFallback(t *testing.T) {
	// Defensive: zero qty should return base entry without panic
	p := &posState{
		entryPrice: 2350.0,
		initQty:    0.0, // edge case
		remainQty:  0.0,
	}
	got := weightedAvgEntry(p)
	if got != 2350.0 {
		t.Errorf("zero qty: want 2350.0, got %.4f", got)
	}
}

// ─── computeGridTP ───────────────────────────────────────────────────────────

func newTestStrategy() *AIStrategy {
	return &AIStrategy{
		cfg: Config{
			GridMinTPDist:   5.0,
			GridMaxTPDist:   12.0,
			GridTPPct:       0.004,
			GridFixedSLDist: 50.0,
		},
		log: zap.NewNop(),
	}
}

func TestComputeGridTP_LongBBMiddleInRange(t *testing.T) {
	s := newTestStrategy()
	s.lastBBMiddle = 2358.0 // entry+8, within [5, 12]
	got := s.computeGridTP("LONG", 2350.0)
	if got != 2358.0 {
		t.Errorf("LONG BB in range: want 2358.0, got %.4f", got)
	}
}

func TestComputeGridTP_LongBBMiddleTooFar(t *testing.T) {
	s := newTestStrategy()
	s.lastBBMiddle = 2370.0 // entry+20, exceeds maxTP=12
	got := s.computeGridTP("LONG", 2350.0)
	if got != 2362.0 { // clamped to entry+12
		t.Errorf("LONG BB far: want 2362.0 (capped), got %.4f", got)
	}
}

func TestComputeGridTP_LongBBMiddleTooClose(t *testing.T) {
	s := newTestStrategy()
	s.lastBBMiddle = 2353.0 // entry+3, below minTP=5
	got := s.computeGridTP("LONG", 2350.0)
	if got != 2355.0 { // floored to entry+5
		t.Errorf("LONG BB close: want 2355.0 (floored), got %.4f", got)
	}
}

func TestComputeGridTP_LongBBMiddleBelowEntry(t *testing.T) {
	s := newTestStrategy()
	s.lastBBMiddle = 2340.0 // below entry → fallback
	got := s.computeGridTP("LONG", 2350.0)
	// fallback = entry × (1+0.004) = 2350 × 1.004 = 2359.4, in [2355, 2362]
	expected := 2359.4
	if math.Abs(got-expected) > 0.01 {
		t.Errorf("LONG BB below: want %.4f (fallback), got %.4f", expected, got)
	}
}

func TestComputeGridTP_LongBBMiddleZero(t *testing.T) {
	// After recovery before reversion runs, lastBBMiddle=0
	s := newTestStrategy()
	s.lastBBMiddle = 0.0
	got := s.computeGridTP("LONG", 2350.0)
	expected := 2359.4 // fallback
	if math.Abs(got-expected) > 0.01 {
		t.Errorf("LONG BB zero: want %.4f (fallback), got %.4f", expected, got)
	}
}

func TestComputeGridTP_ShortBBMiddleInRange(t *testing.T) {
	s := newTestStrategy()
	s.lastBBMiddle = 2342.0 // entry-8, within [5, 12]
	got := s.computeGridTP("SHORT", 2350.0)
	if got != 2342.0 {
		t.Errorf("SHORT BB in range: want 2342.0, got %.4f", got)
	}
}

func TestComputeGridTP_ShortBBMiddleTooFar(t *testing.T) {
	s := newTestStrategy()
	s.lastBBMiddle = 2330.0 // entry-20, exceeds maxTP=12
	got := s.computeGridTP("SHORT", 2350.0)
	if got != 2338.0 { // clamped to entry-12
		t.Errorf("SHORT BB far: want 2338.0 (capped), got %.4f", got)
	}
}

func TestComputeGridTP_ShortBBMiddleTooClose(t *testing.T) {
	s := newTestStrategy()
	s.lastBBMiddle = 2347.0 // entry-3, below minTP=5
	got := s.computeGridTP("SHORT", 2350.0)
	if got != 2345.0 { // floored to entry-5
		t.Errorf("SHORT BB close: want 2345.0 (floored), got %.4f", got)
	}
}

func TestComputeGridTP_LongAfterLayerAvg(t *testing.T) {
	// Simulates grid after layer fill: avg entry lower than base
	// base @2400, layer @2390 filled → avg 2395
	// BB middle ~2400 → TP = 2400 (in range of avg+5 to avg+12)
	s := newTestStrategy()
	s.lastBBMiddle = 2400.0
	got := s.computeGridTP("LONG", 2395.0) // avg entry
	// BB middle = 2400 = avg + 5, exactly minTP → within range
	if got != 2400.0 {
		t.Errorf("LONG avg TP: want 2400.0, got %.4f", got)
	}
}

// ─── isAdverseMomentum ───────────────────────────────────────────────────────

func makeBar(open, high, low, close_ float64) exchange.Kline {
	return exchange.Kline{Open: open, High: high, Low: low, Close: close_}
}

func newAdverseTestStrategy(bars []exchange.Kline) *AIStrategy {
	s := &AIStrategy{
		cfg: Config{
			PrimaryInterval: "5m",
			ATRPeriod:       14,
		},
		log:            zap.NewNop(),
		barsByInterval: map[string][]exchange.Kline{"5m": bars},
	}
	return s
}

// Build bars where each is "stable" with small range for baseline ATR
func stableBars(count int, midPrice float64) []exchange.Kline {
	out := make([]exchange.Kline, count)
	for i := 0; i < count; i++ {
		out[i] = makeBar(midPrice, midPrice+1, midPrice-1, midPrice)
	}
	return out
}

func TestIsAdverseMomentum_StableMarket(t *testing.T) {
	// Stable market: no shock triggers for either direction
	bars := stableBars(20, 2350.0)
	s := newAdverseTestStrategy(bars)

	blocked, reason := s.isAdverseMomentum("LONG")
	if blocked {
		t.Errorf("stable LONG: want no block, got blocked reason=%s", reason)
	}
	blocked, reason = s.isAdverseMomentum("SHORT")
	if blocked {
		t.Errorf("stable SHORT: want no block, got blocked reason=%s", reason)
	}
}

func TestIsAdverseMomentum_LongBlockedOnBigDropBar(t *testing.T) {
	// 20 stable bars + 1 big down bar (> 1.5 ATR)
	// ATR from stable bars ≈ 2 (high-low range); big drop = close-open < -3
	bars := stableBars(20, 2350.0)
	// Big drop: open 2350, close 2340 (net -10, which is 5× ATR)
	bars = append(bars, makeBar(2350.0, 2352.0, 2335.0, 2340.0))
	s := newAdverseTestStrategy(bars)

	blocked, reason := s.isAdverseMomentum("LONG")
	if !blocked {
		t.Errorf("LONG big drop: want blocked, got pass")
	}
	if reason != "single_bar_shock" {
		t.Errorf("LONG big drop: want reason single_bar_shock, got %s", reason)
	}
}

func TestIsAdverseMomentum_ShortBlockedOnBigRiseBar(t *testing.T) {
	bars := stableBars(20, 2350.0)
	// Big rise: open 2350, close 2360 (+10)
	bars = append(bars, makeBar(2350.0, 2365.0, 2348.0, 2360.0))
	s := newAdverseTestStrategy(bars)

	blocked, reason := s.isAdverseMomentum("SHORT")
	if !blocked {
		t.Errorf("SHORT big rise: want blocked, got pass")
	}
	if reason != "single_bar_shock" {
		t.Errorf("SHORT big rise: want reason single_bar_shock, got %s", reason)
	}
}

func TestIsAdverseMomentum_LongBlockedOn3BarCumDrop(t *testing.T) {
	// 3 down bars each dropping close by 3 (barMove=-3, below single_bar
	// threshold at 1.5×ATR≈-3.6). cum3 = close[-1] - close[-3] = -6,
	// exceeding 2×ATR≈-4.8. Isolates cum3_shock trigger.
	bars := stableBars(17, 2350.0)
	bars = append(bars, makeBar(2350.0, 2350.5, 2346.5, 2347.0)) // -3
	bars = append(bars, makeBar(2347.0, 2347.5, 2343.5, 2344.0)) // -3
	bars = append(bars, makeBar(2344.0, 2344.5, 2340.5, 2341.0)) // -3
	s := newAdverseTestStrategy(bars)

	blocked, reason := s.isAdverseMomentum("LONG")
	if !blocked {
		t.Errorf("LONG cum3 drop: want blocked, got pass (atr=%.2f cum3=%.2f)",
			s.calcATR(), bars[19].Close-bars[17].Close)
	}
	if reason != "single_bar_shock" && reason != "cum3_shock" {
		t.Errorf("LONG cum3: want single_bar_shock or cum3_shock, got %s", reason)
	}
}

func TestIsAdverseMomentum_RangeBreakNotTriggered(t *testing.T) {
	// Small new low shouldn't trigger (range_break was removed)
	// This test ensures the removal is effective
	bars := stableBars(20, 2350.0)
	// Mild new low: low=2347 (slightly below previous 2349 low), close=2348.5
	bars = append(bars, makeBar(2350.0, 2350.5, 2347.0, 2348.5))
	s := newAdverseTestStrategy(bars)

	blocked, reason := s.isAdverseMomentum("LONG")
	if blocked {
		t.Errorf("mild new low: want no block (range_break removed), got blocked reason=%s", reason)
	}
}

func TestIsAdverseMomentum_InsufficientBars(t *testing.T) {
	// Less than 4 bars: should return false (not enough data)
	bars := stableBars(3, 2350.0)
	s := newAdverseTestStrategy(bars)

	blocked, _ := s.isAdverseMomentum("LONG")
	if blocked {
		t.Errorf("insufficient bars: want no block, got blocked")
	}
}

func TestIsAdverseMomentum_SymmetryLongVsShort(t *testing.T) {
	// Big rise should block SHORT but not LONG
	bars := stableBars(20, 2350.0)
	bars = append(bars, makeBar(2350.0, 2365.0, 2348.0, 2360.0))
	s := newAdverseTestStrategy(bars)

	longBlocked, _ := s.isAdverseMomentum("LONG")
	if longBlocked {
		t.Errorf("LONG on rise: want no block (favorable for LONG)")
	}
	shortBlocked, _ := s.isAdverseMomentum("SHORT")
	if !shortBlocked {
		t.Errorf("SHORT on rise: want blocked (adverse for SHORT)")
	}
}
