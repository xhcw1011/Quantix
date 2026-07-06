package live

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/bus"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/monitor"
	"github.com/Quantix/quantix/internal/notify"
	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/orgateway"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/risk"
	"github.com/Quantix/quantix/internal/strategy"
)

// EngineConfig holds live engine parameters.
type EngineConfig struct {
	StrategyID     string
	Symbol         string  // the single symbol this engine trades; used to ignore other engines' fills on a shared account
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
	OnFill   func(userID int, fill *data.Fill)       // called after each DB-persisted fill
	OnEquity func(userID int, equity float64)        // called after each equity snapshot
	OnStatus func(userID int, status map[string]any) // called after each periodic status print

	// SkipCleanSlate skips cancelling all exchange orders on startup.
	// Set to true when user has manual positions/orders that should not be touched.
	SkipCleanSlate bool
}

// Engine drives live trading:
// closed klines → strategy.OnBar → LiveBroker → Binance order → OMS fill → portfolio update.
type Engine struct {
	cfg       EngineConfig
	broker    *Broker
	positions *oms.PositionManager
	omsInst   *oms.OMS
	risk      *risk.Manager
	org       *orgateway.Gateway // Order Risk Gateway (Layer-1 validation); shadow in V1
	strategy  strategy.Strategy
	stratCtx  *strategy.Context
	bus       *bus.Bus                // may be nil
	metrics   *monitor.TradingMetrics // may be nil
	notifier  *notify.Notifier        // may be nil
	marginMon *MarginMonitor          // may be nil; active only for futures/swap exchanges
	tickCh    chan float64            // real-time price from ticker WS
	log       *zap.Logger

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
	startTime                time.Time
	dbWg                     sync.WaitGroup     // tracks in-flight DB write goroutines for clean shutdown
	stratFillCh              chan strategy.Fill // routes OnFill to the main goroutine (eliminates data race)

	// Exchange interfaces (for futures — margin query and equity cache)
	marginQuerier    exchange.MarginQuerier
	equityQuerier    exchange.EquityQuerier
	lastEquityQuery  time.Time
	cachedEquityBits atomic.Uint64 // float64 stored as bits for lock-free access

	// Non-trade balance adjustment (transfer/deposit/funding fee)
	equityAdjusted atomic.Value // float64; set by UDS goroutine, consumed by bar goroutine

	// Stale bar detection
	lastBarTime  time.Time // last time a kline was received
	staleAlerted bool      // avoid repeated stale alerts

	// Position syncer (Redis-backed, exchange is source of truth)
	posSyncer *position.Syncer // nil if not configured
}

const (
	// orgMaxOrdersPerMin is the ORG order-rate guard: a runaway-loop backstop set
	// far above normal trading (a grid may fire several orders on one bar; that is
	// fine — this only trips on sustained runaway).
	orgMaxOrdersPerMin = 120
	// orgMaxNotionalPerOrder is an absolute fat-finger cap on one order's notional;
	// 0 = disabled until a per-deployment value is chosen.
	orgMaxNotionalPerOrder = 0
)

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

	// Order Risk Gateway (ORG): every strategy order passes through it before the
	// broker. It is Layer 1 only — pure ORDER SAFETY (is this single order safe),
	// strategy- and portfolio-agnostic: max leverage, max notional per order, and an
	// order-rate limit. Portfolio-relative caps (position %, single-trade %, account
	// drawdown) deliberately do NOT live here — they belong in the portfolio/account
	// engine, which decides per-strategy exposure; putting them in the per-strategy
	// order gateway wrongly kills single-symbol strategies. Runs in Shadow (evaluate
	// + log + count, never block) until validated, then Enforce.
	org := orgateway.New(
		broker,
		[]orgateway.Rule{
			orgateway.MaxGrossLeverageRule{Frac: defaultMaxGrossExposureFrac},
			orgateway.MaxNotionalPerOrderRule{Max: orgMaxNotionalPerOrder},
			&orgateway.OrderRateRule{Max: orgMaxOrdersPerMin, Window: time.Minute},
		},
		&orgLiveState{broker: broker, positions: pm, risk: rm},
		orgateway.Shadow,
		log,
	)

	stratCtx := strategy.NewContext(
		&livePortfolioView{broker: broker, positions: pm},
		org,
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
		org:           org,
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

// fillForOtherSymbol reports whether a user-data-stream fill belongs to a
// different symbol than this engine trades. The UDS is account-wide, so when two
// engines share one exchange account each sees the other's fills as "unmatched"
// and would double-count their cash via applyUnmatchedFillCash. Ignore fills for
// other symbols. Empty fillSymbol or engineSymbol (unknown) is treated as
// belonging, so a legit fill is never dropped when the symbol isn't populated.
func fillForOtherSymbol(fillSymbol, engineSymbol string) bool {
	return fillSymbol != "" && engineSymbol != "" && fillSymbol != engineSymbol
}

// watchdogStaleThreshold is how long the engine tolerates no bar activity before
// the watchdog stops it. It scales with the traded interval (3× — i.e. three
// missed bars) so a slow feed like 15m isn't killed between healthy bars, with a
// 10-minute floor for fast/unknown intervals.
func watchdogStaleThreshold(barInterval time.Duration) time.Duration {
	const floor = 10 * time.Minute
	if t := 3 * barInterval; t > floor {
		return t
	}
	return floor
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

// ─── orgLiveState (Order Risk Gateway state provider) ───────────────────────────

// orgLiveState feeds the ORG the live account/market snapshot for each order.
// It reads dynamically at decision time: leverage/gross come from the broker's
// exposure guard, which is wired in before any order flows.
type orgLiveState struct {
	broker    *Broker
	positions *oms.PositionManager
	risk      *risk.Manager // for account peak equity (Layer 3 drawdown rule)
}

func (s *orgLiveState) Snapshot(req strategy.OrderRequest) orgateway.OrderState {
	price := s.broker.LastPrice()
	var posVal float64
	if pos, ok := s.positions.Position(req.Symbol); ok {
		posVal = math.Abs(pos.Qty) * price
	}
	return orgateway.OrderState{
		Equity:         s.broker.Equity(),
		PositionValue:  posVal,
		GrossNotional:  s.broker.GrossQty() * price,
		Price:          price,
		Leverage:       float64(s.broker.MaxLeverage()),
		Now:            time.Now(),
		PeakEquity:     s.risk.PeakEquity(),
		DayStartEquity: s.broker.DayStartEquity(),
	}
}
