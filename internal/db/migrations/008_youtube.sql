-- YouTube upload integration
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS youtube_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS youtube_privacy TEXT NOT NULL DEFAULT 'unlisted';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS youtube_client_id TEXT NOT NULL DEFAULT '';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS youtube_client_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS youtube_access_token TEXT NOT NULL DEFAULT '';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS youtube_refresh_token TEXT NOT NULL DEFAULT '';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS youtube_token_expiry TIMESTAMPTZ NOT NULL DEFAULT '1970-01-01';

ALTER TABLE runs ADD COLUMN IF NOT EXISTS youtube_attempt INT NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS youtube_video_id TEXT NOT NULL DEFAULT '';
