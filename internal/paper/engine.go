package paper

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/bus"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/monitor"
	"github.com/Quantix/quantix/internal/notify"
	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/risk"
	"github.com/Quantix/quantix/internal/strategy"
)

// Config holds the paper engine parameters.
type Config struct {
	StrategyID     string
	InitialCapital float64
	FeeRate        float64
	Slippage       float64
	Leverage       int // futures leverage (e.g. 10 for 10x); 0 or 1 = no leverage (spot)
	// StatusInterval controls how often the live P&L status is printed.
	// Zero defaults to 1 minute.
	StatusInterval time.Duration

	// Optional DB persistence (set by API engine manager).
	Store        *data.Store // may be nil → no DB persistence
	UserID       int         // required when Store != nil
	CredentialID int         // stored on each OrderRecord for audit trail

	// Optional real-time push callbacks (set by API engine manager to wire WS hub).
	OnFill   func(userID int, fill *data.Fill) // called after each DB-persisted fill
	OnEquity func(userID int, equity float64)  // called after each equity snapshot
}

// Engine drives paper trading:
// live klines → strategy.OnBar → PaperBroker → OMS fill → portfolio update.
type Engine struct {
	cfg       Config
	broker    *Broker
	positions *oms.PositionManager
	omsInst   *oms.OMS
	risk      *risk.Manager
	strategy  strategy.Strategy
	stratCtx  *strategy.Context
	bus       *bus.Bus               // may be nil
	metrics   *monitor.TradingMetrics // may be nil
	notifier  *notify.Notifier        // may be nil
	log       *zap.Logger

	fillMu      sync.Mutex     // protects realizedPnL, wins, total and applyFillEvent calls
	realizedPnL float64
	wins, total int // for rolling win rate
	startTime   time.Time
	dbWg        sync.WaitGroup // tracks in-flight DB write goroutines for clean shutdown
}

// New creates a paper trading engine.
// bus, metrics, notifier are optional — pass nil to disable.
func New(
	cfg Config,
	strat strategy.Strategy,
	rm *risk.Manager,
	b *bus.Bus,
	tm *monitor.TradingMetrics,
	notif *notify.Notifier,
	log *zap.Logger,
) *Engine {
	o := oms.New(oms.ModePaper, log)
	pm := oms.NewPositionManager()

	broker := NewBroker(o, rm, pm, cfg.StrategyID, cfg.InitialCapital, cfg.FeeRate, cfg.Slippage, cfg.Leverage, log)

	stratCtx := strategy.NewContext(
		&portfolioView{broker: broker, positions: pm},
		broker,
		log,
	)

	return &Engine{
		cfg:      cfg,
		broker:   broker,
		positions: pm,
		omsInst:  o,
		risk:     rm,
		strategy: strat,
		stratCtx: stratCtx,
		bus:      b,
		metrics:  tm,
		notifier: notif,
		log:      log,
	}
}

// Run starts the paper trading loop. It reads closed klines from klineCh
// and processes them until ctx is cancelled.
func (e *Engine) Run(ctx context.Context, klineCh <-chan exchange.Kline) error {
	e.startTime = time.Now()
	e.omsInst.SetContext(ctx) // enable backpressure on fills/orders channels
	statusInterval := e.cfg.StatusInterval
	if statusInterval == 0 {
		statusInterval = time.Minute
	}

	statusTicker := time.NewTicker(statusInterval)
	defer statusTicker.Stop()

	dailyTicker := time.NewTicker(24 * time.Hour)
	defer dailyTicker.Stop()

	// Restore cash/equity, positions, and pending orders from DB before starting.
	recoveryCtx, recoveryCancel := context.WithTimeout(ctx, 60*time.Second)
	e.recoverFromDB(recoveryCtx)
	recoveryCancel()

	go e.processFills(ctx)
	go e.persistOrdersLoop(ctx)

	e.log.Info("paper trading started",
		zap.String("strategy", e.strategy.Name()),
		zap.String("id", e.cfg.StrategyID),
		zap.Float64("capital", e.cfg.InitialCapital),
	)

	if e.notifier != nil {
		e.notifier.SystemAlert("INFO", fmt.Sprintf(
			"Quantix paper trading started\nStrategy: %s | Capital: $%.2f",
			e.strategy.Name(), e.cfg.InitialCapital,
		))
	}

	for {
		select {
		case <-ctx.Done():
			// Wait for in-flight DB writes to complete before exiting.
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
					"Quantix stopped\n%s", e.Summary(),
				))
			}
			e.log.Info("paper trading stopped")
			return nil

		case kline, ok := <-klineCh:
			if !ok {
				// klineCh closed (WS disconnect) — nil-ify so select no longer polls
				// this case (prevents CPU busy loop). Engine stays alive on ctx/ticker.
				e.log.Warn("kline channel closed, disabling kline select case")
				klineCh = nil
				continue
			}
			e.onBar(kline)

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

// onBar processes a closed kline through the strategy.
func (e *Engine) onBar(bar exchange.Kline) {
	e.broker.SetLastPrice(bar.Close)

	// Check deferred LIMIT / STOP_MARKET / TakeProfit orders against bar's range
	e.broker.ProcessBar(bar.High, bar.Low, bar.Close)

	// Drain fills from ProcessBar (pending order triggers) before strategy runs.
	e.drainPendingFills()

	// Let strategy process the bar — it may place new market orders that produce
	// fills synchronously via PlaceOrder → executeMarket → oms.Fill.
	e.strategy.OnBar(e.stratCtx, bar)

	// Drain fills produced by strategy.OnBar (market orders) so that
	// PositionManager is up-to-date before we compute equity.
	e.drainPendingFills()

	prices := map[string]float64{bar.Symbol: bar.Close}
	unrealized := e.positions.TotalUnrealizedPnL(prices)
	holdingsValue := e.positions.TotalMarketValue(prices)
	equity := e.broker.Cash() + holdingsValue
	e.broker.SetEquity(equity)

	// Update metrics
	if e.metrics != nil {
		e.metrics.EquityGauge.WithLabelValues(e.cfg.StrategyID).Set(equity)
		e.metrics.UnrealizedPnL.WithLabelValues(e.cfg.StrategyID).Set(unrealized)
		e.metrics.OpenPositions.WithLabelValues(e.cfg.StrategyID).Set(float64(len(e.positions.All())))
		if e.risk.Halted() {
			e.metrics.RiskHalted.WithLabelValues(e.cfg.StrategyID).Set(1)
		} else {
			e.metrics.RiskHalted.WithLabelValues(e.cfg.StrategyID).Set(0)
		}
	}

	if err := e.risk.UpdateEquity(equity); err != nil {
		e.log.Error("trading halted by risk manager",
			zap.Float64("equity", equity), zap.Error(err))
		if e.notifier != nil {
			var drawdown float64
			if e.cfg.InitialCapital > 0 {
				drawdown = (1 - equity/e.cfg.InitialCapital) * 100
			}
			e.notifier.RiskAlert(e.cfg.StrategyID, err.Error(), equity, drawdown)
		}
	}
}

// drainPendingFills non-blocking drains all pending fills from the OMS channel,
// applying them to PositionManager synchronously. This ensures equity computation
// in onBar sees up-to-date holdings after PlaceOrder/ProcessBar produced fills.
func (e *Engine) drainPendingFills() {
	for {
		select {
		case event, ok := <-e.omsInst.Fills():
			if !ok {
				return
			}
			e.applyFillEvent(event)
		default:
			return
		}
	}
}

// applyFillEvent is the shared fill-processing logic used by both drainPendingFills
// (synchronous, in onBar goroutine) and processFills (asynchronous, in background goroutine).
func (e *Engine) applyFillEvent(event oms.FillEvent) {
	e.fillMu.Lock()
	defer e.fillMu.Unlock()

	fillTime := time.Now()
	realized := e.positions.ApplyFill(event.Fill)
	e.realizedPnL += realized
	e.total++
	if realized > 0 {
		e.wins++
	}

	// For futures positions (LONG/SHORT), applyCashForFill only returns margin.
	// Add realized PnL to cash here, after ApplyFill has computed it.
	ps := string(event.Fill.PositionSide)
	isClosingLong := ps == string(strategy.PositionSideLong) && event.Fill.Side == strategy.SideSell
	isClosingShort := ps == string(strategy.PositionSideShort) && event.Fill.Side == strategy.SideBuy
	if (isClosingLong || isClosingShort) && realized != 0 {
		e.broker.SetCash(e.broker.Cash() + realized)
	}

	prices := map[string]float64{event.Fill.Symbol: event.Fill.Price}
	holdingsValue := e.positions.TotalMarketValue(prices)
	equity := e.broker.Cash() + holdingsValue
	e.broker.SetEquity(equity)

	latencyMs := float64(fillTime.Sub(event.Fill.Timestamp).Milliseconds())

	if e.metrics != nil {
		e.metrics.RealizedPnL.WithLabelValues(e.cfg.StrategyID).Set(e.realizedPnL)
		e.metrics.FillsTotal.WithLabelValues(
			e.cfg.StrategyID, event.Fill.Symbol, string(event.Fill.Side), "paper",
		).Inc()
		e.metrics.TradeLatency.WithLabelValues(e.cfg.StrategyID).Observe(latencyMs)
		if e.total > 0 {
			e.metrics.WinRate.WithLabelValues(e.cfg.StrategyID).Set(
				float64(e.wins) / float64(e.total) * 100,
			)
		}
	}

	if e.bus != nil {
		e.bus.PublishFill(bus.FillMsg{
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

	if e.cfg.Store != nil {
		fill := &data.Fill{
			UserID:       e.cfg.UserID,
			StrategyID:   e.cfg.StrategyID,
			Symbol:       event.Fill.Symbol,
			Side:         string(event.Fill.Side),
			PositionSide: string(event.Fill.PositionSide),
			Qty:          event.Fill.Qty,
			Price:        event.Fill.Price,
			Fee:          event.Fill.Fee,
			RealizedPnL:  realized,
			Mode:         "paper",
			FilledAt:      event.Fill.Timestamp,
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

	e.log.Info("paper fill",
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

// processFills waits for engine shutdown and drains any remaining fills.
// During normal operation, fills are processed synchronously by drainPendingFills()
// inside onBar — this avoids the race where a background goroutine and onBar
// compete for the same fills channel, causing equity to be computed before
// positions are updated.
func (e *Engine) processFills(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("processFills panic recovered", zap.Any("panic", r))
		}
	}()
	// Block until engine shutdown, then drain remaining fills.
	<-ctx.Done()
	for {
		select {
		case event, ok := <-e.omsInst.Fills():
			if !ok {
				return
			}
			e.applyFillEvent(event)
		default:
			return
		}
	}
}

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
			// Drain remaining order events so final CANCELLED statuses are persisted.
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
		Mode:           "paper",
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

// recoverFromDB restores paper engine state from persisted records.
// It is called once at the start of Run() before the main loop begins.
//
//   - Restores cash/equity from the most recent equity snapshot.
//   - Reconstructs PositionManager by replaying all fills in order.
//   - Requeues PENDING orders as new broker pending orders (old DB records marked CANCELLED).
func (e *Engine) recoverFromDB(ctx context.Context) {
	if e.cfg.Store == nil {
		return
	}

	// D: Restore cash/equity from the latest equity snapshot.
	snap, err := e.cfg.Store.GetLatestEquitySnapshot(ctx, e.cfg.UserID, e.cfg.StrategyID)
	if err == nil && snap != nil {
		e.broker.SetCashEquity(snap.Cash, snap.Equity)
		e.log.Info("paper: restored cash/equity from snapshot",
			zap.Float64("cash", snap.Cash),
			zap.Float64("equity", snap.Equity),
			zap.Time("as_of", snap.SnapshottedAt),
		)
	}

	// D: Reconstruct PositionManager by replaying fills in chronological order.
	fills, err := e.cfg.Store.GetAllFillsForStrategy(ctx, e.cfg.UserID, e.cfg.StrategyID, "paper")
	if err == nil && len(fills) > 0 {
		pm := oms.NewPositionManager()
		for _, f := range fills {
			pm.ApplyFill(strategy.Fill{
				Symbol:       f.Symbol,
				Side:         strategy.Side(f.Side),
				PositionSide: strategy.PositionSide(f.PositionSide),
				Qty:          f.Qty,
				Price:        f.Price,
				Fee:          f.Fee,
			})
		}
		e.broker.SetPositions(pm)
		e.log.Info("paper: reconstructed positions from fills", zap.Int("fill_count", len(fills)))
	}

	// B: Requeue PENDING orders as new broker pending orders.
	// Old DB records are marked CANCELLED; new OMS orders get fresh client_order_ids.
	active, err := e.cfg.Store.GetActiveOrders(ctx, e.cfg.UserID, e.cfg.StrategyID)
	if err != nil {
		e.log.Warn("paper: failed to load active orders for recovery", zap.Error(err))
		return
	}
	restored := 0
	for _, rec := range active {
		// Mark old DB record as CANCELLED before re-queuing (avoids index conflict).
		oldRec := *rec
		oldRec.Status = "CANCELLED"
		dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		e.cfg.Store.UpsertOrder(dbCtx, &oldRec) //nolint:errcheck
		cancel()

		if rec.Status != "PENDING" {
			// OPEN/PARTIAL paper orders shouldn't exist; cancel and move on.
			continue
		}
		if err := e.broker.RestorePendingOrder(rec); err != nil {
			e.log.Warn("paper: failed to restore pending order",
				zap.String("client_order_id", rec.ClientOrderID),
				zap.Error(err))
			continue
		}
		restored++
	}
	if len(active) > 0 {
		e.log.Info("paper: recovered pending orders",
			zap.Int("restored", restored),
			zap.Int("cancelled", len(active)-restored),
		)
	}
}

// publishStatus sends a StatusMsg to NATS.
func (e *Engine) publishStatus() {
	if e.bus == nil {
		return
	}
	e.fillMu.Lock()
	rpnl := e.realizedPnL
	e.fillMu.Unlock()

	equity := e.broker.Equity()
	e.bus.PublishStatus(bus.StatusMsg{ //nolint:errcheck
		StrategyID:     e.cfg.StrategyID,
		Cash:           e.broker.Cash(),
		Equity:         equity,
		RealizedPnL:    rpnl,
		TotalReturnPct: safePctReturn(equity, e.cfg.InitialCapital),
		OpenPositions:  len(e.positions.All()),
		RiskHalted:     e.risk.Halted(),
	})
}

// printStatus logs the current P&L snapshot.
func (e *Engine) printStatus() {
	e.fillMu.Lock()
	rpnl := e.realizedPnL
	e.fillMu.Unlock()

	positions := e.positions.All()
	cash := e.broker.Cash()
	equity := e.broker.Equity()
	totalReturn := safePctReturn(equity, e.cfg.InitialCapital)
	elapsed := time.Since(e.startTime).Truncate(time.Second)

	e.log.Info("──── Paper Trading Status ────",
		zap.Duration("uptime", elapsed),
		zap.Float64("initial_capital", e.cfg.InitialCapital),
		zap.Float64("cash", cash),
		zap.Float64("equity", equity),
		zap.Float64("total_return_pct", totalReturn),
		zap.Float64("realized_pnl", rpnl),
		zap.Int("open_positions", len(positions)),
		zap.Bool("risk_halted", e.risk.Halted()),
	)

	for _, pos := range positions {
		e.log.Info("  position",
			zap.String("symbol", pos.Symbol),
			zap.Float64("qty", pos.Qty),
			zap.Float64("avg_entry", pos.AvgEntryPrice),
		)
	}

	orders := e.omsInst.All()
	filled := 0
	for _, o := range orders {
		if o.Status == oms.StatusFilled {
			filled++
		}
	}
	e.log.Info("  orders",
		zap.Int("total", len(orders)),
		zap.Int("filled", filled),
	)
}

func (e *Engine) sendDailySummary() {
	if e.notifier == nil {
		return
	}
	e.fillMu.Lock()
	rpnl, total, wins := e.realizedPnL, e.total, e.wins
	e.fillMu.Unlock()

	equity := e.broker.Equity()
	ret := safePctReturn(equity, e.cfg.InitialCapital)
	e.notifier.DailySummary(e.cfg.StrategyID, equity, rpnl, ret, total, wins)
}

func (e *Engine) persistEquitySnapshot() {
	if e.cfg.Store == nil {
		return
	}
	equity := e.broker.Equity()
	e.fillMu.Lock()
	rpnl := e.realizedPnL
	e.fillMu.Unlock()

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
		if err := e.cfg.Store.InsertEquitySnapshot(ctx, snap); err != nil {
			e.log.Error("persist equity snapshot failed", zap.Error(err))
		}
		if onEquity != nil {
			onEquity(userID, equity)
		}
	}()
}

// Positions returns a copy of all currently open positions.
func (e *Engine) Positions() []oms.LivePosition { return e.positions.All() }

// LastPrice returns the close price of the most recently processed kline.
func (e *Engine) LastPrice() float64 {
	return safeLoadFloat64(&e.broker.lastPrice)
}

// Cash returns the current available cash balance.
func (e *Engine) Cash() float64 { return e.broker.Cash() }

// Equity returns the current total equity.
func (e *Engine) Equity() float64 { return e.broker.Equity() }

// Summary returns a one-line result string.
// safePctReturn computes percentage return, returning 0 if initial is zero.
func safePctReturn(equity, initial float64) float64 {
	if initial <= 0 {
		return 0
	}
	return (equity/initial - 1) * 100
}

func (e *Engine) Summary() string {
	e.fillMu.Lock()
	rpnl := e.realizedPnL
	e.fillMu.Unlock()

	equity := e.broker.Equity()
	ret := safePctReturn(equity, e.cfg.InitialCapital)
	orders := e.omsInst.All()
	filled := 0
	for _, o := range orders {
		if o.Status == oms.StatusFilled {
			filled++
		}
	}
	return fmt.Sprintf(
		"Paper Trading Summary | Strategy: %s | Capital: $%.2f → $%.2f (%.2f%%) | "+
			"Realized PnL: $%.2f | Filled orders: %d | Duration: %s",
		e.strategy.Name(),
		e.cfg.InitialCapital, equity, ret,
		rpnl,
		filled,
		time.Since(e.startTime).Truncate(time.Second),
	)
}

// ─── portfolioView ────────────────────────────────────────────────────────────

type portfolioView struct {
	broker    *Broker
	positions *oms.PositionManager
}

func (pv *portfolioView) Cash() float64 { return pv.broker.Cash() }

func (pv *portfolioView) Position(symbol string) (qty, avgPrice float64, ok bool) {
	pos, exists := pv.positions.Position(symbol)
	if !exists {
		return 0, 0, false
	}
	return pos.Qty, pos.AvgEntryPrice, true
}

func (pv *portfolioView) Equity(prices map[string]float64) float64 {
	unrealized := pv.positions.TotalUnrealizedPnL(prices)
	return pv.broker.Cash() + unrealized
}
