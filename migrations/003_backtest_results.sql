-- Phase 9: Backtest results persistence
-- Run as superuser: psql -U apexis-backdesk -d quantix -f migrations/003_backtest_results.sql

CREATE TABLE IF NOT EXISTS backtest_results (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    strategy_id     TEXT NOT NULL,
    symbol          TEXT NOT NULL,
    interval        TEXT NOT NULL,
    params          JSONB,
    start_date      TIMESTAMPTZ,
    end_date        TIMESTAMPTZ,
    initial_capital NUMERIC(20,8) DEFAULT 10000,
    fee_rate        NUMERIC(10,6) DEFAULT 0.001,
    slippage        NUMERIC(10,6) DEFAULT 0.0005,
    status          TEXT NOT NULL DEFAULT 'running', -- running | completed | failed
    result          JSONB,                            -- serialised backtest.Report
    error_msg       TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_backtest_user_id ON backtest_results(user_id);
CREATE INDEX IF NOT EXISTS idx_backtest_status  ON backtest_results(status);
