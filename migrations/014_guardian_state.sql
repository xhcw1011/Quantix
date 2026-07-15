-- Guardian trail-state persistence: lets a Position Guardian restore its advanced
-- (trailed) stop after an engine/server restart instead of resetting to the
-- initial protective level. Keyed by engine id (stable across restarts).
CREATE TABLE IF NOT EXISTS guardian_state (
    engine_id  TEXT PRIMARY KEY,
    state      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
