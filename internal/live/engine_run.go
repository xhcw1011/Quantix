package live

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/strategy"
)

// Run starts the live trading loop. Reads closed klines from klineCh.
func (e *Engine) Run(ctx context.Context, klineCh <-chan exchange.Kline) error {
	e.startTime = time.Now()
	e.lastBarTime = time.Now()
	// Self-cancelable context so the watchdog can stop THIS engine only, instead
	// of os.Exit(1) killing the whole process (and every other healthy engine).
	ctx, selfCancel := context.WithCancel(ctx)
	defer selfCancel()
	// Capture starting equity as baseline for the first daily summary's % return.
	e.fillMu.Lock()
	e.dailyBaselineEquity = e.broker.Equity()
	e.broker.SetDayStartEquity(e.dailyBaselineEquity) // feed ORG daily-loss rule
	e.dailyBaselineWins = 0
	e.dailyBaselineTotal = 0
	e.dailyBaselineRealizedPnL = 0
	e.fillMu.Unlock()
	e.broker.SetEngineCtx(ctx) // allow async order-fill pollers to use engine lifecycle ctx

	// Wire engine context into the staged exit adapter.
	if adapter, ok := e.stratCtx.Extra["staged_exit"].(*stagedExitAdapter); ok {
		adapter.ctx = ctx
	}
	e.omsInst.SetContext(ctx) // enable backpressure on fills/orders channels

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
	// Threshold scales with the traded interval so a slow (e.g. 15m) feed isn't
	// killed between healthy bars; the status warning (2× interval) fires first.
	staleKill := watchdogStaleThreshold(e.cfg.BarInterval)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				elapsed := time.Since(e.lastBarTime)
				if elapsed > staleKill {
					e.log.Error("WATCHDOG: no bar activity past stale threshold — stopping THIS engine only (feed likely dead); process + other engines keep running",
						zap.String("engine_id", e.cfg.StrategyID),
						zap.Int("user_id", e.cfg.UserID),
						zap.Duration("since_last_bar", elapsed),
						zap.Duration("threshold", staleKill))
					if e.notifier != nil {
						e.notifier.SystemAlert("CRITICAL", fmt.Sprintf(
							"engine %s (user %d) stopped: no market data for %s — restart it once the feed is healthy",
							e.cfg.StrategyID, e.cfg.UserID, elapsed.Truncate(time.Second)))
					}
					selfCancel() // stop only this engine, not the whole process
					return
				}
			}
		}
	}()

	// Periodic real-equity poll — feeds the circuit breaker exchange truth even
	// for exchanges with NO push account stream (e.g. OKX). Also a REST backstop
	// for Binance if its WS account stream stalls. Without this, cachedEq stays 0
	// for OKX and equity falls back to reconstructed cash, which drifts and
	// mis-fires the circuit breaker (the real-money risk #1).
	if e.equityQuerier != nil {
		pollEquity := func() {
			qCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			eq, err := e.equityQuerier.GetEquity(qCtx, "USDT")
			cancel()
			if err != nil {
				e.log.Warn("equity poll failed (keeping last cached value)", zap.Error(err))
				return
			}
			if eq <= 0 {
				return
			}
			// Sanity guard: reject only an absurd UPWARD spike vs the last good
			// equity (e.g. an exchange field that sums ALL currencies → 453k on an
			// $8k account). Compare against the last cached equity, NOT walletBalance
			// — walletBalance is reconstructed and drifts for exchanges with no real
			// wallet poll (OKX drifted to 682 vs real 7700), which wrongly rejected
			// the real equity. NEVER guard the downside: a real drawdown MUST reach
			// the circuit breaker.
			if last := math.Float64frombits(e.cachedEquityBits.Load()); last > 0 && eq > last*5 {
				e.log.Warn("equity poll: implausible upward jump vs last, ignoring",
					zap.Float64("polled_equity", eq), zap.Float64("last_equity", last))
				return
			}
			e.cachedEquityBits.Store(math.Float64bits(eq))
			e.lastEquityQuery = time.Now()
		}
		// Synchronous first poll so cachedEq holds REAL exchange equity before the
		// main bar/status loop runs — otherwise the first ticks fall back to
		// cash+local-uPnL (the broken path) and momentarily feed the breaker garbage.
		pollEquity()
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					pollEquity()
				}
			}
		}()
	}

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
			// The user-data stream is account-wide. When another engine shares this
			// exchange account, ignore its fills entirely — otherwise the unmatched-fill
			// path would double-count their cash into this engine's accounting.
			if fillForOtherSymbol(fill.Symbol, e.cfg.Symbol) {
				return
			}
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
			// Go live once the pre-buffered warmup backfill is drained (channel
			// empty after a warmup bar) or a real live bar arrives. Clearing the
			// broker warmup here lets real-time OnTick actions (guardian
			// arm-with-entry) fire immediately instead of waiting for the next
			// closed bar. One-shot.
			if !e.engineLive && (!kline.Warmup || len(klineCh) == 0) {
				e.engineLive = true
				e.broker.SetWarmup(false)
				e.stratCtx.Extra["engine_live"] = true
				e.log.Info("engine live — warmup drained, real-time entry enabled")
			}

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

	// Report this engine's state to the Capital Layer (Layer 3) each live bar.
	if e.cfg.Pool != nil && !bar.Warmup {
		e.cfg.Pool.Report(e.cfg.StrategyID, e.poolMemberState())
	}

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

	// Suppress order execution while replaying startup backfill bars, so the
	// strategy primes its indicators without trading on stale signals.
	e.broker.SetWarmup(bar.Warmup)

	e.strategy.OnBar(e.stratCtx, bar)
}
