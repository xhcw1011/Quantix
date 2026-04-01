# Quantix — 数字货币量化交易机器人

Production-grade cryptocurrency quantitative trading bot written in Go.

## Architecture

```
Exchange API ──► Data Ingestion ──► TimescaleDB / Redis
      │                │
      │           NATS Bus
      │          ┌────┴────┐
      │     Strategy   Risk
      │     Engine     Manager
      │          └────┬────┘
      └────────── OMS (Order Mgmt)
                       │
                  Exchange API

Prometheus ◄── Metrics ──► Grafana Dashboard
```

## Quick Start

### 1. Start infrastructure

```bash
cd deploy
docker compose up -d
```

This starts:
- **TimescaleDB** on `localhost:5432` (auto-applies `migrations/001_init.sql`)
- **Redis** on `localhost:6379`
- **NATS** on `localhost:4222`
- **Prometheus** on `localhost:9091`
- **Grafana** on `localhost:3000` (admin / quantix_grafana)

### 2. Configure

```bash
cp config/config.example.yaml config/config.yaml
# Edit config/config.yaml — or use env vars:
# QUANTIX_EXCHANGE_BINANCE_API_KEY=...
# QUANTIX_EXCHANGE_BINANCE_API_SECRET=...
```

### 3. Run

```bash
go run ./cmd/quantix -config config/config.yaml
```

## Development Phases

| Phase | Status | Description |
|-------|--------|-------------|
| 1 | ✅ | Data ingestion: WebSocket + REST + TimescaleDB |
| 2 | 🔜 | Backtesting engine (event-driven, MA Cross strategy) |
| 3 | 🔜 | OMS + Risk management + Paper trading |
| 4 | 🔜 | Live trading + NATS bus + Grafana dashboards |
| 5 | 🔜 | Multi-strategy, multi-exchange, ML factors |

## Project Structure

```
Quantix/
├── cmd/quantix/          # Main entry point
├── internal/
│   ├── config/           # Viper-based typed configuration
│   ├── logger/           # Zap structured logger
│   ├── exchange/         # Binance REST + WebSocket clients
│   ├── data/             # TimescaleDB store + ingestion pipeline
│   ├── monitor/          # Prometheus metrics
│   ├── strategy/         # Strategy interface (Phase 2)
│   ├── backtest/         # Backtesting engine (Phase 2)
│   ├── oms/              # Order management (Phase 3)
│   ├── risk/             # Risk controls (Phase 3)
│   └── indicator/        # Technical indicators (Phase 2)
├── config/
│   ├── config.yaml
│   └── config.example.yaml
├── deploy/
│   ├── docker-compose.yml
│   ├── prometheus.yml
│   └── grafana/
├── migrations/
│   └── 001_init.sql      # TimescaleDB hypertables + indexes
└── scripts/
```

## Key Dependencies

| Package | Purpose |
|---------|---------|
| `adshao/go-binance/v2` | Binance REST + WebSocket SDK |
| `jackc/pgx/v5` | PostgreSQL / TimescaleDB driver |
| `spf13/viper` | Config management (YAML + env vars) |
| `uber-go/zap` | Structured high-performance logging |
| `prometheus/client_golang` | Metrics exposition |
| `nats-io/nats.go` | Message bus (Phase 4) |
| `redis/go-redis/v9` | Real-time cache (Phase 4) |
| `markcheno/go-talib` | Technical indicators (Phase 2) |

## Environment Variables

All config values can be overridden via `QUANTIX_<SECTION>_<KEY>`:

```bash
QUANTIX_EXCHANGE_BINANCE_API_KEY=xxx
QUANTIX_EXCHANGE_BINANCE_API_SECRET=yyy
QUANTIX_DATABASE_PASSWORD=secret
QUANTIX_TRADING_MODE=paper   # paper | live | backtest
```

## Binance Testnet Smoke Tests

Quantix now includes two small verification commands for Binance Spot testnet.
They are useful after changing SDK/auth code, rotating API keys, or switching between
HMAC and private-key auth.

### Private-key config

For Binance RSA private-key auth, configure `config/config.yaml` like this:

```yaml
exchange:
  active: binance
  binance:
    api_key: "<your-binance-testnet-api-key>"
    api_secret: ""      # leave empty when testing private-key auth
    testnet: true
    market_type: spot
    key_type: "RSA"
    private_key_path: "/absolute/path/to/test-prv-key.pem"
```

### 1) Exchange-level smoke test

`cmd/binance-smoke` validates the direct Binance broker path:
- RSA auth
- balance fetch
- market order placement
- optional buy/sell roundtrip

Examples:

```bash
go run ./cmd/binance-smoke -symbol BTCUSDT -qty 0.00010 -side buy

go run ./cmd/binance-smoke -symbol BTCUSDT -qty 0.00010 -roundtrip

go run ./cmd/binance-smoke -symbol BTCUSDT -qty 0.00010 -roundtrip -json
```

### 2) Quantix live-path smoke test

`cmd/live-smoke` validates the internal Quantix live order path:
- live broker submission
- OMS order creation
- fill event propagation
- position manager updates
- optional buy/sell roundtrip

Examples:

```bash
go run ./cmd/live-smoke -symbol BTCUSDT -qty 0.00010

go run ./cmd/live-smoke -symbol BTCUSDT -qty 0.00010 -roundtrip

go run ./cmd/live-smoke -symbol BTCUSDT -qty 0.00010 -roundtrip -json
```

### Makefile shortcuts

For quick repeatable checks:

```bash
make smoke-binance
make smoke-binance-roundtrip
make smoke-live
make smoke-live-roundtrip
```

### What “good” looks like

For a healthy setup you should see:
- Binance client logs with `key_type: RSA`
- order status `FILLED`
- `live-smoke` showing OMS order IDs and fill events
- `roundtrip` finishing with the position emptied again

These commands hit Binance **testnet**, not mainnet, when `testnet: true` is set.
