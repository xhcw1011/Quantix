package macross

import (
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
)

// mutablePortfolio lets a test flip "do we have a position" to simulate a
// limit entry's fill appearing between bars — onBarSimple (long-only mode)
// checks Portfolio.Position() each bar instead of tracking hasLong/hasShort
// the way hedge mode does.
type mutablePortfolio struct {
	hasPosition bool
	avgPrice    float64
	qty         float64
}

func (p *mutablePortfolio) Cash() float64 { return 10_000 }
func (p *mutablePortfolio) Position(_ string) (qty, avgPrice float64, ok bool) {
	if !p.hasPosition {
		return 0, 0, false
	}
	return p.qty, p.avgPrice, true
}
func (p *mutablePortfolio) Equity(_ map[string]float64) float64 { return 10_000 }

// simpleEntryBars triggers exactly the golden cross opening a long (at
// close=152) and no further bars -- see entryBars for why that matters.
var simpleEntryBars = []float64{
	150, 149.6, 149.2, 148.8, 148.4, 148, // downtrend warmup
	152, // sharp uptrend -- golden cross
}

func newSimpleMACross(cfg Config, portfolio strategy.PortfolioView) (*MACross, *strategy.Context, *stubBroker) {
	cfg.Symbol = "BTCUSDT"
	if cfg.FastPeriod == 0 {
		cfg.FastPeriod = 3
	}
	if cfg.SlowPeriod == 0 {
		cfg.SlowPeriod = 6
	}
	log, _ := zap.NewDevelopment()
	broker := &stubBroker{}
	ctx := strategy.NewContext(portfolio, broker, log)
	return New(cfg), ctx, broker
}

func lastOrder(broker *stubBroker) strategy.OrderRequest {
	return broker.orders[len(broker.orders)-1]
}

// --- Hedge mode (EnableShort=true) ---

// entryBars is the prefix of downtrendThenSharpRecovery that triggers exactly
// the death cross opening a SHORT (at close=90) and no further bars —
// anything beyond this would start advancing/timing out the pending entry
// before a test gets to assert on the just-placed order.
var entryBars = downtrendThenSharpRecovery[:8]

func TestMACross_LimitEntry_PlacesLimitWithFavorableOffset(t *testing.T) {
	m, ctx, broker := newHedgeMACross(Config{
		EntryOrderType:      "limit",
		EntryLimitOffsetPct: 0.001, // 0.1%
		EntryTimeoutBars:    3,
		TrendFilterMin:      0,
	})
	feedBarsNoAutoFill(m, ctx, broker, "BTCUSDT", entryBars)

	req := lastOrder(broker)
	if req.Type != strategy.OrderLimit {
		t.Fatalf("expected a LIMIT order, got %s", req.Type)
	}
	if !req.MakerOnly {
		t.Error("expected MakerOnly=true so a would-cross order is rejected, not silently filled as taker")
	}
	// Death cross opens a SHORT at bar close 90 (see entryBars). Favorable for
	// a short = ABOVE the close.
	wantPrice := 90 * 1.001
	if req.Price < wantPrice-1e-9 || req.Price > wantPrice+1e-9 {
		t.Errorf("expected limit price %.4f (close + offset), got %.4f", wantPrice, req.Price)
	}
	if !m.pending.Active() {
		t.Error("expected a pending entry to be tracked after placing the limit order")
	}
}

func TestMACross_LimitEntry_PendingBlocksFurtherSignalEvaluation(t *testing.T) {
	m, ctx, broker := newHedgeMACross(Config{
		EntryOrderType:      "limit",
		EntryLimitOffsetPct: 0.001,
		EntryTimeoutBars:    5,
		TrendFilterMin:      0,
	})
	feedBarsNoAutoFill(m, ctx, broker, "BTCUSDT", entryBars)
	countAfterEntry := len(broker.orders)

	// Feed one more bar with no fill simulated -- the pending entry should
	// swallow this bar entirely (no fresh signal evaluation, no new order).
	m.OnBar(ctx, hedgeBar("BTCUSDT", 8, 95))

	if len(broker.orders) != countAfterEntry {
		t.Fatalf("expected no new orders while a limit entry is pending, got %d new",
			len(broker.orders)-countAfterEntry)
	}
	if m.pending.Bars != 1 {
		t.Errorf("expected the pending entry's bar counter to advance to 1, got %d", m.pending.Bars)
	}
}

func TestMACross_LimitEntry_TimesOutToMarketFallback(t *testing.T) {
	m, ctx, broker := newHedgeMACross(Config{
		EntryOrderType:      "limit",
		EntryLimitOffsetPct: 0.001,
		EntryTimeoutBars:    2,
		TrendFilterMin:      0,
	})
	feedBarsNoAutoFill(m, ctx, broker, "BTCUSDT", entryBars)
	countAfterEntry := len(broker.orders)

	// Two more unfilled bars should reach the timeout and fall back to market.
	m.OnBar(ctx, hedgeBar("BTCUSDT", 8, 95))
	m.OnBar(ctx, hedgeBar("BTCUSDT", 9, 100))

	if len(broker.cancels) != 1 {
		t.Fatalf("expected the stale limit order to be cancelled once, got %d cancels", len(broker.cancels))
	}
	if len(broker.orders) != countAfterEntry+1 {
		t.Fatalf("expected exactly one fallback market order, got %d new orders",
			len(broker.orders)-countAfterEntry)
	}
	fallback := lastOrder(broker)
	if fallback.Type != strategy.OrderMarket {
		t.Errorf("expected the timeout fallback to be a MARKET order, got %s", fallback.Type)
	}
	if m.pending.Active() {
		t.Error("expected pending entry to be cleared after falling back to market")
	}
}

func TestMACross_LimitEntry_InstantRejectFallsBackImmediately(t *testing.T) {
	m, ctx, broker := newHedgeMACross(Config{
		EntryOrderType:      "limit",
		EntryLimitOffsetPct: 0.001,
		EntryTimeoutBars:    3,
		TrendFilterMin:      0,
	})
	broker.rejectNextID = true
	feedBarsNoAutoFill(m, ctx, broker, "BTCUSDT", entryBars)

	if len(broker.orders) != 2 {
		t.Fatalf("expected the rejected limit + an immediate market fallback, got %d orders", len(broker.orders))
	}
	if broker.orders[0].Type != strategy.OrderLimit || broker.orders[1].Type != strategy.OrderMarket {
		t.Errorf("expected [LIMIT, MARKET], got [%s, %s]", broker.orders[0].Type, broker.orders[1].Type)
	}
	if m.pending.Active() {
		t.Error("expected no pending entry to be tracked after an instant reject")
	}
	if len(broker.cancels) != 0 {
		t.Errorf("an instantly-rejected order was never resting, expected no cancel, got %d", len(broker.cancels))
	}
}

func TestMACross_LimitEntry_FillClearsPending(t *testing.T) {
	m, ctx, broker := newHedgeMACross(Config{
		EntryOrderType:      "limit",
		EntryLimitOffsetPct: 0.001,
		EntryTimeoutBars:    5,
		TrendFilterMin:      0,
	})
	feedBarsNoAutoFill(m, ctx, broker, "BTCUSDT", entryBars)
	req := lastOrder(broker)

	// Simulate the resting limit order actually filling.
	m.OnFill(ctx, strategy.Fill{
		Symbol: "BTCUSDT", Side: req.Side, PositionSide: req.PositionSide,
		Qty: 1, Price: req.Price,
	})
	countAfterFill := len(broker.orders)

	m.OnBar(ctx, hedgeBar("BTCUSDT", 8, 95))

	if m.pending.Active() {
		t.Error("expected pending entry to clear once the position appears via OnFill")
	}
	// The bar that just cleared the pending entry should stay quiet (no signal
	// re-evaluation on the same bar the fill is discovered).
	if len(broker.orders) != countAfterFill {
		t.Errorf("expected no new order on the bar the fill is discovered, got %d new",
			len(broker.orders)-countAfterFill)
	}
}

// --- Default behaviour (regression) ---

func TestMACross_DefaultEntryOrderType_StillPlacesMarket(t *testing.T) {
	m, ctx, broker := newHedgeMACross(Config{TrendFilterMin: 0}) // EntryOrderType left unset
	feedBarsNoAutoFill(m, ctx, broker, "BTCUSDT", entryBars)

	req := lastOrder(broker)
	if req.Type != strategy.OrderMarket {
		t.Fatalf("expected default behaviour to stay MARKET, got %s", req.Type)
	}
	if req.Price != 0 || req.MakerOnly {
		t.Errorf("market order should carry no limit price / MakerOnly, got price=%.2f makerOnly=%v", req.Price, req.MakerOnly)
	}
	if m.pending.Active() {
		t.Error("market entries should never populate a pending limit entry")
	}
}

// --- Simple mode (EnableShort=false) ---

func TestMACross_LimitEntry_SimpleMode_PlacesLimitAndTracksPending(t *testing.T) {
	pf := &mutablePortfolio{}
	m, ctx, broker := newSimpleMACross(Config{
		EntryOrderType:      "limit",
		EntryLimitOffsetPct: 0.001,
		EntryTimeoutBars:    2,
	}, pf)

	for i, c := range simpleEntryBars {
		m.OnBar(ctx, hedgeBar("BTCUSDT", i, c))
	}

	req := lastOrder(broker)
	if req.Type != strategy.OrderLimit {
		t.Fatalf("expected a LIMIT entry in simple mode too, got %s", req.Type)
	}
	if !m.pending.Active() {
		t.Fatal("expected a pending entry to be tracked")
	}

	// Unfilled bar: still no position, pending should still be active and not
	// re-evaluate (no second order).
	countBefore := len(broker.orders)
	m.OnBar(ctx, hedgeBar("BTCUSDT", len(simpleEntryBars), 152))
	if len(broker.orders) != countBefore {
		t.Errorf("expected no new order while pending in simple mode, got %d new", len(broker.orders)-countBefore)
	}

	// Now the position appears (limit filled) -- pending should clear.
	pf.hasPosition = true
	pf.qty = 1
	pf.avgPrice = req.Price
	m.OnBar(ctx, hedgeBar("BTCUSDT", len(simpleEntryBars)+1, 152))
	if m.pending.Active() {
		t.Error("expected pending to clear once Portfolio.Position() reports a position")
	}
}

// feedBarsNoAutoFill drives OnBar without synthesizing fills (unlike feedBars),
// so limit orders stay open/unfilled for the caller to control explicitly.
func feedBarsNoAutoFill(m *MACross, ctx *strategy.Context, _ *stubBroker, symbol string, closes []float64) {
	for i, c := range closes {
		m.OnBar(ctx, hedgeBar(symbol, i, c))
	}
}
