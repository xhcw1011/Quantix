package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/position"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	xfactory "github.com/Quantix/quantix/internal/exchange/factory"
	"github.com/Quantix/quantix/internal/guardian"
	"github.com/Quantix/quantix/internal/live"
	"github.com/Quantix/quantix/internal/monitor"
	"github.com/Quantix/quantix/internal/notify"
	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/paper"
	"github.com/Quantix/quantix/internal/pool"
	"github.com/Quantix/quantix/internal/risk"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// RiskOverride allows per-engine risk parameter customization.
type RiskOverride struct {
	MaxPositionPct   float64 `json:"max_position_pct"`
	MaxDrawdownPct   float64 `json:"max_drawdown_pct"`
	MaxSingleLossPct float64 `json:"max_single_loss_pct"`
}

// PaperConfig holds paper-trading-specific parameters.
type PaperConfig struct {
	InitialCapital float64 `json:"initial_capital"` // default 10000
	FeeRate        float64 `json:"fee_rate"`        // default 0.001
	Slippage       float64 `json:"slippage"`        // default 0.0005
}

// StartRequest contains parameters to start a live or paper engine for a user.
type StartRequest struct {
	CredentialID int            `json:"credential_id"`
	StrategyID   string         `json:"strategy_id"`
	Symbol       string         `json:"symbol"`
	Interval     string         `json:"interval"`
	Intervals    []string       `json:"intervals,omitempty"` // multi-timeframe: e.g. ["1m","5m","15m"]
	Mode         string         `json:"mode"`                // "live" (default) | "paper"
	Leverage     int            `json:"leverage"`            // 1–20; 0 = use exchange default (futures/swap only)
	ContractMode string         `json:"contract_mode"`       // "hedge" | "one_way" (futures/swap only)
	Params       map[string]any `json:"params,omitempty"`    // extra strategy parameters
	Risk         *RiskOverride  `json:"risk,omitempty"`
	Paper        *PaperConfig   `json:"paper,omitempty"` // required when mode=paper
	// ConfirmLive must be true when mode is "live". This is a safety gate to
	// prevent accidental live trading from scripts or UI bugs. Paper mode
	// does not require this field. Omitting it for a live request returns 400.
	ConfirmLive bool `json:"confirm_live,omitempty"`
}

// EngineInfo describes a running or stopped engine.
type EngineInfo struct {
	EngineID   string `json:"engine_id"`
	UserID     int    `json:"user_id,omitempty"` // populated in admin views
	StrategyID string `json:"strategy_id"`
	Symbol     string `json:"symbol"`
	Interval   string `json:"interval"`
	Mode       string `json:"mode"` // "live" | "paper"
	Leverage   int    `json:"leverage,omitempty"`
	Running    bool   `json:"running"`
	StartedAt  string `json:"started_at"`
	Error      string `json:"error,omitempty"`
}

// EngineStatus is kept for backward-compat with the old /api/engine/status endpoint.
type EngineStatus = EngineInfo

type runningEngine struct {
	engine     *live.Engine  // set for live mode
	paperEng   *paper.Engine // set for paper mode
	cancel     context.CancelFunc
	engineID   string
	userID     int
	strategyID string
	symbol     string
	interval   string
	mode       string
	leverage   int
	startedAt  time.Time
	lastErr    error
	done       chan struct{}
}

// PositionView is an API-facing snapshot of a single open position,
// with UnrealizedPnL pre-computed using the engine's last known price.
type PositionView struct {
	Symbol        string  `json:"symbol"`
	PositionSide  string  `json:"position_side"` // "" (net/spot), "LONG", or "SHORT"
	Qty           float64 `json:"qty"`
	AvgEntryPrice float64 `json:"avg_entry_price"`
	UnrealizedPnL float64 `json:"unrealized_pnl"`
	RealizedPnL   float64 `json:"realized_pnl"`
}

// EnginePositions bundles all open positions for a single running engine.
type EnginePositions struct {
	EngineID   string         `json:"engine_id"`
	StrategyID string         `json:"strategy_id"`
	Symbol     string         `json:"symbol"`
	Mode       string         `json:"mode"`
	LastPrice  float64        `json:"last_price"`
	Cash       float64        `json:"cash"`
	Equity     float64        `json:"equity"`
	Positions  []PositionView `json:"positions"`
}

// EngineManager manages multiple live/paper trading engines per user.
// The map is keyed by userID → engineID → engine.
type EngineManager struct {
	mu      sync.RWMutex
	engines map[int]map[string]*runningEngine
	pools   map[int]*pool.Manager // per-user Capital Layer (Layer 3), lazily created

	store   *data.Store
	enc     *Encryptor
	smtpCfg config.SMTPConfig
	wsHub   *WSHub
	log     *zap.Logger

	// Per-engine default configuration (read from config file).
	riskDef     config.RiskConfig
	paperDef    config.PaperConfig
	backfillLim int
	monitorCfg  config.MonitorConfig
	liveEnabled bool
	openaiCfg   config.OpenAIConfig
	wsCfg       config.WSConfig
	redis       *redis.Client // nil if Redis not configured

	// binanceNetworkMode tracks the first Binance network mode seen in this process.
	// go-binance uses package-level globals (UseTestnet/UseDemo), so all Binance brokers
	// in the same process MUST use the same mode. Empty = not yet set.
	binanceNetworkMode string // "", "demo", "testnet", "live"
}

// NewEngineManager creates a manager backed by the given store.
// smtpCfg is optional; pass a zero-value to disable email notifications.
// wsHub is used to push real-time fill/equity events to connected frontend clients.
// cfg provides per-engine defaults (risk limits, paper trading params, backfill limit).
func NewEngineManager(store *data.Store, enc *Encryptor, smtpCfg config.SMTPConfig, wsHub *WSHub, cfg *config.Config, rdb *redis.Client, log *zap.Logger) *EngineManager {
	backfill := cfg.Data.BackfillLimit
	if backfill <= 0 {
		backfill = 500
	}
	return &EngineManager{
		engines:     make(map[int]map[string]*runningEngine),
		pools:       make(map[int]*pool.Manager),
		store:       store,
		enc:         enc,
		smtpCfg:     smtpCfg,
		wsHub:       wsHub,
		log:         log,
		riskDef:     cfg.Risk,
		paperDef:    cfg.Paper,
		backfillLim: backfill,
		monitorCfg:  cfg.Monitor,
		liveEnabled: cfg.Live.Enabled,
		openaiCfg:   cfg.OpenAI,
		wsCfg:       cfg.WS,
		redis:       rdb,
	}
}

// LiveEnabled reports whether the live-trading kill-switch is on.
// Used by the health/ready endpoint.
func (m *EngineManager) LiveEnabled() bool { return m.liveEnabled }

// poolManagerFor returns the user's shared Capital-Layer PoolManager, creating it on
// first use. Caller MUST hold m.mu (the engine-create path already does). The
// NotionalCap / thresholds are v1 shadow placeholders pending the deferred capital
// allocation.
func (m *EngineManager) poolManagerFor(userID int) *pool.Manager {
	if pm, ok := m.pools[userID]; ok {
		return pm
	}
	pm := pool.NewManager([]pool.Config{
		{Name: "Growth", NotionalCap: 10000, MaxDrawdown: 0.25, RecoverDrawdown: 0.15, RecoverBars: 5, MaxLongExp: 1.5, MaxShortExp: 1.5},
		{Name: "Yield", NotionalCap: 10000, MaxDrawdown: 0.15, RecoverDrawdown: 0.08, RecoverBars: 5, MaxLongExp: 1.2, MaxShortExp: 0.5},
		{Name: "Cash", NotionalCap: 0},
		{Name: "default", NotionalCap: 10000, MaxDrawdown: 0.30, RecoverDrawdown: 0.20, RecoverBars: 5, MaxLongExp: 2.0, MaxShortExp: 2.0},
	}, nil, m.log)
	m.pools[userID] = pm
	return pm
}

// poolForStrategy maps a strategy to its behavior-family pool. MeanReversion floats
// into Growth for now (migratable later); unclassified strategies (ai, composite,
// ml) go to a loose default pool.
func poolForStrategy(strategyID string) string {
	switch strategyID {
	case "macross", "spottrend", "meanreversion":
		return "Growth"
	case "grid", "dca", "dipdca", "spotgrid", "rebalance":
		return "Yield"
	default:
		return "default"
	}
}

// Start creates and runs a live or paper engine for the user.
// Returns the engineID on success.
func (m *EngineManager) Start(userID int, req StartRequest) (string, error) {
	if req.Mode == "" {
		req.Mode = "live"
	}

	// ── Pre-flight safety gates ──────────────────────────────────────────
	// 1. Validate mode enum
	if req.Mode != "live" && req.Mode != "paper" {
		return "", fmt.Errorf("mode must be \"live\" or \"paper\" (got %q)", req.Mode)
	}
	// 2. Kill-switch: reject live starts when live trading is disabled in config/env.
	if req.Mode == "live" && !m.liveEnabled {
		return "", fmt.Errorf("live trading is disabled (set live.enabled=true in config or QUANTIX_LIVE_ENABLED=true)")
	}
	// 3. Explicit confirmation gate for live mode (prevents accidental live starts).
	if req.Mode == "live" && !req.ConfirmLive {
		return "", fmt.Errorf("confirm_live must be true to start a live engine")
	}
	// 3b. Environment-level confirmation: QUANTIX_LIVE_CONFIRM=true must be set
	// in the process environment. This is a fail-closed safety net so that even
	// if the config flag and JSON field are both set, the operator must also
	// explicitly opt in at the OS level before any exchange connectivity occurs.
	if req.Mode == "live" && os.Getenv("QUANTIX_LIVE_CONFIRM") != "true" {
		return "", fmt.Errorf("live trading blocked: set QUANTIX_LIVE_CONFIRM=true in the environment to allow live engine starts")
	}
	// 3c. Normalize intervals: if Intervals is empty, use Interval as single-element list.
	// Interval (primary) is authoritative for engineID and strategy params.
	if len(req.Intervals) == 0 && req.Interval != "" {
		req.Intervals = []string{req.Interval}
	}
	if req.Interval == "" && len(req.Intervals) > 0 {
		req.Interval = req.Intervals[0]
	}
	// Merge strategy params.Intervals into req.Intervals for WS subscription.
	// Strategy may need multi-timeframe data (e.g., 15m for regime detection) —
	// WS must subscribe to all needed intervals, not just primary.
	if paramIntervals, ok := req.Params["Intervals"].([]any); ok {
		existing := make(map[string]bool)
		for _, iv := range req.Intervals {
			existing[iv] = true
		}
		for _, iv := range paramIntervals {
			if s, ok := iv.(string); ok && !existing[s] {
				req.Intervals = append(req.Intervals, s)
				existing[s] = true
			}
		}
	}
	if paramIntervals, ok := req.Params["Intervals"].([]string); ok {
		existing := make(map[string]bool)
		for _, iv := range req.Intervals {
			existing[iv] = true
		}
		for _, iv := range paramIntervals {
			if !existing[iv] {
				req.Intervals = append(req.Intervals, iv)
				existing[iv] = true
			}
		}
	}

	// 4. Validate strategy exists before credential lookup to fail fast.
	if !registry.Exists(req.StrategyID) {
		return "", fmt.Errorf("strategy %q not registered; available: %v", req.StrategyID, registry.Names())
	}

	engineID := fmt.Sprintf("%s-%s-%s", req.Symbol, req.Interval, req.StrategyID)

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.engines[userID] == nil {
		m.engines[userID] = make(map[string]*runningEngine)
	}

	// Check if same engine is already running
	if re, ok := m.engines[userID][engineID]; ok {
		select {
		case <-re.done:
			delete(m.engines[userID], engineID)
		default:
			return "", fmt.Errorf("engine %q already running", engineID)
		}
	}

	// Fetch + decrypt credentials (needed for market data in both modes)
	credCtx, credCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer credCancel()
	cred, err := m.store.GetCredentialByID(credCtx, req.CredentialID, userID)
	if err != nil {
		return "", fmt.Errorf("get credential: %w", err)
	}
	apiKey, err := m.enc.Decrypt(cred.APIKey)
	if err != nil {
		return "", fmt.Errorf("decrypt api_key: %w", err)
	}
	apiSecret, err := m.enc.Decrypt(cred.APISecret)
	if err != nil {
		return "", fmt.Errorf("decrypt api_secret: %w", err)
	}
	passphrase := ""
	if cred.Passphrase != "" {
		passphrase, err = m.enc.Decrypt(cred.Passphrase)
		if err != nil {
			return "", fmt.Errorf("decrypt passphrase: %w", err)
		}
	}

	// Build exchange config
	excCfg := config.ExchangeConfig{Active: cred.Exchange}
	switch cred.Exchange {
	case "binance":
		// go-binance uses package-level globals (UseTestnet/UseDemo), so all Binance
		// brokers in the same process MUST use the same network mode.
		// NOTE: m.mu is already held by the caller (Start locks at entry).
		credMode := "live"
		if cred.Demo {
			credMode = "demo"
		} else if cred.Testnet {
			credMode = "testnet"
		}
		if m.binanceNetworkMode == "" {
			m.binanceNetworkMode = credMode
		} else if m.binanceNetworkMode != credMode {
			return "", fmt.Errorf("all Binance engines must use the same network; first engine set %s mode, this credential is %s — cannot mix", m.binanceNetworkMode, credMode)
		}

		excCfg.Binance = config.BinanceConfig{
			APIKey: apiKey, APISecret: apiSecret,
			Testnet: cred.Testnet, Demo: cred.Demo, MarketType: cred.MarketType,
		}
		// Spot does not support leverage — reject early to prevent confusing exchange errors.
		if cred.MarketType == "spot" && req.Leverage > 1 {
			return "", fmt.Errorf("leverage is not supported for spot trading (got %d); set leverage to 0 or 1", req.Leverage)
		}
	case "okx":
		excCfg.OKX = config.OKXConfig{
			APIKey: apiKey, APISecret: apiSecret, Passphrase: passphrase,
			Demo: cred.Demo, MarketType: cred.MarketType,
		}
		if (cred.MarketType == "SPOT" || cred.MarketType == "spot") && req.Leverage > 1 {
			return "", fmt.Errorf("leverage is not supported for spot trading (got %d); set leverage to 0 or 1", req.Leverage)
		}
	case "bybit":
		if req.Mode != "paper" {
			return "", fmt.Errorf("bybit order execution is not yet implemented; use binance or okx for live trading (bybit supports market data only)")
		}
		excCfg.Bybit = config.BybitConfig{APIKey: apiKey, APISecret: apiSecret}
	default:
		return "", fmt.Errorf("unsupported exchange %q", cred.Exchange)
	}

	// Build strategy params (always include Symbol + Interval(s); merge user-supplied params)
	stratParams := map[string]any{
		"Symbol":    req.Symbol,
		"Interval":  req.Interval,
		"Intervals": req.Intervals,
	}
	for k, v := range req.Params {
		stratParams[k] = v
	}
	// For AI strategy: inject OpenAI config from server config if not provided in params.
	if req.StrategyID == "ai" {
		if _, hasKey := stratParams["APIKey"]; !hasKey {
			apiKey := m.openaiCfg.APIKey
			if envKey := os.Getenv("QUANTIX_OPENAI_API_KEY"); envKey != "" {
				apiKey = envKey
			}
			if apiKey != "" {
				stratParams["APIKey"] = apiKey
			}
		}
		if _, hasModel := stratParams["Model"]; !hasModel && m.openaiCfg.Model != "" {
			stratParams["Model"] = m.openaiCfg.Model
		}
	}

	strat, err := registry.Create(req.StrategyID, stratParams, m.log)
	if err != nil {
		return "", fmt.Errorf("create strategy %q: %w", req.StrategyID, err)
	}

	// Risk manager: defaults come from config file, overridden by per-request params.
	riskCfg := risk.Config{
		MaxPositionPct:   m.riskDef.MaxPositionPct,
		MaxDrawdownPct:   m.riskDef.MaxDrawdownPct,
		MaxSingleLossPct: m.riskDef.MaxSingleLossPct,
	}
	// Apply safe minimums in case config is missing or zero.
	if riskCfg.MaxPositionPct <= 0 {
		riskCfg.MaxPositionPct = 0.10
	}
	if riskCfg.MaxDrawdownPct <= 0 {
		riskCfg.MaxDrawdownPct = 0.15
	}
	if riskCfg.MaxSingleLossPct <= 0 {
		riskCfg.MaxSingleLossPct = 0.02
	}
	if req.Risk != nil {
		if req.Risk.MaxPositionPct > 0 {
			if req.Risk.MaxPositionPct > 1.0 {
				return "", fmt.Errorf("max_position_pct must be <= 1.0 (got %.4f)", req.Risk.MaxPositionPct)
			}
			riskCfg.MaxPositionPct = req.Risk.MaxPositionPct
		}
		if req.Risk.MaxDrawdownPct > 0 {
			if req.Risk.MaxDrawdownPct > 1.0 {
				return "", fmt.Errorf("max_drawdown_pct must be <= 1.0 (got %.4f)", req.Risk.MaxDrawdownPct)
			}
			riskCfg.MaxDrawdownPct = req.Risk.MaxDrawdownPct
		}
		if req.Risk.MaxSingleLossPct > 0 {
			if req.Risk.MaxSingleLossPct > 1.0 {
				return "", fmt.Errorf("max_single_loss_pct must be <= 1.0 (got %.4f)", req.Risk.MaxSingleLossPct)
			}
			riskCfg.MaxSingleLossPct = req.Risk.MaxSingleLossPct
		}
	}
	rm := risk.New(riskCfg, 0, m.log)

	// Create REST + WS clients (used for market data in both modes)
	restClient, err := xfactory.NewRESTClient(excCfg, m.log)
	if err != nil {
		return "", fmt.Errorf("create rest client: %w", err)
	}
	wsClient, err := xfactory.NewWSClient(excCfg, m.wsCfg, m.log)
	if err != nil {
		return "", fmt.Errorf("create ws client: %w", err)
	}

	ctx, engineCancel := context.WithCancel(context.Background())
	klineCh := make(chan exchange.Kline, 2048)

	re := &runningEngine{
		cancel:     engineCancel,
		engineID:   engineID,
		userID:     userID,
		strategyID: req.StrategyID,
		symbol:     req.Symbol,
		interval:   req.Interval,
		mode:       req.Mode,
		leverage:   req.Leverage,
		startedAt:  time.Now(),
		done:       make(chan struct{}),
	}

	// Build notifier: load user's per-user Telegram config from DB, then optionally add email.
	notifier := m.buildNotifier(context.Background(), userID)

	// Guardian: wire the notifier for alerts, and for LIVE mode enable the
	// exchange-native resting stop (survives bot/server death) plus DB-backed
	// trail-state persistence (restores the advanced stop on restart).
	if g, ok := strat.(*guardian.Guardian); ok {
		g.SetDispatcher(guardian.NewNotifyDispatcher(notifier))
		if req.Mode == "live" {
			g.SetRestingStop(true)
			g.SetStateStore(&guardianStateStore{store: m.store}, engineID)
		}
	}

	if req.Mode == "paper" {
		// Paper mode — no real order placement.
		// Defaults come from config file; overridden by per-request Paper params.
		initCap := m.paperDef.InitialCapital
		if initCap <= 0 {
			initCap = 10000
		}
		feeRate := m.paperDef.FeeRate
		if feeRate <= 0 {
			feeRate = 0.001
		}
		slippage := m.paperDef.Slippage
		// slippage may legitimately be 0 (no slippage simulation); keep as-is.

		paperCfg := paper.Config{
			StrategyID:     engineID,
			InitialCapital: initCap,
			FeeRate:        feeRate,
			Slippage:       slippage,
			Leverage:       req.Leverage,
			StatusInterval: time.Minute,
			Store:          m.store,
			UserID:         userID,
			CredentialID:   req.CredentialID,
		}
		if req.Paper != nil {
			if req.Paper.InitialCapital > 0 {
				paperCfg.InitialCapital = req.Paper.InitialCapital
			}
			if req.Paper.FeeRate > 0 {
				paperCfg.FeeRate = req.Paper.FeeRate
			}
			if req.Paper.Slippage >= 0 {
				paperCfg.Slippage = req.Paper.Slippage
			}
		}
		// Wire WebSocket real-time push callbacks.
		if m.wsHub != nil {
			hub := m.wsHub
			paperCfg.OnFill = func(uid int, f *data.Fill) {
				hub.Broadcast(uid, map[string]any{"type": "fill", "data": f})
			}
			paperCfg.OnEquity = func(uid int, eq float64) {
				hub.Broadcast(uid, map[string]any{"type": "equity", "equity": eq})
			}
		}
		tm := monitor.NewTradingMetrics()
		re.paperEng = paper.New(paperCfg, strat, rm, nil, tm, notifier, m.log)
	} else {
		// Live mode
		orderClient, err := xfactory.NewOrderClient(excCfg, m.log)
		if err != nil {
			engineCancel()
			return "", fmt.Errorf("create order client: %w", err)
		}

		// Validate and configure leverage for futures/swap exchanges
		if req.Leverage < 0 || req.Leverage > 125 {
			engineCancel()
			return "", fmt.Errorf("leverage must be between 0 and 125 (got %d)", req.Leverage)
		}
		// Futures requires explicit leverage. Without it, the engine's internal cash
		// accounting falls back to leverage=1 (spot semantics), making cash field
		// log values wildly wrong (full notional locked instead of 1/leverage margin).
		// Real exchange-side margin is unaffected, but monitoring/log values become
		// untrustworthy. spot already rejected above (lines 307-317).
		isFutures := cred.MarketType != "spot" && cred.MarketType != "SPOT"
		if isFutures && req.Leverage <= 0 {
			engineCancel()
			return "", fmt.Errorf("leverage is required for %s/%s (got %d); futures engines need explicit leverage to compute correct margin", cred.Exchange, cred.MarketType, req.Leverage)
		}
		if req.Leverage > 0 {
			leverageCtx, leverageCancel := context.WithTimeout(context.Background(), 10*time.Second)
			lvErr := orderClient.SetLeverage(leverageCtx, req.Symbol, req.Leverage)
			leverageCancel()
			if lvErr != nil {
				engineCancel()
				return "", fmt.Errorf("set leverage to %dx: %w", req.Leverage, lvErr)
			}
			m.log.Info("leverage configured",
				zap.String("symbol", req.Symbol),
				zap.Int("leverage", req.Leverage),
			)
		}

		liveCfg := live.EngineConfig{
			StrategyID:              engineID,
			Symbol:                  req.Symbol,
			StatusInterval:          time.Minute,
			BarInterval:             parseBarInterval(req.Interval),
			Leverage:                req.Leverage,
			Store:                   m.store,
			UserID:                  userID,
			CredentialID:            req.CredentialID,
			MarginWarnThreshold:     m.monitorCfg.MarginWarnThreshold,
			MarginCriticalThreshold: m.monitorCfg.MarginCriticalThreshold,
			MarginCheckInterval:     m.monitorCfg.MarginCheckInterval,
		}
		// Wire WebSocket real-time push callbacks.
		if m.wsHub != nil {
			hub := m.wsHub
			liveCfg.OnFill = func(uid int, f *data.Fill) {
				hub.Broadcast(uid, map[string]any{"type": "fill", "data": f})
			}
			liveCfg.OnEquity = func(uid int, eq float64) {
				hub.Broadcast(uid, map[string]any{"type": "equity", "equity": eq})
			}
			liveCfg.OnStatus = func(uid int, status map[string]any) {
				hub.Broadcast(uid, map[string]any{"type": "status", "data": status})
			}
		}
		// Capital Layer (Layer 3): attach this engine to its behavior-family pool in
		// the user's shared PoolManager. Shadow — the ORG's PoolGateRule only logs.
		userPool := m.poolManagerFor(userID)
		userPool.Assign(engineID, poolForStrategy(req.StrategyID))
		liveCfg.Pool = userPool

		tm := monitor.NewTradingMetrics()
		eng, err := live.NewEngine(liveCfg, strat, rm, nil, tm, notifier, orderClient, m.log)
		if err != nil {
			engineCancel()
			return "", fmt.Errorf("create live engine: %w", err)
		}
		syncCtx, syncCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer syncCancel()
		if err := eng.SyncBalance(syncCtx, "USDT"); err != nil {
			engineCancel()
			return "", fmt.Errorf("sync balance: %w", err)
		}
		// Create PositionSyncer (Redis + DB backed)
		if m.redis != nil {
			posRedis := position.NewRedisStore(m.redis, userID, engineID, m.log)
			syncer := position.NewSyncer(position.SyncerConfig{
				Redis: posRedis, Store: m.store,
				UserID: userID, EngineID: engineID, Symbol: req.Symbol,
				Log: m.log,
			})
			// Load: Redis first, then verify against exchange
			syncCtx2, syncCancel2 := context.WithTimeout(context.Background(), 10*time.Second)
			if mq, ok := orderClient.(exchange.MarginQuerier); ok {
				syncer.Load(syncCtx2, mq)
			} else {
				syncer.Load(syncCtx2, nil)
			}
			syncCancel2()
			eng.SetPositionSyncer(syncer)
			if m.redis != nil {
				eng.SetExtra("redis_client", m.redis)
			}
			eng.SetExtra("data_store", m.store)
			eng.SetExtra("user_id", userID)
			eng.SetExtra("engine_id", engineID)
			m.log.Info("position syncer initialized",
				zap.Bool("has_long", syncer.HasPosition("LONG")),
				zap.Bool("has_short", syncer.HasPosition("SHORT")))
		}

		// DO NOT cancel exchange orders on restart — staged TP/SL must survive restarts.
		// Previously CancelAllOpenOrders ran here, which destroyed position protection
		// and caused cascading issues (phantom clearing, position stacking, unprotected holds).

		// Query true equity from exchange and seed risk manager + engine cache
		initEquity := eng.Cash()
		if eq, ok := orderClient.(exchange.EquityQuerier); ok {
			eqCtx, eqCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if realEq, err := eq.GetEquity(eqCtx, "USDT"); err == nil {
				initEquity = realEq
				eng.SetCachedEquity(realEq)
				m.log.Info("initial equity from exchange", zap.Float64("equity", realEq))
			}
			eqCancel()
		}
		rm.Reset(initEquity)
		re.engine = eng
	}

	m.engines[userID][engineID] = re

	go func() {
		defer close(re.done)
		defer engineCancel()

		// Backfill all intervals (longer intervals first for warmup order)
		for i := len(req.Intervals) - 1; i >= 0; i-- {
			iv := req.Intervals[i]
			klines, err := restClient.GetKlines(ctx, req.Symbol, iv, m.backfillLim)
			if err != nil {
				m.log.Warn("backfill failed", zap.String("interval", iv), zap.Error(err))
				continue
			}
			for _, k := range klines {
				k.IsClosed = true
				k.Warmup = true // prime indicators; engine suppresses orders for these
				select {
				case klineCh <- k:
				default:
				}
			}
			m.log.Info("backfill done", zap.String("interval", iv), zap.Int("bars", len(klines)))
		}

		// WS subscription — all intervals (auto-recover on unexpected exit)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				m.log.Info("kline WS: starting subscription")
				wsClient.SubscribeKlines(ctx, []string{req.Symbol}, req.Intervals,
					func(k exchange.Kline) {
						if !k.IsClosed {
							return
						}
						m.log.Info("kline WS: closed bar received",
							zap.String("symbol", k.Symbol),
							zap.String("interval", k.Interval),
							zap.Float64("close", k.Close))
						select {
						case klineCh <- k:
						default:
							m.log.Warn("kline channel full, dropping bar")
						}
					},
				)
				// SubscribeKlines returned unexpectedly — restart after delay
				select {
				case <-ctx.Done():
					return
				default:
					m.log.Warn("kline WS: subscription exited unexpectedly, restarting in 5s")
					time.Sleep(5 * time.Second)
				}
			}
		}()

		// Real-time ticker for precise TP/SL (live engine only)
		if re.engine != nil {
			tickCh := re.engine.GetTickCh()
			go wsClient.SubscribeTickers(ctx, []string{req.Symbol},
				func(t exchange.Ticker) {
					select {
					case tickCh <- t.LastPrice:
					default:
					}
				},
			)
		}

		var runErr error
		if re.paperEng != nil {
			runErr = re.paperEng.Run(ctx, klineCh)
		} else {
			runErr = re.engine.Run(ctx, klineCh)
		}
		if runErr != nil {
			m.mu.Lock()
			re.lastErr = runErr
			m.mu.Unlock()
			m.log.Error("engine stopped with error",
				zap.Int("user_id", userID),
				zap.String("engine_id", engineID),
				zap.String("mode", req.Mode),
				zap.Error(runErr),
			)
		}
	}()

	// Persist session synchronously so auto-restart can recover this engine if we crash.
	// This must happen after the engine goroutine is launched (so engineID is valid)
	// but we accept the small risk of session persisting for an engine that immediately
	// fails, since AutoRestart handles that gracefully.
	if m.store != nil {
		reqJSON, err := json.Marshal(req)
		if err != nil {
			m.log.Error("marshal engine session request failed", zap.Error(err))
		} else {
			sCtx, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := m.store.UpsertEngineSession(sCtx, userID, engineID, reqJSON); err != nil {
				m.log.Error("persist engine session failed",
					zap.Int("user_id", userID),
					zap.String("engine_id", engineID),
					zap.Error(err),
				)
			}
			sCancel()
		}
	}

	m.log.Info("engine started",
		zap.Int("user_id", userID),
		zap.String("engine_id", engineID),
		zap.String("mode", req.Mode),
	)
	return engineID, nil
}

// Stop gracefully stops the engine identified by engineID for the given user.
func (m *EngineManager) Stop(userID int, engineID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	userEngines, ok := m.engines[userID]
	if !ok {
		return fmt.Errorf("no engines for user %d", userID)
	}
	re, ok := userEngines[engineID]
	if !ok {
		return fmt.Errorf("engine %q not found", engineID)
	}
	re.cancel()
	delete(userEngines, engineID)

	// Synchronously deactivate session to prevent ghost auto-restarts on next boot.
	if m.store != nil {
		sCtx, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := m.store.DeactivateEngineSession(sCtx, userID, engineID); err != nil {
			m.log.Error("deactivate engine session failed — session may auto-restart on next boot",
				zap.Int("user_id", userID),
				zap.String("engine_id", engineID),
				zap.Error(err),
			)
		}
		sCancel()
	}
	return nil
}

// ClosePosition closes a single side of the engine's open position via the
// exchange, returning the closed qty and fill price. The engine must be in
// live mode and the exchange must support position query (futures/swap).
// Returns the engine's symbol as a courtesy so callers don't have to re-fetch.
func (m *EngineManager) ClosePosition(ctx context.Context, userID int, engineID, side string) (symbol string, qty, price float64, err error) {
	m.mu.RLock()
	userEngines, ok := m.engines[userID]
	if !ok {
		m.mu.RUnlock()
		return "", 0, 0, fmt.Errorf("no engines for user %d", userID)
	}
	re, ok := userEngines[engineID]
	m.mu.RUnlock()
	if !ok {
		return "", 0, 0, fmt.Errorf("engine %q not found", engineID)
	}
	if re.engine == nil {
		return "", 0, 0, fmt.Errorf("engine %q is not in live mode", engineID)
	}
	q, p, err := re.engine.ClosePosition(ctx, re.symbol, side)
	if err != nil {
		return re.symbol, 0, 0, err
	}
	return re.symbol, q, p, nil
}

// StopAll stops all engines for the given user (used by legacy endpoint).
func (m *EngineManager) StopAll(userID int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, re := range m.engines[userID] {
		re.cancel()
		delete(m.engines[userID], id)
		// Synchronously deactivate session to prevent ghost auto-restarts.
		if m.store != nil {
			sCtx, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := m.store.DeactivateEngineSession(sCtx, userID, id); err != nil {
				m.log.Error("deactivate engine session failed — session may auto-restart on next boot",
					zap.Int("user_id", userID),
					zap.String("engine_id", id),
					zap.Error(err),
				)
			}
			sCancel()
		}
	}
}

// StopAllUsers cancels every running engine across all users.
// Called during API server shutdown to prevent orphaned orders.
func (m *EngineManager) StopAllUsers() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for userID, userEngines := range m.engines {
		for id, re := range userEngines {
			re.cancel()
			delete(userEngines, id)
		}
		delete(m.engines, userID)
	}
}

// AutoRestart queries all active engine sessions from the DB and restarts them.
// It is intended to be called once, in a goroutine, after server startup so that
// engines that were running before a restart come back automatically.
func (m *EngineManager) AutoRestart(ctx context.Context) {
	if m.store == nil {
		return
	}
	sessions, err := m.store.GetActiveEngineSessions(ctx)
	if err != nil {
		m.log.Error("auto-restart: failed to load sessions", zap.Error(err))
		return
	}
	if len(sessions) == 0 {
		return
	}
	m.log.Info("auto-restart: found active sessions", zap.Int("count", len(sessions)))
	for _, sess := range sessions {
		var req StartRequest
		if err := json.Unmarshal(sess.RequestJSON, &req); err != nil {
			m.log.Error("auto-restart: unmarshal request failed, deactivating session",
				zap.Int("user_id", sess.UserID),
				zap.String("engine_id", sess.EngineID),
				zap.Error(err),
			)
			// Deactivate permanently broken session to prevent infinite restart loops.
			dCtx, dCancel := context.WithTimeout(ctx, 5*time.Second)
			_ = m.store.DeactivateEngineSession(dCtx, sess.UserID, sess.EngineID)
			dCancel()
			continue
		}
		// Auto-restart sessions were already confirmed when originally started;
		// set ConfirmLive so the safety gate does not block recovery.
		req.ConfirmLive = true
		if _, err := m.Start(sess.UserID, req); err != nil {
			m.log.Error("auto-restart: engine start failed, deactivating session",
				zap.Int("user_id", sess.UserID),
				zap.String("engine_id", sess.EngineID),
				zap.Error(err),
			)
			// Deactivate session that fails to start to prevent retry storms.
			dCtx, dCancel := context.WithTimeout(ctx, 5*time.Second)
			_ = m.store.DeactivateEngineSession(dCtx, sess.UserID, sess.EngineID)
			dCancel()
		} else {
			m.log.Info("auto-restart: engine restarted",
				zap.Int("user_id", sess.UserID),
				zap.String("engine_id", sess.EngineID),
			)
		}
	}
}

// GetEngine returns the EngineInfo for a specific engine.
func (m *EngineManager) GetEngine(userID int, engineID string) (*EngineInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	userEngines, ok := m.engines[userID]
	if !ok {
		return nil, fmt.Errorf("engine %q not found", engineID)
	}
	re, ok := userEngines[engineID]
	if !ok {
		return nil, fmt.Errorf("engine %q not found", engineID)
	}
	return engineInfoFromRE(re), nil
}

// ListAll returns info about all engines for the given user.
func (m *EngineManager) ListAll(userID int) []EngineInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]EngineInfo, 0, len(m.engines[userID]))
	for _, re := range m.engines[userID] {
		out = append(out, *engineInfoFromRE(re))
	}
	return out
}

// Status returns status of the first engine found for the user (backward compat).
func (m *EngineManager) Status(userID int) EngineStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, re := range m.engines[userID] {
		return *engineInfoFromRE(re)
	}
	return EngineInfo{Running: false}
}

// ListAllGlobalEngines returns all running engines across all users (admin use).
func (m *EngineManager) ListAllGlobalEngines() []EngineInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []EngineInfo
	for userID, userEngines := range m.engines {
		for _, re := range userEngines {
			info := engineInfoFromRE(re)
			info.UserID = userID
			out = append(out, *info)
		}
	}
	return out
}

// ForceStop cancels any engine for any user (admin use).
func (m *EngineManager) ForceStop(userID int, engineID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	userEngines, ok := m.engines[userID]
	if !ok {
		return fmt.Errorf("no engines for user %d", userID)
	}
	re, ok := userEngines[engineID]
	if !ok {
		return fmt.Errorf("engine %q not found for user %d", engineID, userID)
	}
	re.cancel()
	delete(userEngines, engineID)

	// Synchronously deactivate session to prevent ghost auto-restarts.
	if m.store != nil {
		sCtx, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := m.store.DeactivateEngineSession(sCtx, userID, engineID); err != nil {
			m.log.Error("deactivate engine session failed — session may auto-restart on next boot",
				zap.Int("user_id", userID),
				zap.String("engine_id", engineID),
				zap.Error(err),
			)
		}
		sCancel()
	}
	return nil
}

// buildNotifier creates a Notifier configured with the user's per-user Telegram
// settings (from DB) and the global SMTP config (if set).
func (m *EngineManager) buildNotifier(ctx context.Context, userID int) *notify.Notifier {
	tgToken, tgChatID, _ := m.store.GetUserNotifications(ctx, userID)
	n := notify.New(tgToken, tgChatID, m.log)

	if m.smtpCfg.Host != "" {
		user, err := m.store.GetUserByID(ctx, userID)
		if err == nil && user.Email != "" {
			n.AddEmail(m.smtpCfg.Host, m.smtpCfg.Port,
				m.smtpCfg.User, m.smtpCfg.Password,
				m.smtpCfg.From, user.Email)
		}
	}
	return n
}

// GetPositions returns a live snapshot of all open positions across every
// running engine for the given user, with unrealized P&L pre-computed.
func (m *EngineManager) GetPositions(userID int) []EnginePositions {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []EnginePositions
	for _, re := range m.engines[userID] {
		// Skip engines that have already stopped.
		select {
		case <-re.done:
			continue
		default:
		}

		var lastPrice, cash, equity float64
		var rawPositions []oms.LivePosition

		if re.paperEng != nil {
			lastPrice = re.paperEng.LastPrice()
			cash = re.paperEng.Cash()
			equity = re.paperEng.Equity()
			rawPositions = re.paperEng.Positions()
		} else if re.engine != nil {
			lastPrice = re.engine.LastPrice()
			cash = re.engine.Cash()
			equity = re.engine.Equity()
			rawPositions = re.engine.Positions()
		}

		views := make([]PositionView, len(rawPositions))
		for i, p := range rawPositions {
			views[i] = PositionView{
				Symbol:        p.Symbol,
				PositionSide:  p.PositionSide,
				Qty:           p.Qty,
				AvgEntryPrice: p.AvgEntryPrice,
				UnrealizedPnL: p.UnrealizedPnL(lastPrice),
				RealizedPnL:   p.RealizedPnL,
			}
		}

		out = append(out, EnginePositions{
			EngineID:   re.engineID,
			StrategyID: re.strategyID,
			Symbol:     re.symbol,
			Mode:       re.mode,
			LastPrice:  lastPrice,
			Cash:       cash,
			Equity:     equity,
			Positions:  views,
		})
	}
	return out
}

// parseBarInterval converts a kline interval string (e.g. "5m", "1h") to time.Duration.
func parseBarInterval(iv string) time.Duration {
	if iv == "" {
		return 0
	}
	d, err := time.ParseDuration(iv)
	if err == nil {
		return d
	}
	// Binance-style: "5m" is valid Go duration. "1h", "4h" too.
	// Fallback for non-standard suffixes.
	return 0
}

func engineInfoFromRE(re *runningEngine) *EngineInfo {
	info := &EngineInfo{
		EngineID:   re.engineID,
		StrategyID: re.strategyID,
		Symbol:     re.symbol,
		Interval:   re.interval,
		Mode:       re.mode,
		Leverage:   re.leverage,
		StartedAt:  re.startedAt.Format(time.RFC3339),
	}
	select {
	case <-re.done:
		info.Running = false
		if re.lastErr != nil {
			info.Error = re.lastErr.Error()
		}
	default:
		info.Running = true
	}
	return info
}
