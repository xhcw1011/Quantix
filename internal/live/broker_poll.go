package live

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// pollOrderUntilFilled polls the exchange every 5 seconds until the order is filled,
// cancelled, or the engine context is cancelled. On fill, publishes to the OMS so that
// processFills() handles portfolio accounting and strategy.OnFill notification.
func (b *Broker) pollOrderUntilFilled(ctx context.Context, sc exchange.OrderStatusChecker, exchangeID, ordID string, req strategy.OrderRequest) {
	interval := b.pollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Track cumulative filled qty to emit only the incremental portion on each poll,
	// preventing duplicate fills when the exchange reports the same partial fill repeatedly.
	var prevFilledQty float64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			status, fill, err := sc.GetOrderStatus(queryCtx, req.Symbol, exchangeID)
			cancel()
			// Check if engine context was cancelled during the query.
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				b.log.Debug("poll order status error",
					zap.String("exchange_id", exchangeID),
					zap.Error(err))
				continue
			}

			switch status {
			case "filled", "FILLED", "partially_filled", "PARTIALLY_FILLED":
				// Only publish the incremental fill qty since the last poll.
				incrementalQty := fill.FilledQty - prevFilledQty
				if incrementalQty > filledEps {
					stratFill := strategy.Fill{
						ID:           fmt.Sprintf("%s-poll-%d", ordID, time.Now().UnixMilli()),
						Symbol:       req.Symbol,
						Side:         req.Side,
						PositionSide: req.PositionSide,
						Qty:          incrementalQty,
						Price:        fill.AvgPrice,
						Fee:          fill.Fee,
						Timestamp:    time.Now(),
					}
					if fillErr := b.omsInst.Fill(ordID, stratFill); fillErr != nil {
						b.log.Warn("poll fill: OMS fill failed",
							zap.String("order_id", ordID),
							zap.Error(fillErr))
					} else {
						b.log.Info("poll confirmed order filled",
							zap.String("order_id", ordID),
							zap.String("exchange_id", exchangeID),
							zap.Float64("incremental_qty", incrementalQty),
							zap.Float64("total_filled", fill.FilledQty),
							zap.Float64("price", fill.AvgPrice),
						)
					}
					prevFilledQty = fill.FilledQty
				}
				if status == "filled" || status == "FILLED" {
					return // fully filled; stop polling
				}
			case "canceled", "CANCELED", "cancelled", "CANCELLED", "expired", "EXPIRED":
				b.log.Info("polled order no longer active",
					zap.String("order_id", ordID),
					zap.String("status", status))
				return
			}
		}
	}
}
