-- Quantix Multi-User Extension
-- Adds: users, exchange_credentials, fills, equity_snapshots
-- Extends: orders (user_id, credential_id)

-- ─────────────────────────────────────────────
-- Users
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS users (
    id            SERIAL PRIMARY KEY,
    username      TEXT UNIQUE NOT NULL,
    email         TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,        -- bcrypt cost=12
    is_active     BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─────────────────────────────────────────────
-- Exchange credentials (API keys encrypted at rest)
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS exchange_credentials (
    id          SERIAL PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    exchange    TEXT NOT NULL,           -- 'binance' | 'okx' | 'bybit'
    label       TEXT NOT NULL,           -- user-defined name, e.g. "Binance Testnet"
    api_key     TEXT NOT NULL,           -- AES-256-GCM encrypted, base64
    api_secret  TEXT NOT NULL,           -- AES-256-GCM encrypted, base64
    passphrase  TEXT,                    -- OKX only, encrypted, base64
    testnet     BOOLEAN NOT NULL DEFAULT false,
    demo        BOOLEAN NOT NULL DEFAULT false,
    market_type TEXT NOT NULL DEFAULT 'spot',
    is_active   BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, exchange, label)
);

CREATE INDEX IF NOT EXISTS idx_credentials_user ON exchange_credentials (user_id);

-- ─────────────────────────────────────────────
-- Fill records (persisted from live/paper engine)
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS fills (
    id                BIGSERIAL PRIMARY KEY,
    user_id           INTEGER REFERENCES users(id),
    strategy_id       TEXT NOT NULL,
    symbol            TEXT NOT NULL,
    side              TEXT NOT NULL,           -- 'BUY' | 'SELL'
    qty               NUMERIC(20,8) NOT NULL,
    price             NUMERIC(20,8) NOT NULL,
    fee               NUMERIC(20,8) NOT NULL DEFAULT 0,
    realized_pnl      NUMERIC(20,8) NOT NULL DEFAULT 0,
    exchange_order_id TEXT,
    mode              TEXT NOT NULL DEFAULT 'live',   -- 'live' | 'paper'
    filled_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_fills_user_time ON fills (user_id, filled_at DESC);
CREATE INDEX IF NOT EXISTS idx_fills_strategy   ON fills (strategy_id, filled_at DESC);

-- ─────────────────────────────────────────────
-- Equity snapshots (equity curve data)
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS equity_snapshots (
    id              BIGSERIAL PRIMARY KEY,
    user_id         INTEGER REFERENCES users(id),
    strategy_id     TEXT NOT NULL,
    equity          NUMERIC(20,8) NOT NULL,
    cash            NUMERIC(20,8) NOT NULL,
    unrealized_pnl  NUMERIC(20,8) NOT NULL DEFAULT 0,
    realized_pnl    NUMERIC(20,8) NOT NULL DEFAULT 0,
    snapshotted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_equity_user_time ON equity_snapshots (user_id, snapshotted_at DESC);
CREATE INDEX IF NOT EXISTS idx_equity_strategy  ON equity_snapshots (strategy_id, snapshotted_at DESC);

-- ─────────────────────────────────────────────
-- Extend existing orders table
-- ─────────────────────────────────────────────
ALTER TABLE orders ADD COLUMN IF NOT EXISTS user_id       INTEGER REFERENCES users(id);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS credential_id INTEGER REFERENCES exchange_credentials(id);
