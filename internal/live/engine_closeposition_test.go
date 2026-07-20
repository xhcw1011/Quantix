package live

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/oms"
)

// closePosMock is a mockOrderClient that also answers position queries
// (PositionQuerier) and records which exchange order IDs were cancelled — enough
// to verify Engine.ClosePosition cancels the position's paired protective stop.
type closePosMock struct {
	*mockOrderClient
	positions    []exchange.PositionInfo
	cancelledIDs []string
}

func (c *closePosMock) GetPositions(context.Context) ([]exchange.PositionInfo, error) {
	return c.positions, nil
}

func (c *closePosMock) CancelOrder(_ context.Context, _, id string) error {
	c.mu.Lock()
	c.cancelledIDs = append(c.cancelledIDs, id)
	c.mu.Unlock()
	return c.cancelErr
}

// The web "close position" button routes through Engine.ClosePosition, which fires
// a market close directly at the exchange client — bypassing the broker's normal
// closing-fill flow where cancelProtectiveOrders runs. Without an explicit cancel
// here, the resting stop-loss is orphaned on the exchange after the position closes.
func TestEngineClosePositionCancelsProtectiveStop(t *testing.T) {
	log := zap.NewNop()
	mock := &closePosMock{
		mockOrderClient: &mockOrderClient{
			marketFill: exchange.OrderFill{ExchangeID: "close-1", FilledQty: 0.043, AvgPrice: 64000, Status: "filled"},
		},
		positions: []exchange.PositionInfo{
			{Symbol: "BTCUSDT", PositionSide: "LONG", Amt: 0.043, EntryPrice: 64884},
		},
	}
	o := oms.New(oms.ModeLive, log)
	pm := oms.NewPositionManager()
	b := New(mock, o, pm, nil, log)
	b.SetEngineCtx(context.Background())

	// A protective stop is resting for the LONG (as placeProtectiveOrders tracks it).
	b.protMu.Lock()
	b.protectiveOrders[brokerPosKey("BTCUSDT", "LONG")] = protectiveIDs{stopID: "stop-abc"}
	b.protMu.Unlock()

	e := &Engine{broker: b, log: log}
	_, _, err := e.ClosePosition(context.Background(), "BTCUSDT", "LONG")
	require.NoError(t, err)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	assert.Contains(t, mock.cancelledIDs, "stop-abc",
		"web close-position must cancel the position's paired stop-loss (else it orphans on the exchange)")
}
