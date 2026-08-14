-- Fix: guardian_state was keyed by engine_id ALONE, but engine_id
-- ("SYMBOL-INTERVAL-STRATEGY", e.g. "BTCUSDT-5m-guardian") is only unique
-- WITHIN one user's engines (internal/api EngineManager keys its running-engine
-- map as engines[userID][engineID]) -- it is NOT globally unique. Two different
-- users running the same symbol+interval+strategy combo share the identical
-- engine_id string, so a restart could load one user's persisted trail-stop
-- into another user's guardian and trigger a spurious protective close on real
-- money (incident: 2026-08-05, user 4's BTCUSDT-5m-guardian picked up user 13's
-- restored stop and immediately closed a real position at a loss).
--
-- Every OTHER per-engine persistence table already keys by (user_id, engine_id)
-- -- see engine_sessions (008) and strategy_positions (009) -- guardian_state
-- was the sole outlier that never got user_id added when it was introduced.
ALTER TABLE guardian_state ADD COLUMN IF NOT EXISTS user_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE guardian_state DROP CONSTRAINT IF EXISTS guardian_state_pkey;
ALTER TABLE guardian_state ADD PRIMARY KEY (user_id, engine_id);
