-- Instagram Reels support: content types, Instagram fields, Pexels API.

-- Runs: content type + Instagram fields.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS content_type TEXT NOT NULL DEFAULT 'sleep_narration';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS duration_sec INT NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS instagram_media_id TEXT NOT NULL DEFAULT '';

-- Worker settings: Instagram OAuth + Pexels.
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS instagram_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS instagram_access_token TEXT NOT NULL DEFAULT '';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS instagram_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS pexels_api_key TEXT NOT NULL DEFAULT '';
