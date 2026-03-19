ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS instagram_app_id TEXT NOT NULL DEFAULT '';
ALTER TABLE worker_settings ADD COLUMN IF NOT EXISTS instagram_app_secret TEXT NOT NULL DEFAULT '';
