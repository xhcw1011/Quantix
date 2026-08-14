package position

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
)

// fakeFullQuerier implements both exchange.MarginQuerier and exchange.PositionQuerier,
// with independently controllable results — mirrors Binance Futures' OrderBroker,
// where both methods hit the same NewGetPositionRiskService() call but parse its
// fields with different strictness (GetMarginRatios drops an entry entirely if
// markPrice fails to parse; GetPositions ignores that same error).
type fakeFullQuerier struct {
	ratios    []exchange.PositionMarginInfo
	positions []exchange.PositionInfo
}

func (f *fakeFullQuerier) GetMarginRatios(ctx context.Context) ([]exchange.PositionMarginInfo, error) {
	return f.ratios, nil
}

func (f *fakeFullQuerier) GetPositions(ctx context.Context) ([]exchange.PositionInfo, error) {
	return f.positions, nil
}

// fakeMarginOnlyQuerier implements only exchange.MarginQuerier — mirrors brokers
// (e.g. OKX) that don't implement PositionQuerier at all, so loadFromExchange has
// no secondary signal to cross-check against.
type fakeMarginOnlyQuerier struct {
	ratios []exchange.PositionMarginInfo
}

func (f *fakeMarginOnlyQuerier) GetMarginRatios(ctx context.Context) ([]exchange.PositionMarginInfo, error) {
	return f.ratios, nil
}

func shortPosition() *StrategyPosition {
	return &StrategyPosition{
		ExchangePosition: ExchangePosition{Symbol: "BTCUSDT", Side: "SHORT", Qty: 0.002, EntryPrice: 63839.7},
		Filled:           true,
	}
}

// TestLoadFromExchange_MarginRatiosGlitchDoesNotWipeRealPosition reproduces a real
// incident: GetMarginRatios silently dropped a real, open SHORT position from its
// result (e.g. a transient markPrice parse failure), while GetPositions — hitting
// the same underlying exchange call with looser parsing — still confirmed it. The
// old phantom-clear logic trusted GetMarginRatios alone and wiped the position from
// memory + deleted its Redis cache entry, even though the position was real and
// still protected by a resting exchange stop-loss.
func TestLoadFromExchange_MarginRatiosGlitchDoesNotWipeRealPosition(t *testing.T) {
	s := &Syncer{log: zap.NewNop(), symbol: "BTCUSDT", short: shortPosition()}
	q := &fakeFullQuerier{
		ratios: nil, // GetMarginRatios glitch: SHORT entry silently dropped
		positions: []exchange.PositionInfo{
			{Symbol: "BTCUSDT", PositionSide: "SHORT", Amt: -0.002, EntryPrice: 63839.7},
		},
	}

	s.loadFromExchange(context.Background(), q)

	if s.short == nil {
		t.Fatal("phantom-clear wiped a real SHORT position that GetPositions still confirmed — a single flaky signal must not be enough to clear a real position")
	}
}

// TestLoadFromExchange_BothSourcesAgreeAbsent_ClearsPhantomPosition confirms a
// genuine phantom (both GetMarginRatios and GetPositions agree the position is
// gone — a real manual close or SL fill) still gets cleared as before.
func TestLoadFromExchange_BothSourcesAgreeAbsent_ClearsPhantomPosition(t *testing.T) {
	s := &Syncer{log: zap.NewNop(), symbol: "BTCUSDT", short: shortPosition()}
	q := &fakeFullQuerier{ratios: nil, positions: nil}

	s.loadFromExchange(context.Background(), q)

	if s.short != nil {
		t.Fatal("expected a genuine phantom SHORT (both sources agree absent) to be cleared")
	}
}

// TestLoadFromExchange_NoPositionQuerier_FallsBackToMarginRatiosOnly confirms
// brokers without a secondary PositionQuerier (e.g. OKX) still clear genuine
// phantom positions using GetMarginRatios alone — the cross-check is a guard,
// not a requirement.
func TestLoadFromExchange_NoPositionQuerier_FallsBackToMarginRatiosOnly(t *testing.T) {
	s := &Syncer{log: zap.NewNop(), symbol: "BTCUSDT", short: shortPosition()}
	q := &fakeMarginOnlyQuerier{ratios: nil}

	s.loadFromExchange(context.Background(), q)

	if s.short != nil {
		t.Fatal("exchanges without a PositionQuerier must still clear genuine phantom positions using GetMarginRatios alone")
	}
}
