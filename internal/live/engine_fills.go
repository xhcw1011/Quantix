package live

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/bus"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

func (e *Engine) processFills(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("processFills panic recovered", zap.Any("panic", r))
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-e.omsInst.Fills():
			if !ok {
				return
			}
			fillTime := time.Now()
			realized := e.positions.ApplyFill(event.Fill)

			e.fillMu.Lock()
			e.realizedPnL += realized
			// Win rate counts only closing fills (those with non-zero realized PnL).
			// Opening fills produce realized=0 by definition; counting them dilutes
			// the win rate metric to ~half the true value.
			if realized != 0 {
				e.total++
				if realized > 0 {
					e.wins++
				}
			}
			e.fillMu.Unlock()

			// Update cash: broker.PlaceOrder only publishes the OMS event;
			// cash accounting is the sole responsibility of processFills.
			// Cash accounting uses actual leverage for futures margin calculation.
			leverage := e.cfg.Leverage
			if leverage < 1 {
				leverage = 1
			}
			marginRate := 1.0 / float64(leverage)
			prevCash := e.broker.Cash()
			ps := string(event.Fill.PositionSide)
			isOpeningShort := ps == string(strategy.PositionSideShort) && event.Fill.Side == strategy.SideSell
			isClosingShort := ps == string(strategy.PositionSideShort) && event.Fill.Side == strategy.SideBuy
			isOpeningLong := ps == string(strategy.PositionSideLong) && event.Fill.Side == strategy.SideBuy
			isClosingLong := ps == string(strategy.PositionSideLong) && event.Fill.Side == strategy.SideSell
			notional := event.Fill.Qty * event.Fill.Price
			switch {
			case isOpeningShort:
				e.broker.cash.Store(prevCash - notional*marginRate - event.Fill.Fee)
			case isClosingShort:
				e.broker.cash.Store(prevCash + notional*marginRate + realized - event.Fill.Fee)
			case isOpeningLong:
				e.broker.cash.Store(prevCash - notional*marginRate - event.Fill.Fee)
			case isClosingLong:
				e.broker.cash.Store(prevCash + notional*marginRate + realized - event.Fill.Fee)
			case event.Fill.Side == strategy.SideBuy: // spot/one-way: full notional
				e.broker.cash.Store(prevCash - notional - event.Fill.Fee)
			case event.Fill.Side == strategy.SideSell:
				e.broker.cash.Store(prevCash + notional - event.Fill.Fee)
			}

			// Update true wallet balance: only fees and realized PnL affect it.
			// Opening a position does NOT change wallet balance (only locks margin).
			prevWallet := e.broker.WalletBalance()
			e.broker.walletBalance.Store(prevWallet + realized - event.Fill.Fee)

			prices := map[string]float64{event.Fill.Symbol: event.Fill.Price}
			unrealizedPnL := e.positions.TotalUnrealizedPnL(prices)
			equity := e.broker.WalletBalance() + unrealizedPnL
			e.broker.equity.Store(equity)

			latencyMs := float64(fillTime.Sub(event.Fill.Timestamp).Milliseconds())

			if e.metrics != nil {
				e.fillMu.Lock()
				rpnl, wins, total := e.realizedPnL, e.wins, e.total
				e.fillMu.Unlock()
				e.metrics.RealizedPnL.WithLabelValues(e.cfg.StrategyID).Set(rpnl)
				e.metrics.FillsTotal.WithLabelValues(
					e.cfg.StrategyID, event.Fill.Symbol, string(event.Fill.Side), "live",
				).Inc()
				e.metrics.TradeLatency.WithLabelValues(e.cfg.StrategyID).Observe(latencyMs)
				if total > 0 {
					e.metrics.WinRate.WithLabelValues(e.cfg.StrategyID).Set(
						float64(wins) / float64(total) * 100,
					)
				}
			}

			if e.bus != nil {
				e.bus.PublishFill(bus.FillMsg{ //nolint:errcheck
					StrategyID:  e.cfg.StrategyID,
					OrderID:     event.Order.ID,
					Symbol:      event.Fill.Symbol,
					Side:        string(event.Fill.Side),
					Qty:         event.Fill.Qty,
					Price:       event.Fill.Price,
					Fee:         event.Fill.Fee,
					RealizedPnL: realized,
					Timestamp:   event.Fill.Timestamp,
				})
			}

			// Persist fill to DB asynchronously and push WS notification.
			if e.cfg.Store != nil {
				fill := &data.Fill{
					UserID:          e.cfg.UserID,
					StrategyID:      e.cfg.StrategyID,
					Symbol:          event.Fill.Symbol,
					Side:            string(event.Fill.Side),
					PositionSide:    string(event.Fill.PositionSide),
					Qty:             event.Fill.Qty,
					Price:           event.Fill.Price,
					Fee:             event.Fill.Fee,
					RealizedPnL:     realized,
					ExchangeOrderID: event.Order.ExchangeID,
					Mode:            "live",
					FilledAt:        event.Fill.Timestamp,
				}
				onFill := e.cfg.OnFill
				userID := e.cfg.UserID
				e.dbWg.Add(1)
				go func() {
					defer e.dbWg.Done()
					dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if err := e.cfg.Store.InsertFill(dbCtx, fill); err != nil {
						e.log.Error("persist fill failed", zap.Error(err))
					}
					if onFill != nil {
						onFill(userID, fill)
					}
				}()
			}

			if e.notifier != nil {
				ps := string(event.Fill.PositionSide)
				isClose := (ps == string(strategy.PositionSideLong) && event.Fill.Side == strategy.SideSell) ||
					(ps == string(strategy.PositionSideShort) && event.Fill.Side == strategy.SideBuy)
				e.notifier.FillNotification(
					e.cfg.StrategyID, event.Order.ID,
					event.Fill.Symbol, string(event.Fill.Side),
					event.Fill.Qty, event.Fill.Price, event.Fill.Fee, realized,
					isClose,
				)
			}

			e.log.Info("live fill",
				zap.String("order_id", event.Order.ID),
				zap.String("symbol", event.Fill.Symbol),
				zap.String("side", string(event.Fill.Side)),
				zap.Float64("qty", event.Fill.Qty),
				zap.Float64("price", event.Fill.Price),
				zap.Float64("fee", event.Fill.Fee),
				zap.Float64("realized_pnl", realized),
				zap.Float64("cash", e.broker.Cash()),
				zap.Float64("latency_ms", latencyMs),
			)

			select {
			case e.stratFillCh <- event.Fill:
			default:
				e.log.Warn("strategy fill channel full — OnFill delayed",
					zap.String("order_id", event.Order.ID))
			}
		}
	}
}

// applyUnmatchedFillCash updates cash accounting for fills not tracked by OMS.
// This covers exchange-native SL/TP (algo orders), manual trades, and external closes.
// Without this, margin locked by the closed position is never returned to cash.
func (e *Engine) applyUnmatchedFillCash(fill exchange.OrderFill) {
	if fill.FilledQty <= 0 || fill.AvgPrice <= 0 {
		return
	}
	leverage := e.cfg.Leverage
	if leverage < 1 {
		leverage = 1
	}
	marginRate := 1.0 / float64(leverage)
	notional := fill.FilledQty * fill.AvgPrice
	prevCash := e.broker.Cash()

	// Determine if this is a closing fill.
	// Exchange SL/TP are always reduce-only (closing), and are the primary case.
	isClosingLong := fill.PositionSide == "LONG" && fill.Side == "SELL"
	isClosingShort := fill.PositionSide == "SHORT" && fill.Side == "BUY"
	isOpeningLong := fill.PositionSide == "LONG" && fill.Side == "BUY"
	isOpeningShort := fill.PositionSide == "SHORT" && fill.Side == "SELL"

	// Estimate realized PnL from position manager.
	sym := fill.Symbol
	if sym == "" {
		sym = "ETHUSDT"
	} // fallback
	realized := e.positions.ApplyFill(strategy.Fill{
		Symbol:       sym,
		Side:         strategy.Side(fill.Side),
		PositionSide: strategy.PositionSide(fill.PositionSide),
		Qty:          fill.FilledQty,
		Price:        fill.AvgPrice,
		Fee:          fill.Fee,
		Timestamp:    time.Now(),
	})

	e.fillMu.Lock()
	e.realizedPnL += realized
	// Same win-rate fix as processFills: only count closing fills.
	if realized != 0 {
		e.total++
		if realized > 0 {
			e.wins++
		}
	}
	e.fillMu.Unlock()

	switch {
	case isClosingLong:
		e.broker.cash.Store(prevCash + notional*marginRate + realized - fill.Fee)
	case isClosingShort:
		e.broker.cash.Store(prevCash + notional*marginRate + realized - fill.Fee)
	case isOpeningLong:
		e.broker.cash.Store(prevCash - notional*marginRate - fill.Fee)
	case isOpeningShort:
		e.broker.cash.Store(prevCash - notional*marginRate - fill.Fee)
	default:
		// One-way mode or unknown — use side heuristic
		if fill.Side == "SELL" {
			e.broker.cash.Store(prevCash + notional - fill.Fee)
		} else {
			e.broker.cash.Store(prevCash - notional - fill.Fee)
		}
	}

	// Update true wallet balance
	prevWallet := e.broker.WalletBalance()
	e.broker.walletBalance.Store(prevWallet + realized - fill.Fee)

	prices := map[string]float64{sym: fill.AvgPrice}
	unrealizedPnL := e.positions.TotalUnrealizedPnL(prices)
	equity := e.broker.WalletBalance() + unrealizedPnL
	e.broker.equity.Store(equity)

	e.log.Info("unmatched fill: cash updated",
		zap.String("side", fill.Side),
		zap.String("position_side", fill.PositionSide),
		zap.Float64("qty", fill.FilledQty),
		zap.Float64("price", fill.AvgPrice),
		zap.Float64("realized", realized),
		zap.Float64("cash", e.broker.Cash()),
		zap.Float64("equity", equity),
	)

	if e.notifier != nil {
		isClose := isClosingLong || isClosingShort
		orderID := "unmatched"
		if fill.ExchangeID != "" {
			orderID = "unmatched-" + fill.ExchangeID
		}
		e.notifier.FillNotification(
			e.cfg.StrategyID, orderID,
			sym, fill.Side,
			fill.FilledQty, fill.AvgPrice, fill.Fee, realized,
			isClose,
		)
	}
}
