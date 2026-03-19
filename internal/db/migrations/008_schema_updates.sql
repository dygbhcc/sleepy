-- runs: new columns
ALTER TABLE runs ADD COLUMN IF NOT EXISTS youtube_attempt    INTEGER   NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS title              TEXT      NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS title_locked       BOOLEAN   NOT NULL DEFAULT false;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS youtube_video_id   TEXT      NOT NULL DEFAULT '';

-- worker_settings: throughput / safety knobs
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS worker_mode           TEXT      NOT NULL DEFAULT 'SAFE';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS require_voice_approval BOOLEAN  NOT NULL DEFAULT true;
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS max_inflight_script   INTEGER   NOT NULL DEFAULT 1;
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS max_inflight_tts      INTEGER   NOT NULL DEFAULT 1;
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS max_inflight_render   INTEGER   NOT NULL DEFAULT 1;

-- worker_settings: music + youtube
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS music_path            TEXT      NOT NULL DEFAULT '';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS youtube_enabled       BOOLEAN   NOT NULL DEFAULT false;
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS youtube_privacy       TEXT      NOT NULL DEFAULT 'unlisted';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS youtube_client_id     TEXT      NOT NULL DEFAULT '';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS youtube_client_secret TEXT      NOT NULL DEFAULT '';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS youtube_access_token  TEXT      NOT NULL DEFAULT '';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS youtube_refresh_token TEXT      NOT NULL DEFAULT '';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS youtube_token_expiry  TIMESTAMPTZ NOT NULL DEFAULT '1970-01-01T00:00:00Z';
