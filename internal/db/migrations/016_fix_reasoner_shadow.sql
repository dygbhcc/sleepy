-- Shadow-mode LLM reasoner: logs what the model would have chosen for an
-- ambiguous QA fix, alongside the deterministic scorer's actual choice.
-- Purely observational (never read back by the pipeline) — safe to add
-- without affecting run processing or existing fix_engine invariants.

CREATE TABLE IF NOT EXISTS fix_reasoner_log (
    id                 SERIAL PRIMARY KEY,
    run_id             UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    stage              TEXT NOT NULL,
    fail_type          TEXT NOT NULL,
    scorer_choice      TEXT NOT NULL,
    reasoner_choice    TEXT NOT NULL DEFAULT '',
    reasoner_rationale TEXT NOT NULL DEFAULT '',
    agreed             BOOLEAN NOT NULL DEFAULT FALSE,
    error              TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_fix_reasoner_log_run ON fix_reasoner_log(run_id);
