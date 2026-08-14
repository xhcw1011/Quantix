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

// persistOrderEvent upserts one order snapshot into the DB. Deliberately
// SYNCHRONOUS (unlike persistEquitySnapshot/processFills' fire-and-forget
// goroutines below): UpsertOrder is keyed by client_order_id, so multiple
// events for the SAME order (e.g. "NEW" right after submit, then "FILLED"
// moments later once the exchange fill confirms) upsert the SAME row. Firing
// each as an independent, unordered goroutine let a later-queued-but-
// faster-executing "NEW" write race past and clobber an already-persisted
// "FILLED" write — the order ends up stuck showing status=OPEN/filled_qty=0
// in the DB forever despite having actually filled (confirmed via the fills
// table and OMS logs; 2026-08-13 finding). persistOrdersLoop already
// processes events for this engine one at a time from a channel, so making
// this call synchronous is enough to guarantee correct per-order ordering —
// no per-order locking needed.
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
	dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.cfg.Store.UpsertOrder(dbCtx, rec); err != nil {
		e.log.Error("persist order failed",
			zap.String("client_order_id", rec.ClientOrderID),
			zap.String("status", rec.Status),
			zap.Error(err))
	}
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
	equity := e.broker.Equity()

	e.fillMu.Lock()
	// Compute window-relative metrics (since last summary), then snapshot for next window.
	baseline := e.dailyBaselineEquity
	intervalRealized := e.realizedPnL - e.dailyBaselineRealizedPnL
	intervalWins := e.wins - e.dailyBaselineWins
	intervalTotal := e.total - e.dailyBaselineTotal
	e.dailyBaselineEquity = equity
	e.broker.SetDayStartEquity(equity) // roll ORG daily-loss baseline
	e.dailyBaselineWins = e.wins
	e.dailyBaselineTotal = e.total
	e.dailyBaselineRealizedPnL = e.realizedPnL
	e.fillMu.Unlock()

	var ret float64
	if baseline > 0 {
		ret = (equity/baseline - 1) * 100
	}
	e.notifier.DailySummary(e.cfg.StrategyID, equity, intervalRealized, ret, intervalTotal, intervalWins)
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
		"实盘交易汇总 | 策略：%s | 余额：$%.2f（%.2f%%）| "+
			"已实现盈亏：$%.2f | 运行时长：%s",
		e.strategy.Name(),
		equity, ret,
		rpnl,
		time.Since(e.startTime).Truncate(time.Second),
	)
}
