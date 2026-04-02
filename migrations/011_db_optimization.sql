-- Migration 011: Database optimization + user expansion tables
-- 2026-04-02

-- ============================================================
-- 1. Drop duplicate indexes (saves space + write overhead)
-- ============================================================
DROP INDEX IF EXISTS idx_fills_user_created;      -- duplicate of idx_fills_user_time
DROP INDEX IF EXISTS idx_equity_user_created;      -- duplicate of idx_equity_user_time

-- Also redundant: idx_backtest_user_id is a prefix of idx_backtest_user_created
DROP INDEX IF EXISTS idx_backtest_user_id;

-- ============================================================
-- 2. Add missing foreign keys
-- ============================================================
ALTER TABLE trade_events
    ADD CONSTRAINT trade_events_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id);

ALTER TABLE strategy_positions
    ADD CONSTRAINT strategy_positions_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id);

-- ============================================================
-- 3. Strategy configs — per-user per-strategy parameter persistence
--    Stores the full strategy_params JSON so auto-restart can
--    recover without the user re-submitting.
-- ============================================================
CREATE TABLE IF NOT EXISTS strategy_configs (
    id          SERIAL PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    strategy_id TEXT    NOT NULL,          -- e.g. "ai", "macross", "grid"
    name        TEXT    NOT NULL DEFAULT 'default',  -- user-given name for this config
    symbol      TEXT    NOT NULL DEFAULT 'ETHUSDT',
    params      JSONB   NOT NULL DEFAULT '{}',       -- full strategy params
    is_active   BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, strategy_id, name)
);

CREATE INDEX idx_strategy_configs_user ON strategy_configs (user_id);

-- ============================================================
-- 4. User preferences — UI settings, notification preferences
-- ============================================================
CREATE TABLE IF NOT EXISTS user_preferences (
    user_id     INTEGER PRIMARY KEY REFERENCES users(id),
    timezone    TEXT    NOT NULL DEFAULT 'Asia/Shanghai',
    locale      TEXT    NOT NULL DEFAULT 'zh-CN',
    theme       TEXT    NOT NULL DEFAULT 'dark',
    notify_on_fill      BOOLEAN NOT NULL DEFAULT true,
    notify_on_stop      BOOLEAN NOT NULL DEFAULT true,
    notify_on_error     BOOLEAN NOT NULL DEFAULT true,
    notify_daily_summary BOOLEAN NOT NULL DEFAULT true,
    preferences JSONB   NOT NULL DEFAULT '{}',  -- extensible JSON for future settings
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============================================================
-- 5. Notification log — track sent notifications for audit
-- ============================================================
CREATE TABLE IF NOT EXISTS notification_log (
    id          BIGSERIAL PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    channel     TEXT    NOT NULL,          -- 'telegram', 'email', 'push'
    level       TEXT    NOT NULL,          -- 'info', 'warn', 'critical'
    title       TEXT    NOT NULL,
    body        TEXT    NOT NULL DEFAULT '',
    sent_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered   BOOLEAN NOT NULL DEFAULT false,
    error       TEXT
);

CREATE INDEX idx_notification_log_user ON notification_log (user_id, sent_at DESC);

-- ============================================================
-- 6. Convert equity_snapshots to hypertable for scalability
--    (every minute per user — grows fast)
-- ============================================================
-- Note: equity_snapshots already has data, so we need to handle
-- the conversion carefully. TimescaleDB can migrate existing data.

-- Hypertable requires partition column in all unique constraints.
-- Change PK from (id) to (id, snapshotted_at) before converting.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM timescaledb_information.hypertables
        WHERE hypertable_name = 'equity_snapshots'
    ) THEN
        ALTER TABLE equity_snapshots DROP CONSTRAINT equity_snapshots_pkey;
        ALTER TABLE equity_snapshots ADD PRIMARY KEY (id, snapshotted_at);
        PERFORM create_hypertable('equity_snapshots', 'snapshotted_at',
            migrate_data => true,
            chunk_time_interval => INTERVAL '7 days'
        );
    END IF;
END
$$;

-- Enable compression (idempotent — check first)
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM timescaledb_information.hypertables
        WHERE hypertable_name = 'equity_snapshots' AND compression_enabled = true
    ) THEN
        ALTER TABLE equity_snapshots SET (
            timescaledb.compress,
            timescaledb.compress_segmentby = 'user_id,strategy_id'
        );
    END IF;
END
$$;
SELECT add_compression_policy('equity_snapshots', INTERVAL '30 days', if_not_exists => true);

-- Add retention policy (drop data older than 1 year)
SELECT add_retention_policy('equity_snapshots', INTERVAL '365 days', if_not_exists => true);

-- ============================================================
-- 7. Add retention policies for other high-volume tables
-- ============================================================
-- klines: keep 2 years (historical data for backtesting)
SELECT add_retention_policy('klines', INTERVAL '730 days', if_not_exists => true);

-- tickers: keep 30 days (tick data is very high volume, rarely needed old)
SELECT add_retention_policy('tickers', INTERVAL '30 days', if_not_exists => true);
