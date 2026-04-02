# Quantix System Status Report — 2026-04-02

## Overview

Quantix is a production-grade cryptocurrency quantitative trading bot in Go, running live on Binance Futures (ETHUSDT) with 10x leverage. The system uses a GPT-powered AI strategy (gpt-5.4-mini) with dual-mode trading (Trend + Range/Scalp).

- **Module:** `github.com/Quantix/quantix`
- **Go Version:** 1.24.5
- **Live Since:** ~2026-03-24
- **Current Equity:** ~102.9 USDT (Total Return: +34.4%)

---

## Architecture

```
cmd/api/main.go          API server (:9300)
internal/
  api/                   HTTP router, EngineManager, JWT auth, rate limiting, WebSocket hub
  exchange/              Exchange abstraction layer
    binance/             Binance Spot
    binance_futures/     Binance USDM Futures (LIVE)
    okx/                 OKX SWAP (demo/live)
    factory/             Exchange client factory
  live/                  Live engine loop, broker, margin monitor
  paper/                 Paper trading simulator
  backtest/              Backtesting engine
  strategy/
    aistrat/             GPT-powered AI strategy (primary)
    macross/             MA crossover
    meanreversion/       Mean reversion
    grid/                Grid trading
    mlstrat/             ML-based strategy
    registry/            Global strategy factory registry
  oms/                   Order management system
  risk/                  Risk manager (drawdown, single-loss, position limits)
  position/              Redis-backed position syncer
  data/                  TimescaleDB store, migrations
  indicator/             Technical indicators (RSI, MACD, BB, EMA, ATR)
  notify/                Telegram + SMTP alerts
  bus/                   NATS message bus
  monitor/               Prometheus trading metrics
  config/                Viper-based config loading
  logger/                Zap structured logging
web/                     React frontend (Vite + TypeScript)
  src/pages/             Dashboard, Engine, Positions, Orders, Fills, Backtest, Admin, Settings
deploy/                  Docker Compose, Nginx, Grafana, Prometheus
scripts/                 start-quantix.sh, restart-live.sh, soak tests
```

## Infrastructure

| Component | Details |
|-----------|---------|
| Database | PostgreSQL 17 + TimescaleDB 2.25.1 |
| Cache | Redis (position syncing, signal caching) |
| Message Bus | NATS (trading events) |
| Monitoring | Prometheus + Grafana |
| Frontend | React + Vite (localhost:5173) |
| Reverse Proxy | Nginx |

## Database Migrations (10)

| # | File | Purpose |
|---|------|---------|
| 1 | 001_init.sql | klines, tickers, orders, positions (hypertables) |
| 2 | 002_multiuser.sql | users, exchange_credentials, fills, equity_snapshots |
| 3 | 003_backtest_results.sql | backtest_results (UUID PK, JSONB) |
| 4 | 004_admin.sql | users.role + users.is_active |
| 5 | 005_notifications.sql | notification settings |
| 6 | 006_indexes.sql | user_id+created_at indexes |
| 7 | 007_order_persistence.sql | client_order_id, position_side, stop_price, reject_reason |
| 8 | 008_session_recovery.sql | engine_sessions table, order_role, fill position_side |
| 9 | 009_strategy_positions.sql | strategy position snapshots |
| 10 | 010_trade_events.sql | trade event audit trail |

---

## Exchange Support

| Exchange | Type | Status | Features |
|----------|------|--------|----------|
| Binance Futures | USDM | **LIVE** | Hedge mode, leverage, stop-market, TP-market, reduce-only limit, order polling |
| Binance Spot | Spot | Ready | Market, limit, stop-loss-limit |
| OKX | SWAP | Ready | Cross margin, demo trading, algo orders, reduce-only limit |

## Key Interfaces

```go
OrderClient interface {
    PlaceMarketOrder(...)
    PlaceLimitOrder(...)
    PlaceStopMarketOrder(...)
    PlaceTakeProfitMarketOrder(...)
    PlaceReduceOnlyLimitOrder(...)   // NEW: staged TP
    SetLeverage(...)
    CancelOrder(...)
    GetBalance(...)
}

// Optional extensions (type-asserted):
MarginQuerier          // GetMarginRatios
EquityQuerier          // GetEquity
OrderStatusChecker     // GetOrderStatus (polling)
OpenOrdersCanceller    // CancelAllOpenOrders (clean-slate)

StagedExitPlacer interface {           // NEW: exchange-native TP/SL
    PlaceStagedTPOrders(...)           // SL + N reduce-only limit TPs
    ReplaceSLOrder(...)                // breakeven SL move
    CancelAllProtective(...)           // cancel all for reversal
}
```

---

## AI Strategy (aistrat) — Full Config

### Core
| Parameter | Default | Description |
|-----------|---------|-------------|
| Symbol | ETHUSDT | Trading pair |
| Model | gpt-5.4-mini | GPT model |
| ConfidenceThreshold | 0.82 | Min confidence to open position |
| LookbackBars | 60 | Bars for warmup |
| CallIntervalBars | 10 | GPT call frequency (bars) |
| EnableShort | true | Allow short positions |
| HedgeMode | false | Simultaneous long+short |
| PrimaryInterval | 5m | Main trading timeframe |
| Intervals | [1m, 5m, 15m] | All subscribed timeframes |

### Trend Mode — Staged TP (Exchange-Native)
| Parameter | Default | Description |
|-----------|---------|-------------|
| RiskPerTrade | 0.02 (2%) | Equity risk per trade |
| ATRK | 4.0 | Stop-loss ATR multiplier |
| TPLevels | [1.0, 1.5, 2.5, 4.0] | TP R-multiples |
| TPQtySplits | [0.40, 0.30, 0.20, 0.10] | Qty fraction per TP level |
| BreakevenR | 0.5 | R to move SL to breakeven |
| BreakevenBuf | 0.001 | Breakeven buffer (0.1%) |

### Trend Mode — Trailing (Fallback for paper/backtest)
| Parameter | Default | Description |
|-----------|---------|-------------|
| TrailingATRK | 10.0 | ATR trailing multiplier |
| TrailBasePct | 0.012 | Base trailing % (1.2%) |
| TrailLowVolPct | 0.008 | Low-vol trailing (0.8%) |
| TrailHighVolPct | 0.015 | High-vol trailing (1.5%) |
| TrailFloorPct | 0.005 | Absolute min trail (0.5%) |
| MinSLDistPct | 0.008 | Min SL distance (0.8%) |
| ReversalConf | 0.72 | GPT reversal exit threshold |
| MarketEntryConf | 0.90 | Confidence for market (vs limit) entry |

### Range/Scalp Mode
| Parameter | Default | Description |
|-----------|---------|-------------|
| RangeTPPct | 0.012 | Take-profit (1.2%) |
| RangeSLPct | 0.010 | Stop-loss (1.0%) |
| RangeBEPct | 0.003 | Breakeven at 0.3% profit |
| RangeLockPct | 0.006 | Lock profit at 0.6% |
| RangeLockOffset | 0.003 | Lock offset (0.3%) |
| RangeTrailPct | 0.008 | Start trailing at 0.8% |
| RangeTrailDist | 0.003 | Trailing distance (0.3%) |
| RangeProfitTimeout | 60m | Timeout for profitable pos |
| RangeLossTimeout | 20m | Timeout for losing pos |
| RangeFlatTimeout | 30m | Timeout for flat pos |
| BBWidthMin | 0.006 | Min BB width for TP calc |
| BBWidthMax | 0.015 | Max BB width for TP calc |
| RangeEMAConv | 0.003 | EMA convergence threshold |

### Grid Mode
| Parameter | Default | Description |
|-----------|---------|-------------|
| GridMaxLayers | 2 | Max grid orders per position |
| GridSpacingPct | 0.005 | Grid level spacing (0.5%) |
| GridTPPct | 0.004 | Grid take-profit (0.4%) |
| GridQtyRatio | 0.5 | Grid qty / base qty |

### MTF Scoring
| Parameter | Default | Description |
|-----------|---------|-------------|
| MTFStrongTrend | 0.01 | 15m return for strong trend |
| MTFWeakTrend | 0.002 | 15m return for weak trend |
| MTFBullRSI | 60 | RSI bullish threshold |
| MTFBearRSI | 40 | RSI bearish threshold |
| MTF1mThreshold | 0.001 | 1m return threshold |
| MTFQtyScaleHard | 0.70 | Qty scale for strong headwind |
| MTFQtyScaleSoft | 0.85 | Qty scale for mild headwind |
| SwingProximity | 0.0015 | Swing high/low proximity |

### Technical Indicators
| Parameter | Default | Description |
|-----------|---------|-------------|
| RSIPeriod | 14 | RSI lookback |
| MACDFast/Slow/Signal | 12/26/9 | MACD periods |
| EMAFast/Slow | 20/50 | EMA periods |
| BBPeriod/StdDev | 20/2.0 | Bollinger Bands |
| ATRPeriod | 60 | ATR lookback |
| VolMAPeriod | 20 | Volume MA period |

### Entry/Exit Tuning
| Parameter | Default | Description |
|-----------|---------|-------------|
| EntryOffsetPct | 0.0013 | Limit entry offset |
| MaxEntryDevPct | 0.005 | Max GPT entry deviation |
| LimitTimeoutBars | 2 | Bars to wait for limit fill |
| MinHoldBars | 3 | Min hold before TP/SL |
| MinTrendBars | 5 | Min bars for trend management |
| GPTTemperature | 0.3 | GPT randomness |
| GPTMaxTokens | 400 | GPT response limit |
| GPTTimeout | 15s | GPT API timeout |

### Risk Limits
| Parameter | Default | Description |
|-----------|---------|-------------|
| MaxDailyLossPct | 0.10 | Max daily loss (10%) |
| MaxConsecLoss | 5 | Max consecutive losses |

---

## Recent Changes (This Session — 2026-04-02)

### 1. WebSocket Reconnection Fix (P0)
**Problem:** WS only monitored first connection's `doneC`. Other streams dying went undetected — engine ran 10+ hours without data.

**Fix:**
- `sync.Once` teardown: any stream death → tear down all → unified reconnect
- Data staleness watchdog: 3s check interval, 30s timeout → force reconnect
- Engine stale bar alert: 2min without K-line → ERROR log + CRITICAL notification

**Files:** `binance_futures_ws.go`, `binance_ws.go`, `live/engine.go`

### 2. Staged Limit Order TP System (Trend Mode)
**Problem:** All exits depended on local tick data + market orders. WS disconnect = missed exits + slippage.

**Fix:** After entry fill, place exchange-native orders:
- 1 STOP_MARKET for SL (exchange guarantees execution)
- N REDUCE_ONLY LIMIT orders for staged TP (fills at exact price, zero slippage)
- OnTick only handles +0.5R breakeven SL move
- GPT reversal cancels all protective orders before closing

**TP Plan (R-based):**
| Level | R-Multiple | Qty | Remaining | Rationale |
|-------|-----------|-----|-----------|-----------|
| +1.0R | 1.0 | 40% | 60% | Recover 2x risk, "free position" |
| +1.5R | 1.5 | 30% | 30% | Lock profit, 70% total closed |
| +2.5R | 2.5 | 20% | 10% | Trend confirmed |
| +4.0R | 4.0 | 10% | 0% | "Lottery ticket" |

**Files:** `orderbroker.go` (interface), `binance_futures/`, `binance/`, `okx/` (implementations), `live/broker.go`, `live/engine.go`, `strategy/strategy.go`, `aistrat/aistrat.go`

### 3. Configurable Strategy Parameters
**Problem:** 60+ hardcoded magic numbers in aistrat.go.

**Fix:** All trading parameters extracted to `Config` struct (42 new fields across 7 groups). Configurable per-user per-strategy via API `strategy_params` JSON.

**Files:** `aistrat/aistrat.go` (Config + DefaultConfig + init parser + all references)

### 4. WS System Config
**Problem:** WS timeout constants were compile-time.

**Fix:** Added `WSConfig` to `config.go`, passed through factory to WS clients.

**Files:** `config/config.go`, `binance_futures_ws.go`, `binance_ws.go`, `factory/factory.go`, `api/manager.go`

### 5. Binance Futures ReduceOnly + PositionSide Fix
**Problem:** Binance API rejects `reduceOnly=true` when `positionSide` is set (hedge mode).

**Fix:** All three methods (`PlaceStopMarketOrder`, `PlaceTakeProfitMarketOrder`, `PlaceReduceOnlyLimitOrder`) now conditionally set `ReduceOnly` only in one-way mode.

### 6. Infrastructure
- Startup script moved from `/tmp` → `scripts/start-quantix.sh`
- Logs moved from `/tmp/quantix-logs/` → `logs/` (project directory)
- Credentials read from environment variables (not hardcoded)

---

## Test Status

| Package | Status |
|---------|--------|
| api | PASS |
| backtest | PASS |
| bus | PASS |
| exchange | PASS |
| exchange/okx | PASS |
| live | **FAIL** (1 pre-existing: `TestLiveBroker_DuplicateOrderBlocked`) |
| ml | PASS |
| oms | PASS |
| optimize | PASS |
| paper | PASS |
| portfolio | PASS |
| risk | PASS |
| strategy/* (4 packages) | PASS |

## Known Issues

1. **`TestLiveBroker_DuplicateOrderBlocked`** — pre-existing test failure, duplicate order detection returns empty string instead of pending order ID
2. **Strategy params not persisted to DB** — currently params are ephemeral (passed at engine start). Need per-user per-strategy DB persistence for auto-restart recovery
3. **Telegram notifications disabled** — no token/chat_id configured
4. **Trailing tightening multipliers** (0.40, 0.65 at 1.5R/2.0R) still hardcoded in `manageTrend` fallback — secondary detail, not user-facing

## Startup

```bash
export QUANTIX_ENCRYPTION_KEY=$(openssl rand -hex 32)
export QUANTIX_JWT_SECRET=$(openssl rand -hex 32)
export QUANTIX_LIVE_CONFIRM=true
export QUANTIX_API_ADDR=:9300
go build -o bin/quantix-api ./cmd/api
./scripts/start-quantix.sh
# Logs: logs/quantix-YYYYMMDD.log
# Frontend: cd web && npm run dev → http://localhost:5173
```

## Phase History

| Phase | Status | Description |
|-------|--------|-------------|
| 1 | Done | Data ingestion (WS + REST + TimescaleDB) |
| 2 | Done | Backtesting engine, strategy interface, indicators |
| 3 | Done | OMS, risk management, paper trading |
| 4 | Done | NATS bus, trading metrics, Telegram, live engine, Grafana |
| 5 | Done | Exchange abstraction + OKX/Bybit, WFO optimizer, strategies |
| 6 | Done | CancelOrder + OrderClient abstraction |
| 7 | Done | Multi-user REST API, encrypted credentials, React frontend |
| 8 | Done | Input validation, WS origin check, multi-engine per user |
| 9 | Done | Paper trading UI, Backtest API + UI |
| 10 | Done | Futures trading (short/hedge/leverage/stop-TP), Admin panel |
| 11 | Done | Production readiness (health, migrations, Docker, Settings) |
| 12 | Done | Bug fixes (10) + Swagger/OpenAPI docs |
| 13 | Done | Hardcoded values to config, OMS epsilon consistency |
| 14 | Done | Pre-launch safety + real-time push fixes |
| 15 | Done | OMS persistence + order idempotency |
| 16 | Done | Engine session persistence + state recovery |
| R4 | Done | P0 production safety fixes (7 fixes) |
| R5 | Done | P0+P1 hardening (13 fixes) |
| R6 | Done | Final P2 hardening + canary deployment checklist |
| **Current** | **Done** | WS reconnection, staged TP, configurable params, ReduceOnly fix |

## Next Steps

1. **Strategy params DB persistence** — save per-user per-strategy config to database, auto-restore on engine restart
2. **Telegram integration** — configure alerts for stale engine, critical errors, daily summaries
3. **Fix `TestLiveBroker_DuplicateOrderBlocked`** — investigate and fix the pre-existing test regression
4. **Monitor staged TP in production** — verify exchange fills flow correctly through `handleStagedTPFill` and `remainQty` tracking
