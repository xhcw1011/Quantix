package live

import (
	"context"
	"fmt"
	"math"
	"os"
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
	BarInterval    time.Duration // primary kline interval for stale detection (e.g. 5*time.Minute)
	Leverage       int           // futures leverage (e.g. 10 = 10x); 0 or 1 = spot (full margin)

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
	OnFill   func(userID int, fill *data.Fill)            // called after each DB-persisted fill
	OnEquity func(userID int, equity float64)             // called after each equity snapshot
	OnStatus func(userID int, status map[string]any)      // called after each periodic status print

	// SkipCleanSlate skips cancelling all exchange orders on startup.
	// Set to true when user has manual positions/orders that should not be touched.
	SkipCleanSlate bool
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

	fillMu      sync.Mutex // protects realizedPnL, wins, total, dailyBaselineEquity, dailyBaselineWins, dailyBaselineTotal
	realizedPnL float64
	wins, total int
	// Daily summary baseline — captured at engine start and reset each daily summary.
	// Used to compute "since last summary" return%/wins/total instead of "since engine start"
	// (which made the metrics drift to whatever cash happened to be at last restart).
	dailyBaselineEquity      float64
	dailyBaselineWins        int
	dailyBaselineTotal       int
	dailyBaselineRealizedPnL float64
	startTime           time.Time
	dbWg        sync.WaitGroup // tracks in-flight DB write goroutines for clean shutdown
	stratFillCh chan strategy.Fill // routes OnFill to the main goroutine (eliminates data race)

	// Exchange interfaces (for futures — margin query and equity cache)
	marginQuerier   exchange.MarginQuerier
	equityQuerier   exchange.EquityQuerier
	lastEquityQuery time.Time
	cachedEquityBits atomic.Uint64 // float64 stored as bits for lock-free access

	// Non-trade balance adjustment (transfer/deposit/funding fee)
	equityAdjusted atomic.Value // float64; set by UDS goroutine, consumed by bar goroutine

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
		tickCh:        make(chan float64, 512),
		stratFillCh:   make(chan strategy.Fill, 16),
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

func (a *stagedExitAdapter) CancelExchangeSL(symbol, posSide string) {
	a.broker.CancelExchangeSL(a.ctx, symbol, posSide)
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
	// Capture starting equity as baseline for the first daily summary's % return.
	e.fillMu.Lock()
	e.dailyBaselineEquity = e.broker.Equity()
	e.dailyBaselineWins = 0
	e.dailyBaselineTotal = 0
	e.dailyBaselineRealizedPnL = 0
	e.fillMu.Unlock()
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
	// Skip if SkipCleanSlate is set or QUANTIX_SKIP_CLEAN_SLATE env var is set.
	// Use env var when user has manual positions that should not be touched.
	// Skip clean-slate if syncer has positions (normal restart with active grid/trend positions).
	// Clean-slate cancels ALL exchange orders including grid layer limit orders — destructive.
	hasPositions := e.posSyncer != nil && (e.posSyncer.HasPosition("LONG") || e.posSyncer.HasPosition("SHORT"))
	skipClean := e.cfg.SkipCleanSlate || os.Getenv("QUANTIX_SKIP_CLEAN_SLATE") == "true" || hasPositions
	if hasPositions {
		e.log.Info("clean-slate: skipped — syncer has active positions, preserving exchange orders")
	}

	// Seed PositionManager from syncer so close-fill realized PnL is correct.
	// Without this, ApplyFill on a closing fill returns 0 because the manager
	// never observed the opening trade (which happened pre-restart). Side effect:
	// fills.realized_pnl stays 0 in DB and TG notifications miss the PnL line.
	if e.posSyncer != nil {
		if lp := e.posSyncer.GetLong(); lp != nil && lp.Qty > 0 && lp.EntryPrice > 0 {
			e.positions.SeedPosition(lp.Symbol, "LONG", lp.Qty, lp.EntryPrice)
			e.log.Info("seeded PositionManager from syncer",
				zap.String("side", "LONG"), zap.String("symbol", lp.Symbol),
				zap.Float64("qty", lp.Qty), zap.Float64("entry", lp.EntryPrice))
		}
		if sp := e.posSyncer.GetShort(); sp != nil && sp.Qty > 0 && sp.EntryPrice > 0 {
			e.positions.SeedPosition(sp.Symbol, "SHORT", sp.Qty, sp.EntryPrice)
			e.log.Info("seeded PositionManager from syncer",
				zap.String("side", "SHORT"), zap.String("symbol", sp.Symbol),
				zap.Float64("qty", sp.Qty), zap.Float64("entry", sp.EntryPrice))
		}
	}
	if !recovered && !skipClean {
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

	// Adopt any exchange orders not tracked by OMS (orphans from crashes,
	// races between order placement and DB write, manual orders). After this
	// runs, subsequent fills on those orders go through the matched-fill path
	// instead of unmatched-fill (so TG notifications fire normally).
	if symbol != "" {
		orphanCtx, orphanCancel := context.WithTimeout(ctx, 15*time.Second)
		e.claimOrphanOrders(orphanCtx, symbol)
		orphanCancel()
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

	// Independent watchdog: detects engine loop freeze and forces process exit.
	// Runs in a separate goroutine so it works even when the main select loop is blocked.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				elapsed := time.Since(e.lastBarTime)
				if elapsed > 10*time.Minute {
					e.log.Error("WATCHDOG: engine loop appears frozen — no bar activity for 10+ min, forcing exit",
						zap.Duration("since_last_bar", elapsed))
					os.Exit(1) // hard exit — systemd/auto-restart will bring it back
				}
			}
		}
	}()

	// Start User Data Stream for real-time fill + account + position updates.
	if uds, ok := e.broker.orderClient.(exchange.UserDataSubscriber); ok {
		e.log.Info("user data stream: starting subscription")
		onAccountUpdate := func(walletBalance, crossUnPnl float64, reason string) {
			equity := walletBalance + crossUnPnl
			e.cachedEquityBits.Store(math.Float64bits(equity))
			e.lastEquityQuery = time.Now()
			e.broker.equity.Store(equity)
			// Also update syncer
			if e.posSyncer != nil {
				e.posSyncer.OnEquityUpdate(ctx, walletBalance, crossUnPnl)
			}
			// User-initiated balance changes (transfer in/out) — adjust risk baseline
			// so risk manager doesn't mistake transfers for trading losses.
			// FUNDING_FEE is a trading cost, not a transfer — don't adjust.
			isTransfer := reason == "DEPOSIT" || reason == "WITHDRAW" ||
				reason == "ADMIN_DEPOSIT" || reason == "ADJUSTMENT"
			if isTransfer {
				e.log.Info("account balance change (transfer)",
					zap.String("reason", reason),
					zap.Float64("wallet", walletBalance),
					zap.Float64("equity", equity))
				e.risk.AdjustDayStart(equity)
				// Notify strategy to adjust dayStartEquity (picked up on next OnBar).
				// Use atomic store to avoid race with strategy goroutine reading Extra map.
				e.equityAdjusted.Store(equity)
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
			// Positions and protective orders (TP/SL) are preserved on shutdown.
			// On restart, recoverFromDB reconciles OMS with exchange and
			// the strategy reloads position state from Redis.

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
			e.log.Debug("engine: bar received",
				zap.String("symbol", kline.Symbol), zap.String("interval", kline.Interval),
				zap.Float64("close", kline.Close), zap.Bool("closed", kline.IsClosed))
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

		case fill := <-e.stratFillCh:
			e.strategy.OnFill(e.stratCtx, fill)

		case <-statusTicker.C:
			e.printStatus()
			e.publishStatus()
			e.persistEquitySnapshot()
			e.omsInst.PruneTerminal(30 * time.Minute)

			// Stale bar watchdog moved to independent goroutine (lines 282-298).
			// Independent goroutine can detect freezes even when this select loop is blocked.

		case <-dailyTicker.C:
			e.sendDailySummary()
		}
	}
}

// closeAllPositionsOnShutdown market-closes all open positions before engine stops.
// Prevents orphaned positions from accumulating losses while engine is offline.
func (e *Engine) closeAllPositionsOnShutdown() {
	if e.posSyncer == nil {
		e.log.Info("shutdown watchdog: no position syncer, skipping position close")
		return
	}

	symbol := ""
	if parts := strings.SplitN(e.cfg.StrategyID, "-", 2); len(parts) > 0 {
		symbol = parts[0]
	}

	closed := 0

	// Close LONG if exists
	if lp := e.posSyncer.GetLong(); lp != nil && lp.Qty > 0 {
		e.log.Warn("shutdown watchdog: closing LONG position at market",
			zap.String("symbol", symbol), zap.Float64("qty", lp.Qty),
			zap.Float64("entry", lp.EntryPrice))
		req := strategy.OrderRequest{
			Symbol: symbol, Side: strategy.SideSell,
			PositionSide: strategy.PositionSideLong, Qty: lp.Qty,
		}
		if id := e.stratCtx.PlaceOrder(req); id != "" {
			closed++
			e.log.Info("shutdown watchdog: LONG close order placed", zap.String("id", id))
		} else {
			e.log.Error("shutdown watchdog: failed to close LONG position")
		}
	}

	// Close SHORT if exists
	if sp := e.posSyncer.GetShort(); sp != nil && sp.Qty > 0 {
		e.log.Warn("shutdown watchdog: closing SHORT position at market",
			zap.String("symbol", symbol), zap.Float64("qty", sp.Qty),
			zap.Float64("entry", sp.EntryPrice))
		req := strategy.OrderRequest{
			Symbol: symbol, Side: strategy.SideBuy,
			PositionSide: strategy.PositionSideShort, Qty: sp.Qty,
		}
		if id := e.stratCtx.PlaceOrder(req); id != "" {
			closed++
			e.log.Info("shutdown watchdog: SHORT close order placed", zap.String("id", id))
		} else {
			e.log.Error("shutdown watchdog: failed to close SHORT position")
		}
	}

	if closed > 0 {
		// Give exchange time to process market orders
		time.Sleep(3 * time.Second)
		e.log.Warn("shutdown watchdog: closed positions before stop",
			zap.Int("count", closed))
		if e.notifier != nil {
			e.notifier.SystemAlert("WARN", fmt.Sprintf(
				"⚠️ Watchdog: closed %d position(s) at market on engine shutdown", closed))
		}
	} else {
		e.log.Info("shutdown watchdog: no open positions to close")
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

	// Pass non-trade balance adjustment to strategy (thread-safe via atomic).
	if adj, ok := e.equityAdjusted.Load().(float64); ok && adj > 0 {
		e.stratCtx.Extra["equity_adjusted"] = adj
		e.equityAdjusted.Store(float64(0)) // consumed
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

// ClosePosition closes the position for (symbol, side) by querying the exchange
// for actual qty and placing a reduce-only market order. Used by the operator
// API so a single side can be closed without restarting the engine or running
// the close-positions CLI. The fill comes back via the user-data stream and
// flows through the normal cash/equity/TG path.
//
// side must be "LONG" or "SHORT". Returns the closed qty and avg fill price.
func (e *Engine) ClosePosition(ctx context.Context, symbol, side string) (qty, fillPrice float64, err error) {
	if side != "LONG" && side != "SHORT" {
		return 0, 0, fmt.Errorf("side must be LONG or SHORT (got %q)", side)
	}
	mq, ok := e.broker.orderClient.(exchange.MarginQuerier)
	if !ok {
		return 0, 0, fmt.Errorf("exchange does not support position query (not a futures broker)")
	}
	positions, err := mq.GetMarginRatios(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("query positions: %w", err)
	}
	var target *exchange.PositionMarginInfo
	for i := range positions {
		if positions[i].Symbol == symbol && positions[i].PositionSide == side {
			target = &positions[i]
			break
		}
	}
	if target == nil || target.Size <= 0 {
		return 0, 0, fmt.Errorf("no open %s position for %s", side, symbol)
	}

	closeSide := exchange.OrderSideBuy
	if side == "LONG" {
		closeSide = exchange.OrderSideSell
	}
	clientOrderID := fmt.Sprintf("api-close-%d", time.Now().UnixNano())
	fill, err := e.broker.orderClient.PlaceMarketOrder(ctx, symbol, closeSide, side, target.Size, clientOrderID)
	if err != nil {
		return 0, 0, fmt.Errorf("place close order: %w", err)
	}
	e.log.Info("API close-position executed",
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("qty", target.Size),
		zap.Float64("avg_price", fill.AvgPrice),
		zap.String("exchange_id", fill.ExchangeID))
	return target.Size, fill.AvgPrice, nil
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
	// Prefer exchange-cached equity (from WS ACCOUNT_UPDATE) — this includes
	// ALL positions' unrealized PnL, not just engine-tracked ones.
	if eq := pv.broker.Equity(); eq > 0 {
		return eq
	}
	unrealized := pv.positions.TotalUnrealizedPnL(prices)
	return pv.broker.WalletBalance() + unrealized
}
