-- Experiments table: track strategy config experiments + auto decision rules.
CREATE TABLE IF NOT EXISTS experiments (
    id                   TEXT PRIMARY KEY,
    engine_id            TEXT NOT NULL,
    hypothesis           TEXT NOT NULL,
    started_at           TIMESTAMPTZ NOT NULL,
    ended_at             TIMESTAMPTZ,
    baseline_start       TIMESTAMPTZ,
    baseline_end         TIMESTAMPTZ,
    decision_after_days  INT NOT NULL DEFAULT 7,
    fail_metric          TEXT NOT NULL,
    fail_op              TEXT NOT NULL,
    fail_threshold       DOUBLE PRECISION NOT NULL,
    fail_relative        BOOLEAN NOT NULL DEFAULT FALSE,
    status               TEXT NOT NULL DEFAULT 'active',
    decided_at           TIMESTAMPTZ,
    notes                TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_experiments_status ON experiments(status);

GRANT SELECT, INSERT, UPDATE, DELETE ON experiments TO quantix;

INSERT INTO experiments(id, engine_id, hypothesis, started_at,
                        decision_after_days, fail_metric, fail_op,
                        fail_threshold, fail_relative, notes)
VALUES (
  'exp-20260506-no-trailing',
  'ETHUSDT-5m-ai',
  'EnableTrailing=false + SLDistanceMultiplier=2.0 让仓位跑到 staged TP/SL（commit 03a408e）',
  '2026-05-06 08:53:00+00',
  7,
  'net_per_day',
  '<',
  0,
  FALSE,
  'Phase B baseline: 4/30→5/6 trailing-on era'
)
ON CONFLICT (id) DO NOTHING;
