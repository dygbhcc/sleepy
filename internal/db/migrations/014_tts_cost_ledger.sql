CREATE TABLE IF NOT EXISTS tts_cost_ledger (
    id          BIGSERIAL PRIMARY KEY,
    run_id      TEXT NOT NULL,
    chunk_index INT,
    char_count  INT NOT NULL,
    cost_usd    DOUBLE PRECISION NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tts_cost_run ON tts_cost_ledger(run_id);
