package live

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/oms"
)

// persistOrdersLoop drains ordersCh and upserts each order event into the DB.
// Runs as a goroutine alongside processFills.
func (e *Engine) persistOrdersLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("persistOrdersLoop panic recovered", zap.Any("panic", r))
		}
	}()
	for {
		select {
		case <-ctx.Done():
			// Drain remaining order events so final status transitions (e.g. CANCELLED
			// from shutdown) are persisted. Without this, recoverFromDB sees stale status.
			for {
				select {
				case event, ok := <-e.omsInst.Orders():
					if !ok {
						return
					}
					e.persistOrderEvent(event)
				default:
					return
				}
			}
		case event, ok := <-e.omsInst.Orders():
			if !ok {
				return
			}
			e.persistOrderEvent(event)
		}
	}
}

func (e *Engine) persistOrderEvent(event oms.OrderEvent) {
	if e.cfg.Store == nil {
		return
	}
	ord := event.Order
	rec := &data.OrderRecord{
		ClientOrderID:  ord.ClientOrderID,
		ExchangeID:     ord.ExchangeID,
		UserID:         e.cfg.UserID,
		CredentialID:   e.cfg.CredentialID,
		Symbol:         ord.Symbol,
		Side:           string(ord.Side),
		PositionSide:   string(ord.PositionSide),
		Type:           string(ord.Type),
		Status:         string(ord.Status),
		Quantity:       ord.Qty,
		Price:          ord.Price,
		StopPrice:      ord.StopPrice,
		FilledQuantity: ord.FilledQty,
		AvgFillPrice:   ord.AvgFillPrice,
		Commission:     ord.Commission,
		RejectReason:   ord.RejectReason,
		OrderRole:      ord.Role,
		StrategyID:     e.cfg.StrategyID,
		Mode:           "live",
		CreatedAt:      ord.CreatedAt,
	}
	e.dbWg.Add(1)
	go func(r *data.OrderRecord) {
		defer e.dbWg.Done()
		dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := e.cfg.Store.UpsertOrder(dbCtx, r); err != nil {
			e.log.Error("persist order failed",
				zap.String("client_order_id", r.ClientOrderID),
				zap.String("status", r.Status),
				zap.Error(err))
		}
	}(rec)
}

func (e *Engine) persistEquitySnapshot() {
	if e.cfg.Store == nil {
		return
	}
	e.fillMu.Lock()
	rpnl := e.realizedPnL
	e.fillMu.Unlock()
	equity := e.broker.Equity()
	cash := e.broker.Cash()
	unrealized := equity - cash
	snap := &data.EquitySnapshot{
		UserID:        e.cfg.UserID,
		StrategyID:    e.cfg.StrategyID,
		Equity:        equity,
		Cash:          cash,
		UnrealizedPnL: unrealized,
		RealizedPnL:   rpnl,
	}
	onEquity := e.cfg.OnEquity
	userID := e.cfg.UserID
	e.dbWg.Add(1)
	go func() {
		defer e.dbWg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := e.cfg.Store.InsertEquitySnapshot(ctx, snap)
		if err != nil {
			// Retry once on transient failure.
			e.log.Warn("persist equity snapshot failed, retrying once", zap.Error(err))
			retryCtx, retryCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if retryErr := e.cfg.Store.InsertEquitySnapshot(retryCtx, snap); retryErr != nil {
				e.log.Error("persist equity snapshot failed after retry", zap.Error(retryErr))
			}
			retryCancel()
		}
		if onEquity != nil {
			onEquity(userID, equity)
		}
	}()
}

func (e *Engine) sendDailySummary() {
	if e.notifier == nil {
		return
	}
	e.fillMu.Lock()
	rpnl, total, wins := e.realizedPnL, e.total, e.wins
	e.fillMu.Unlock()
	equity := e.broker.Equity()
	var ret float64
	if e.cfg.InitialCapital > 0 {
		ret = (equity/e.cfg.InitialCapital - 1) * 100
	}
	e.notifier.DailySummary(e.cfg.StrategyID, equity, rpnl, ret, total, wins)
}

// Summary returns a one-line result string.
func (e *Engine) Summary() string {
	e.fillMu.Lock()
	rpnl := e.realizedPnL
	e.fillMu.Unlock()
	equity := e.broker.Equity()
	var ret float64
	if e.cfg.InitialCapital > 0 {
		ret = (equity/e.cfg.InitialCapital - 1) * 100
	}
	return fmt.Sprintf(
		"Live Trading Summary | Strategy: %s | Balance: $%.2f (%.2f%%) | "+
			"Realized PnL: $%.2f | Duration: %s",
		e.strategy.Name(),
		equity, ret,
		rpnl,
		time.Since(e.startTime).Truncate(time.Second),
	)
}
