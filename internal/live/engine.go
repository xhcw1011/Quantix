package live

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/bus"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/monitor"
	"github.com/Quantix/quantix/internal/notify"
	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/risk"
	"github.com/Quantix/quantix/internal/strategy"
)

// EngineConfig holds live engine parameters.
type EngineConfig struct {
	StrategyID     string
	InitialCapital float64 // used for % return calculations only; real balance synced from exchange
	StatusInterval time.Duration
	Leverage       int // futures leverage (e.g. 10 = 10x); 0 or 1 = spot (full margin)

	// Margin monitoring thresholds (futures/swap only).
	// Zero values use the MarginMonitor package defaults (warn=0.20, critical=0.12, interval=60s).
	MarginWarnThreshold     float64
	MarginCriticalThreshold float64
	MarginCheckInterval     time.Duration

	// Optional DB persistence (set by API engine manager)
	Store        *data.Store // may be nil → no DB persistence
	UserID       int         // required when Store != nil
	CredentialID int         // stored on each OrderRecord for audit trail

	// Optional real-time push callbacks (set by API engine manager to wire WS hub).
	OnFill   func(userID int, fill *data.Fill) // called after each DB-persisted fill
	OnEquity func(userID int, equity float64)  // called after each equity snapshot
}

// Engine drives live trading:
// closed klines → strategy.OnBar → LiveBroker → Binance order → OMS fill → portfolio update.
type Engine struct {
	cfg        EngineConfig
	broker     *Broker
	positions  *oms.PositionManager
	omsInst    *oms.OMS
	risk       *risk.Manager
	strategy   strategy.Strategy
	stratCtx   *strategy.Context
	bus        *bus.Bus               // may be nil
	metrics    *monitor.TradingMetrics // may be nil
	notifier   *notify.Notifier        // may be nil
	marginMon  *MarginMonitor          // may be nil; active only for futures/swap exchanges
	tickCh     chan float64            // real-time price from ticker WS
	log        *zap.Logger

	fillMu      sync.Mutex // protects realizedPnL, wins, total
	realizedPnL float64
	wins, total int
	startTime   time.Time
	dbWg        sync.WaitGroup // tracks in-flight DB write goroutines for clean shutdown

	// Exchange interfaces (for futures — margin query and equity cache)
	marginQuerier   exchange.MarginQuerier
	equityQuerier   exchange.EquityQuerier
	lastEquityQuery time.Time
	cachedEquityBits atomic.Uint64 // float64 stored as bits for lock-free access

	// Stale bar detection
	lastBarTime  time.Time // last time a kline was received
	staleAlerted bool      // avoid repeated stale alerts

	// Position syncer (Redis-backed, exchange is source of truth)
	posSyncer *position.Syncer // nil if not configured
}

// NewEngine creates a live trading engine.
// bus, metrics, notifier are optional — pass nil to disable.
// orderClient is the exchange-specific order execution backend
// (e.g. binance.OrderBroker or okx.OrderBroker), already initialised and
// safety-gated by the caller via factory.NewOrderClient.
func NewEngine(
	cfg EngineConfig,
	strat strategy.Strategy,
	rm *risk.Manager,
	b *bus.Bus,
	tm *monitor.TradingMetrics,
	notif *notify.Notifier,
	orderClient exchange.OrderClient,
	log *zap.Logger,
) (*Engine, error) {
	o := oms.New(oms.ModeLive, log)
	pm := oms.NewPositionManager()

	broker := New(orderClient, o, pm, notif, log)

	stratCtx := strategy.NewContext(
		&livePortfolioView{broker: broker, positions: pm},
		broker,
		log,
	)

	// Enable margin monitoring automatically when the exchange supports it
	// (OKX SWAP and Binance USDM Futures implement exchange.MarginQuerier).
	// Threshold/interval values come from EngineConfig; zero values use package defaults.
	var mm *MarginMonitor
	var mq exchange.MarginQuerier
	if mqImpl, ok := orderClient.(exchange.MarginQuerier); ok {
		mq = mqImpl
		mm = NewMarginMonitor(cfg.StrategyID, mq, notif, log,
			cfg.MarginCheckInterval,
			cfg.MarginWarnThreshold,
			cfg.MarginCriticalThreshold,
		)
		log.Info("margin monitor enabled for futures/swap exchange")
	}

	// Check if exchange supports direct equity query (futures/swap)
	var eq exchange.EquityQuerier
	if eqq, ok := orderClient.(exchange.EquityQuerier); ok {
		eq = eqq
		log.Info("exchange equity query enabled (futures)")
	}

	// Inject staged exit placer so strategies can place exchange-native TP/SL orders.
	stratCtx.Extra["staged_exit"] = &stagedExitAdapter{broker: broker}

	return &Engine{
		cfg:           cfg,
		broker:        broker,
		positions:     pm,
		omsInst:       o,
		risk:          rm,
		strategy:      strat,
		stratCtx:      stratCtx,
		bus:           b,
		metrics:       tm,
		notifier:      notif,
		marginMon:     mm,
		marginQuerier: mq,
		equityQuerier: eq,
		tickCh:        make(chan float64, 64),
		log:           log,
	}, nil
}

// stagedExitAdapter wraps *Broker to implement strategy.StagedExitPlacer
// without exposing the full broker to strategies.
type stagedExitAdapter struct {
	broker *Broker
	ctx    context.Context // engine lifecycle context, set in Run()
}

func (a *stagedExitAdapter) PlaceStagedTPOrders(symbol, posSide, closeSide string, stopPrice, totalQty float64, tps []strategy.StagedTP) bool {
	liveTPs := make([]StagedTP, len(tps))
	for i, tp := range tps {
		liveTPs[i] = StagedTP{Price: tp.Price, Qty: tp.Qty}
	}
	return a.broker.PlaceStagedTPOrders(a.ctx, symbol, posSide, exchange.OrderSide(closeSide), stopPrice, totalQty, liveTPs)
}

func (a *stagedExitAdapter) PlaceExchangeSL(symbol, posSide, closeSide string, qty, stopPrice float64) bool {
	return a.broker.PlaceExchangeSL(a.ctx, symbol, posSide, exchange.OrderSide(closeSide), qty, stopPrice)
}

func (a *stagedExitAdapter) ReplaceSLOrder(symbol, posSide, closeSide string, remainQty, newStopPrice float64) bool {
	return a.broker.ReplaceSLOrder(a.ctx, symbol, posSide, exchange.OrderSide(closeSide), remainQty, newStopPrice)
}

func (a *stagedExitAdapter) CancelAllProtective(symbol, posSide string) {
	a.broker.cancelProtectiveOrders(a.ctx, symbol, posSide)
}

// SyncBalance fetches live account balance and seeds the risk manager equity.
func (e *Engine) SyncBalance(ctx context.Context, baseCurrency string) error {
	if err := e.broker.SyncBalance(ctx, baseCurrency); err != nil {
		return err
	}
	equity := e.broker.Cash()
	e.cfg.InitialCapital = equity
	return e.risk.UpdateEquity(equity)
}

// Run starts the live trading loop. Reads closed klines from klineCh.
func (e *Engine) Run(ctx context.Context, klineCh <-chan exchange.Kline) error {
	e.startTime = time.Now()
	e.lastBarTime = time.Now()
	e.broker.SetEngineCtx(ctx) // allow async order-fill pollers to use engine lifecycle ctx

	// Wire engine context into the staged exit adapter.
	if adapter, ok := e.stratCtx.Extra["staged_exit"].(*stagedExitAdapter); ok {
		adapter.ctx = ctx
	}
	e.omsInst.SetContext(ctx)  // enable backpressure on fills/orders channels

	// Extract symbol from strategy ID (format: SYMBOL-INTERVAL-STRATEGY or SYMBOL-...)
	symbol := ""
	if parts := strings.SplitN(e.cfg.StrategyID, "-", 2); len(parts) > 0 {
		symbol = parts[0]
	}

	// Smart DB recovery: attempt to restore OMS state from DB-persisted active orders.
	// Falls back to clean-slate cancel if the exchange doesn't support OrderStatusChecker.
	recovered := false
	if e.cfg.Store != nil && e.cfg.UserID > 0 {
		recoveryCtx, recoveryCancel := context.WithTimeout(ctx, 60*time.Second)
		recovered = e.recoverFromDB(recoveryCtx, symbol)
		recoveryCancel()
	}

	// Clean-slate: only cancel open orders when DB recovery was NOT performed
	// (i.e. when Store is nil or recovery fell back to cancel-all for this exchange).
	if !recovered {
		if oc, ok := e.broker.orderClient.(exchange.OpenOrdersCanceller); ok {
			cleanCtx, cleanFn := context.WithTimeout(ctx, 10*time.Second)
			if symbol != "" {
				if err := oc.CancelAllOpenOrders(cleanCtx, symbol); err != nil {
					e.log.Warn("clean-slate: cancel open orders failed (non-fatal)",
						zap.String("symbol", symbol), zap.Error(err))
				} else {
					e.log.Info("clean-slate: all open orders cancelled on startup",
						zap.String("symbol", symbol))
				}
			}
			cleanFn()
			// Sync DB state after exchange-level cancel (synchronous to ensure consistency).
			if e.cfg.Store != nil && e.cfg.UserID > 0 {
				dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := e.cfg.Store.CancelActiveOrders(dbCtx, e.cfg.UserID, e.cfg.StrategyID); err != nil {
					e.log.Warn("clean-slate: cancel active DB orders failed", zap.Error(err))
				}
				dbCancel()
			}
		}
	}

	statusInterval := e.cfg.StatusInterval
	if statusInterval == 0 {
		statusInterval = time.Minute
	}

	statusTicker := time.NewTicker(statusInterval)
	defer statusTicker.Stop()

	dailyTicker := time.NewTicker(24 * time.Hour)
	defer dailyTicker.Stop()

	go e.processFills(ctx)
	go e.persistOrdersLoop(ctx)
	if e.marginMon != nil {
		go e.marginMon.Run(ctx)
	}

	// Start User Data Stream for real-time fill + account + position updates.
	if uds, ok := e.broker.orderClient.(exchange.UserDataSubscriber); ok {
		e.log.Info("user data stream: starting subscription")
		onAccountUpdate := func(walletBalance, crossUnPnl float64) {
			equity := walletBalance + crossUnPnl
			e.cachedEquityBits.Store(math.Float64bits(equity))
			e.lastEquityQuery = time.Now()
			e.broker.equity.Store(equity)
			// Also update syncer
			if e.posSyncer != nil {
				e.posSyncer.OnEquityUpdate(ctx, walletBalance, crossUnPnl)
			}
		}
		onPositionUpdate := func(symbol, side string, qty, entryPrice float64) {
			if e.posSyncer != nil {
				e.posSyncer.OnExchangePositionUpdate(ctx, symbol, side, qty, entryPrice)
			}
		}
		go uds.SubscribeUserData(ctx, func(fill exchange.OrderFill, clientOrderID, status string) {
			// Sync position state on ANY order event (fill, cancel, new) — not just fills.
			// This catches manual opens, manual closes, manual cancels, SL/TP triggers.
			if status != "FILLED" && status != "PARTIALLY_FILLED" {
				// Non-fill event (NEW, CANCELED, EXPIRED) — trigger position sync to stay in sync.
				if e.posSyncer != nil && e.marginQuerier != nil {
					e.log.Info("user data stream: order event → syncing position",
						zap.String("status", status), zap.String("exchange_id", fill.ExchangeID))
					go e.posSyncer.SyncFromExchange(context.Background(), e.marginQuerier)
				}
				return
			}
			ord := e.omsInst.FindByClientOrderID(clientOrderID)
			if ord == nil {
				// Unmatched fill — staged TP/SL, manual close, or external trade.
				e.log.Info("user data stream: unmatched fill → triggering position sync",
					zap.String("exchange_id", fill.ExchangeID),
					zap.Float64("qty", fill.FilledQty),
					zap.Float64("price", fill.AvgPrice),
					zap.String("side", fill.Side),
					zap.String("position_side", fill.PositionSide),
					zap.Bool("reduce_only", fill.IsReduceOnly),
					zap.String("status", status))

				// Cash accounting for unmatched fills (exchange SL/TP, manual trades).
				// Without this, margin locked by the position is never returned to cash.
				e.applyUnmatchedFillCash(fill)

				if e.posSyncer != nil && e.marginQuerier != nil {
					go e.posSyncer.SyncFromExchange(context.Background(), e.marginQuerier)
				}
				return
			}
			// Set exchange ID if not yet set
			if ord.ExchangeID == "" {
				e.omsInst.SetExchangeID(ord.ID, fill.ExchangeID) //nolint:errcheck
			}
			// Accept if still pending
			if ord.Status == oms.StatusPending {
				e.omsInst.Accept(ord.ID) //nolint:errcheck
			}
			// Apply fill
			stratFill := strategy.Fill{
				ID: ord.ID + "-ws", Symbol: ord.Symbol,
				Side: ord.Side, PositionSide: ord.PositionSide,
				Qty: fill.FilledQty, Price: fill.AvgPrice,
				Fee: fill.Fee, Timestamp: time.Now(),
			}
			if err := e.omsInst.Fill(ord.ID, stratFill); err != nil {
				// May already be filled by REST polling — that's OK
				e.log.Debug("user data stream: fill already applied",
					zap.String("oms_id", ord.ID), zap.Error(err))
				return
			}
			e.log.Info("user data stream: fill applied",
				zap.String("oms_id", ord.ID),
				zap.Float64("qty", fill.FilledQty),
				zap.Float64("price", fill.AvgPrice))
		}, onAccountUpdate, onPositionUpdate)
		e.log.Info("user data stream: started (fills + account + positions)")
	}

	e.log.Warn("⚠️  LIVE TRADING ENGINE RUNNING — REAL MONEY AT RISK",
		zap.String("strategy", e.strategy.Name()),
		zap.String("id", e.cfg.StrategyID),
		zap.Float64("balance", e.broker.Cash()),
	)

	if e.notifier != nil {
		e.notifier.SystemAlert("WARN", fmt.Sprintf(
			"⚠️ Quantix LIVE trading started\nStrategy: %s | Balance: $%.2f",
			e.strategy.Name(), e.broker.Cash(),
		))
	}

	for {
		select {
		case <-ctx.Done():
			// Cancel all open exchange orders before stopping to prevent orphaned
			// stop-loss / take-profit orders from continuing to execute.
			cancelCtx, cancelFn := context.WithTimeout(context.Background(), 10*time.Second)
			e.broker.CancelAllPendingOrders(cancelCtx)
			cancelFn()

			// Wait for in-flight DB writes (fill inserts, order upserts) to complete.
			// Use a timeout to avoid hanging shutdown indefinitely.
			dbDone := make(chan struct{})
			go func() { e.dbWg.Wait(); close(dbDone) }()
			select {
			case <-dbDone:
				e.log.Info("all in-flight DB writes completed")
			case <-time.After(10 * time.Second):
				e.log.Warn("shutdown: timed out waiting for in-flight DB writes")
			}

			e.printStatus()
			if e.notifier != nil {
				e.notifier.SystemAlert("INFO", fmt.Sprintf(
					"Quantix LIVE stopped\n%s", e.Summary(),
				))
			}
			e.log.Info("live trading stopped")
			return nil

		case kline, ok := <-klineCh:
			if !ok {
				e.log.Warn("kline channel closed, disabling kline select case")
				klineCh = nil
				continue
			}
			e.onBar(kline)

		case tickPrice, ok := <-e.tickCh:
			if !ok {
				e.tickCh = nil
				continue
			}
			e.broker.SetLastPrice(tickPrice)
			if tr, ok := e.strategy.(strategy.TickReceiver); ok {
				tr.OnTick(e.stratCtx, tickPrice)
			}

		case <-statusTicker.C:
			e.printStatus()
			e.publishStatus()
			e.persistEquitySnapshot()
			e.omsInst.PruneTerminal(30 * time.Minute)

		case <-dailyTicker.C:
			e.sendDailySummary()
		}
	}
}

func (e *Engine) onBar(bar exchange.Kline) {
	e.lastBarTime = time.Now()
	e.staleAlerted = false
	e.broker.SetLastPrice(bar.Close)

	// Equity from WS ACCOUNT_UPDATE push (futures) or local calc (spot).
	var equity float64
	cachedEq := math.Float64frombits(e.cachedEquityBits.Load())
	if cachedEq > 0 {
		equity = cachedEq
	} else {
		prices := map[string]float64{bar.Symbol: bar.Close}
		equity = e.broker.Cash() + e.positions.TotalUnrealizedPnL(prices)
	}
	e.broker.equity.Store(equity)

	if e.metrics != nil {
		e.metrics.EquityGauge.WithLabelValues(e.cfg.StrategyID).Set(equity)
		prices := map[string]float64{bar.Symbol: bar.Close}
		e.metrics.UnrealizedPnL.WithLabelValues(e.cfg.StrategyID).Set(e.positions.TotalUnrealizedPnL(prices))
		e.metrics.OpenPositions.WithLabelValues(e.cfg.StrategyID).Set(float64(len(e.positions.All())))
		if e.risk.Halted() {
			e.metrics.RiskHalted.WithLabelValues(e.cfg.StrategyID).Set(1)
		} else {
			e.metrics.RiskHalted.WithLabelValues(e.cfg.StrategyID).Set(0)
		}
	}

	if err := e.risk.UpdateEquity(equity); err != nil {
		e.log.Error("live trading halted by risk manager",
			zap.Float64("equity", equity), zap.Error(err))
		if e.notifier != nil {
			var drawdown float64
			if e.cfg.InitialCapital > 0 {
				drawdown = (1 - equity/e.cfg.InitialCapital) * 100
			}
			e.notifier.RiskAlert(e.cfg.StrategyID, err.Error(), equity, drawdown)
		}
		return
	}

	e.strategy.OnBar(e.stratCtx, bar)
}

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
			e.total++
			if realized > 0 {
				e.wins++
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

			prices := map[string]float64{event.Fill.Symbol: event.Fill.Price}
			unrealizedPnL := e.positions.TotalUnrealizedPnL(prices)
			equity := e.broker.Cash() + unrealizedPnL
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
				e.notifier.FillNotification(
					e.cfg.StrategyID, event.Order.ID,
					event.Fill.Symbol, string(event.Fill.Side),
					event.Fill.Qty, event.Fill.Price, event.Fill.Fee, realized,
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

			e.strategy.OnFill(e.stratCtx, event.Fill)
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
	if sym == "" { sym = "ETHUSDT" } // fallback
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
	e.total++
	if realized > 0 {
		e.wins++
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

	prices := map[string]float64{sym: fill.AvgPrice}
	unrealizedPnL := e.positions.TotalUnrealizedPnL(prices)
	equity := e.broker.Cash() + unrealizedPnL
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
}

// ─── livePortfolioView ────────────────────────────────────────────────────────

type livePortfolioView struct {
	broker    *Broker
	positions *oms.PositionManager
}

func (pv *livePortfolioView) Cash() float64 { return pv.broker.Cash() }

func (pv *livePortfolioView) Position(symbol string) (qty, avgPrice float64, ok bool) {
	pos, exists := pv.positions.Position(symbol)
	if !exists {
		return 0, 0, false
	}
	return pos.Qty, pos.AvgEntryPrice, true
}

func (pv *livePortfolioView) Equity(prices map[string]float64) float64 {
	unrealized := pv.positions.TotalUnrealizedPnL(prices)
	return pv.broker.Cash() + unrealized
}
