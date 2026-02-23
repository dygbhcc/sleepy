-- Fix engine: outcome tracking + persisted overrides.

CREATE TABLE IF NOT EXISTS fix_outcomes (
    id               SERIAL PRIMARY KEY,
    run_id           UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    stage            TEXT NOT NULL,
    fail_type        TEXT NOT NULL,
    fix_plan_id      TEXT NOT NULL,
    attempts_to_pass INT NOT NULL DEFAULT 0,
    cost_estimate    DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    success          BOOLEAN NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_fix_outcomes_plan ON fix_outcomes(fix_plan_id);
CREATE INDEX IF NOT EXISTS idx_fix_outcomes_run ON fix_outcomes(run_id);

-- Track active fix plan and when it started (for attempts_to_pass computation).
ALTER TABLE runs ADD COLUMN IF NOT EXISTS active_fix_plan_id      TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS active_fix_start_attempt INT NOT NULL DEFAULT 0;

-- Persisted policy overrides (survive worker restarts).
ALTER TABLE runs ADD COLUMN IF NOT EXISTS policy_overrides_json    JSONB NOT NULL DEFAULT '{}';
