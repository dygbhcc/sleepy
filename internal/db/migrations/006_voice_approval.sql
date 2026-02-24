-- Voice approval gate: TTS only runs after explicit user approval.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS voice_approved BOOLEAN NOT NULL DEFAULT false;
