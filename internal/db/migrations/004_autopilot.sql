-- Autopilot v1: attempt counters, hashes, needs_review flag, and run locking.

-- Attempt counters per stage.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS script_attempt  INT NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS voice_attempt   INT NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS render_attempt  INT NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS package_attempt INT NOT NULL DEFAULT 0;

-- Error tracking.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS last_error      TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS needs_review    BOOLEAN NOT NULL DEFAULT FALSE;

-- Per-step canonical input hashes for idempotency.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS script_hash     TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS voice_hash      TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS render_hash     TEXT NOT NULL DEFAULT '';

-- Run-level locking (TTL-based, no separate lock table).
ALTER TABLE runs ADD COLUMN IF NOT EXISTS locked_by       TEXT    DEFAULT NULL;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS locked_at       TIMESTAMPTZ DEFAULT NULL;

-- Index for the autopilot worker to find claimable runs efficiently.
-- Lock TTL check (locked_at < now() - 5min) is done at query time, not in the index,
-- because now() is not IMMUTABLE.
CREATE INDEX IF NOT EXISTS idx_runs_claimable
    ON runs(created_at ASC)
    WHERE status NOT IN ('DONE','FAILED','NEEDS_REVIEW');
