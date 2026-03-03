CREATE TABLE IF NOT EXISTS tts_attempts (
    id              BIGSERIAL PRIMARY KEY,
    run_id          TEXT NOT NULL,
    chunk_index     INT NOT NULL,
    attempt_num     INT NOT NULL,
    idempotency_key TEXT UNIQUE,
    provider_gen_id TEXT,
    settings_json   JSONB,
    metrics_json    JSONB,
    qa_pass         BOOLEAN,
    qa_fail_type    TEXT,
    char_count      INT,
    cost_usd        DOUBLE PRECISION,
    artifact_path   TEXT,
    created_at      TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tts_attempts_run ON tts_attempts(run_id);
