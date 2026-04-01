-- Add performance indexes for common user-scoped queries.
-- These speed up paginated fills, equity chart, and backtest list endpoints.

CREATE INDEX IF NOT EXISTS idx_fills_user_created
    ON fills (user_id, filled_at DESC);

CREATE INDEX IF NOT EXISTS idx_equity_user_created
    ON equity_snapshots (user_id, snapshotted_at DESC);

CREATE INDEX IF NOT EXISTS idx_backtest_user_created
    ON backtest_results (user_id, created_at DESC);
