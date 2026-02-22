CREATE TABLE IF NOT EXISTS worker_settings (
    id              INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    mode            TEXT NOT NULL DEFAULT 'test',
    groq_api_key    TEXT NOT NULL DEFAULT '',
    openai_api_key  TEXT NOT NULL DEFAULT '',
    openai_base_url TEXT NOT NULL DEFAULT '',
    openai_model    TEXT NOT NULL DEFAULT '',
    elevenlabs_api_key  TEXT NOT NULL DEFAULT '',
    elevenlabs_voice_id TEXT NOT NULL DEFAULT '',
    elevenlabs_model_id TEXT NOT NULL DEFAULT '',
    elevenlabs_speed    REAL NOT NULL DEFAULT 0.80,
    edge_voice      TEXT NOT NULL DEFAULT 'en-US-AndrewNeural',
    edge_rate       TEXT NOT NULL DEFAULT '-20%',
    normalize       BOOLEAN NOT NULL DEFAULT true,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO worker_settings (id) VALUES (1) ON CONFLICT DO NOTHING;
