-- Phase 16: Engine session persistence + state recovery

-- A: engine_sessions stores the full StartRequest for each running engine.
--    On server restart, AutoRestart() queries is_active=true rows and replays Start().
CREATE TABLE IF NOT EXISTS engine_sessions (
    user_id      INTEGER NOT NULL REFERENCES users(id),
    engine_id    TEXT NOT NULL,          -- "{symbol}-{interval}-{strategyID}"
    request_json JSONB NOT NULL,         -- full serialized StartRequest
    is_active    BOOLEAN NOT NULL DEFAULT TRUE,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    stopped_at   TIMESTAMPTZ,
    PRIMARY KEY(user_id, engine_id)
);

-- Partial index speeds up AutoRestart query (only active sessions matter).
CREATE INDEX IF NOT EXISTS idx_engine_sessions_active
    ON engine_sessions(is_active) WHERE is_active = TRUE;

-- C: order_role distinguishes protective orders (stop-loss / take-profit placed
--    automatically by the broker) from regular strategy entry orders.
--    NULL = normal entry order; 'stop_loss' | 'take_profit' = auto-placed protective.
ALTER TABLE orders ADD COLUMN IF NOT EXISTS order_role TEXT;

-- D: position_side in fills enables correct PositionManager reconstruction in
--    hedge mode (distinguishes SELL-to-close-LONG from SELL-to-open-SHORT).
ALTER TABLE fills ADD COLUMN IF NOT EXISTS position_side TEXT NOT NULL DEFAULT '';
