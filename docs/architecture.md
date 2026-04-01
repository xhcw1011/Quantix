# Quantix Architecture

## Overview

Quantix is a production-grade cryptocurrency quantitative trading platform built in Go (backend) and React (frontend). It supports live trading, paper trading, backtesting, and multi-user management across Binance, OKX, and Bybit exchanges.

## Phase History

| Phase | Feature |
|-------|---------|
| 1 | Data ingestion — WebSocket + REST + TimescaleDB |
| 2 | Backtesting engine, strategy interface, indicators |
| 3 | OMS, risk management, paper trading |
| 4 | NATS bus, trading metrics, Telegram alerts, live engine |
| 5 | Exchange abstraction, WFO optimizer, MeanReversion/Grid/ML strategies |
| 6 | OrderClient abstraction, CancelOrder, Binance Testnet + OKX Demo |
| 7 | Multi-user REST API, encrypted credentials, React frontend |
| 8 | Input validation, WS origin check, multi-engine per user |
| 9 | Paper trading via UI, Backtest API + Backtest UI |
| 10 | Futures trading, hedge mode, stop/TP orders, Binance Futures, admin panel |

## Directory Structure

```
Quantix/
├── cmd/
│   ├── api/            — REST API server entry point
│   ├── backtest/       — CLI backtesting tool
│   ├── ingest/         — data ingestion daemon
│   ├── optimize/       — Walk-Forward Optimization CLI
│   └── quantix/        — live trading entry point
├── internal/
│   ├── api/            — HTTP handlers, middleware, engine manager
│   ├── backtest/       — backtesting engine + metrics
│   ├── config/         — Viper config loader
│   ├── data/           — PostgreSQL store (pgxpool)
│   ├── exchange/       — exchange interfaces + implementations
│   │   ├── binance/           — Binance Spot REST+WS
│   │   ├── binance_futures/   — Binance USDM Futures order broker
│   │   ├── bybit/             — Bybit REST+WS
│   │   ├── factory/           — exchange factory (avoids circular imports)
│   │   └── okx/               — OKX REST+WS+order broker
│   ├── indicator/      — SMA, EMA, RSI, MACD, BB, CrossOver/Under
│   ├── live/           — live trading engine + broker + margin monitor
│   ├── logger/         — Zap logger (dev=console, prod=JSON)
│   ├── ml/             — logistic regression model loader
│   ├── monitor/        — Prometheus metrics
│   ├── notify/         — Telegram notifications
│   ├── oms/            — order management system (thread-safe)
│   ├── optimize/       — Walk-Forward Optimization + Pareto filter
│   ├── paper/          — paper trading engine + simulated broker
│   ├── portfolio/      — multi-strategy portfolio manager
│   ├── risk/           — risk manager, Kelly criterion
│   └── strategy/
│       ├── grid/           — grid trading strategy
│       ├── macross/        — dual-SMA crossover (with hedge mode)
│       ├── meanreversion/  — BB+RSI mean reversion
│       ├── mlstrat/        — ML-driven strategy (logistic regression)
│       └── registry/       — global strategy factory registry
├── migrations/         — SQL migrations
├── scripts/ml/         — Python ML training scripts
├── web/                — React + Vite frontend
└── docs/               — documentation
```

## Key Architectural Decisions

### Exchange Abstraction
- `exchange.RESTClient` and `exchange.WSClient` interfaces for market data
- `exchange.OrderClient` interface for order placement (no import cycle)
- `exchange.MarginQuerier` interface for margin ratio polling
- `internal/exchange/factory` subpackage prevents circular imports
- Binance Spot uses `go-binance/v2`; OKX uses raw HTTP+HMAC; Bybit uses REST

### Strategy Interface
```go
type Strategy interface {
    Name() string
    OnBar(ctx *Context, bar exchange.Kline)
    OnFill(ctx *Context, fill Fill)
}
```
- Strategies are registered via `init()` side-effects into a global registry
- Registry allows dynamic instantiation by string ID from API requests

### Engine Lifecycle
```
StartRequest → EngineManager.Start()
  → fetch+decrypt credentials
  → build exchange clients (REST+WS+Order)
  → create strategy via registry
  → configure leverage (futures only)
  → goroutine: backfill 500 bars → WS subscribe → engine.Run(klineCh)
  → engine.Run(): ProcessBar → strategy.OnBar → broker.PlaceOrder
```

### Multi-User Isolation
- Each user has an isolated map of engines: `map[userID]map[engineID]*runningEngine`
- API keys encrypted with AES-256-GCM; `QUANTIX_ENCRYPTION_KEY` required at startup
- JWT tokens (HMAC-SHA256, standard library only) authenticate each request
- Admin role stored in DB, checked per-request by `adminOnly` middleware

### OMS → Broker Pipeline
```
Strategy.OnBar → Context.PlaceOrder(OrderRequest)
  → live.Broker.PlaceOrder
     → risk.Manager.CheckOrder (position %, drawdown circuit breaker)
     → OMS.Submit → exchange.OrderClient.PlaceMarketOrder / PlaceLimitOrder / ...
     → fill → OMS.Fill → PositionManager.ApplyFill
     → auto-attach StopLoss / TakeProfit protective orders
  → Strategy.OnFill(fill)
```

### Paper Trading Simulation
- `paper.Broker.ProcessBar(high, low, close)` checks pending limit/stop/TP orders each bar
- Limit buy triggers when `bar.Low ≤ price`, limit sell when `bar.High ≥ price`
- Stop-market fires when price crosses `StopPrice` in the adverse direction
- Short positions tracked via separate `ShortQty`/`ShortEntry` in `PositionManager`

## Database Schema

See `migrations/` for full SQL. Key tables:
- `users` — `id, username, email, password_hash, role, is_active, created_at`
- `exchange_credentials` — encrypted API keys per user
- `fills` / `orders` — trade history per user + strategy
- `equity_snapshots` — portfolio value over time
- `backtest_results` — async backtest jobs (UUID PK, JSONB params+result)
- `klines` / `tickers` — market data hypertables (TimescaleDB)
