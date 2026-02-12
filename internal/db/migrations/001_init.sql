CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE IF NOT EXISTS runs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    series       TEXT    NOT NULL,
    episode      TEXT    NOT NULL,
    style        TEXT    NOT NULL DEFAULT 'Cosmos',
    duration_min INT     NOT NULL DEFAULT 30,
    status       TEXT    NOT NULL DEFAULT 'PENDING',
    error_text   TEXT    NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS assets (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id     UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    path       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS job_queue (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id      UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    job_type    TEXT NOT NULL DEFAULT 'RUN_PIPELINE',
    status      TEXT NOT NULL DEFAULT 'PENDING',
    error_text  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_job_queue_pending
    ON job_queue(status, created_at) WHERE status = 'PENDING';
CREATE INDEX IF NOT EXISTS idx_assets_run_kind
    ON assets(run_id, kind);
