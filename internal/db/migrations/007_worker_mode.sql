-- HT-01 prerequisite: worker mode and concurrency-limit settings.
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS worker_mode              TEXT    NOT NULL DEFAULT 'SAFE';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS require_voice_approval   BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS max_inflight_script      INT     NOT NULL DEFAULT 1;
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS max_inflight_tts         INT     NOT NULL DEFAULT 1;
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS max_inflight_render      INT     NOT NULL DEFAULT 1;
