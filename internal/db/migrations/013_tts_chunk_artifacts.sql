CREATE TABLE IF NOT EXISTS tts_chunk_artifacts (
    id            BIGSERIAL PRIMARY KEY,
    run_id        TEXT NOT NULL,
    chunk_index   INT NOT NULL,
    attempt_num   INT NOT NULL,
    artifact_path TEXT NOT NULL,
    duration_sec  DOUBLE PRECISION,
    lufs          DOUBLE PRECISION,
    created_at    TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tts_chunks_run ON tts_chunk_artifacts(run_id);
