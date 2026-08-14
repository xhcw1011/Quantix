-- Generic engine-scoped state persistence, shared by any subsystem that
-- needs a small piece of runtime state to survive an engine/server restart
-- (e.g. the risk-manager circuit breaker: internal/risk.Manager).
--
-- engine_id ("SYMBOL-INTERVAL-STRATEGY") is only unique WITHIN one user's
-- engines, not globally -- two different users can run the identical
-- symbol+interval+strategy combo. On 2026-08-05/06 this exact bug (a state
-- key built from engine_id alone) was independently found and fixed three
-- times in three unrelated places: guardian_state (migration 015),
-- experiments (migration 016), and composite's Redis key
-- (internal/strategy/composite/recovery.go). This table -- and the
-- internal/enginestate package built on top of it -- exists so a FOURTH
-- subsystem needing this pattern gets user-scoping by construction (userID
-- is a required parameter of every Save/Load call) instead of by
-- remembering to include it in a hand-rolled key.
--
-- `namespace` separates unrelated subsystems sharing this table (e.g.
-- "risk") so they can never collide with each other even if two of them
-- reuse the same engine_id string for unrelated reasons.
--
-- guardian_state and experiments are NOT migrated onto this table:
-- guardian_state already holds real production data and a live-traffic
-- migration ordering mistake would reintroduce the exact restart-reentry
-- bug class fixed earlier tonight; experiments is a reporting/config table
-- queried with SQL-level filters (status, engine_id), not a fit for an
-- opaque JSONB blob.
CREATE TABLE IF NOT EXISTS engine_state (
    user_id    INTEGER NOT NULL,
    engine_id  TEXT NOT NULL,
    namespace  TEXT NOT NULL,
    state      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, engine_id, namespace)
);
