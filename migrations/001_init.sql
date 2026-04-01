-- Quantix TimescaleDB Schema
-- Run this once against a TimescaleDB-enabled PostgreSQL instance.

-- Enable TimescaleDB extension
CREATE EXTENSION IF NOT EXISTS timescaledb;

-- ─────────────────────────────────────────────
-- OHLCV Klines (hypertable partitioned by time)
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS klines (
    time          TIMESTAMPTZ NOT NULL,
    symbol        TEXT        NOT NULL,
    interval      TEXT        NOT NULL,
    open          DOUBLE PRECISION NOT NULL,
    high          DOUBLE PRECISION NOT NULL,
    low           DOUBLE PRECISION NOT NULL,
    close         DOUBLE PRECISION NOT NULL,
    volume        DOUBLE PRECISION NOT NULL,
    quote_volume  DOUBLE PRECISION NOT NULL,
    num_trades    BIGINT NOT NULL DEFAULT 0,
    close_time    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (time, symbol, interval)
);

-- Convert to hypertable, partitioned by time (7-day chunks)
SELECT create_hypertable('klines', 'time', if_not_exists => TRUE, chunk_time_interval => INTERVAL '7 days');

-- Compression policy: compress chunks older than 30 days
ALTER TABLE klines SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'symbol,interval'
);
SELECT add_compression_policy('klines', INTERVAL '30 days', if_not_exists => TRUE);

-- Index for fast symbol+interval lookups
CREATE INDEX IF NOT EXISTS idx_klines_symbol_interval ON klines (symbol, interval, time DESC);

-- ─────────────────────────────────────────────
-- Tickers (best bid/ask snapshots)
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS tickers (
    time        TIMESTAMPTZ NOT NULL,
    symbol      TEXT        NOT NULL,
    bid_price   DOUBLE PRECISION NOT NULL,
    ask_price   DOUBLE PRECISION NOT NULL,
    last_price  DOUBLE PRECISION NOT NULL,
    volume      DOUBLE PRECISION NOT NULL DEFAULT 0
);

SELECT create_hypertable('tickers', 'time', if_not_exists => TRUE, chunk_time_interval => INTERVAL '1 day');

-- Compress tickers older than 7 days
ALTER TABLE tickers SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'symbol'
);
SELECT add_compression_policy('tickers', INTERVAL '7 days', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS idx_tickers_symbol ON tickers (symbol, time DESC);

-- ─────────────────────────────────────────────
-- Orders (live and paper trading history)
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS orders (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    exchange_id     TEXT,
    symbol          TEXT NOT NULL,
    side            TEXT NOT NULL,       -- BUY | SELL
    type            TEXT NOT NULL,       -- MARKET | LIMIT | STOP_LIMIT
    status          TEXT NOT NULL,       -- PENDING | OPEN | FILLED | CANCELLED | REJECTED
    quantity        DOUBLE PRECISION NOT NULL,
    price           DOUBLE PRECISION,    -- NULL for market orders
    filled_quantity DOUBLE PRECISION NOT NULL DEFAULT 0,
    avg_fill_price  DOUBLE PRECISION,
    commission      DOUBLE PRECISION NOT NULL DEFAULT 0,
    strategy_id     TEXT,
    mode            TEXT NOT NULL,       -- paper | live
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_orders_symbol_status ON orders (symbol, status);
CREATE INDEX IF NOT EXISTS idx_orders_strategy ON orders (strategy_id, created_at DESC);

-- ─────────────────────────────────────────────
-- Positions (aggregated PnL per strategy)
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS positions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    symbol          TEXT NOT NULL,
    strategy_id     TEXT NOT NULL,
    side            TEXT NOT NULL,       -- LONG | SHORT
    quantity        DOUBLE PRECISION NOT NULL,
    entry_price     DOUBLE PRECISION NOT NULL,
    current_price   DOUBLE PRECISION,
    unrealized_pnl  DOUBLE PRECISION,
    realized_pnl    DOUBLE PRECISION NOT NULL DEFAULT 0,
    opened_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at       TIMESTAMPTZ,
    is_open         BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE INDEX IF NOT EXISTS idx_positions_strategy ON positions (strategy_id, is_open);
