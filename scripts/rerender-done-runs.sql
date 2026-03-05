-- Re-render all DONE runs from the render step.
-- Sets status to THUMBNAILED so the worker picks them up and re-renders.
-- Clears render_hash so idempotency doesn't skip re-generation.
-- Does NOT touch voice_hash or script_hash — TTS and script are preserved.

BEGIN;

-- Show what will be affected.
SELECT id, series, episode, style, status
  FROM runs
 WHERE status IN ('DONE', 'UPLOADED', 'PACKAGED', 'RENDERED')
 ORDER BY created_at;

-- Reset to THUMBNAILED and clear render hash + lock.
UPDATE runs
   SET status     = 'THUMBNAILED',
       render_hash = '',
       locked_by   = NULL,
       locked_at   = NULL,
       updated_at  = now()
 WHERE status IN ('DONE', 'UPLOADED', 'PACKAGED', 'RENDERED');

COMMIT;
