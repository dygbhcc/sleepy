# Go Conventions — Sleepy

## Errors
- Wrap with context: `fmt.Errorf("step_render: %w", err)`
- Use `errs.NewTransient(provider, statusCode, err)` for retryable errors (429, 502, 503, 504).
- Check with `errs.IsTransient(err)` — never type-assert directly.
- Step functions return `error`. Worker decides retry vs needs_review.

## Database
- All queries use `context.Context` as first parameter.
- Use `ExecContext` / `QueryRowContext` / `QueryContext` — never bare `Exec`.
- Column order in `runColumns` must match `scanRun` field order exactly.
- New columns: `ALTER TABLE runs ADD COLUMN IF NOT EXISTS ... DEFAULT ...;`
- New tables: `CREATE TABLE IF NOT EXISTS ...;`
- Migration files: `internal/db/migrations/NNN_description.sql`, numbered sequentially.

## Transactions
- No explicit transactions in current code — single-statement atomicity suffices.
- `ClaimNextRun` uses `FOR UPDATE SKIP LOCKED` for atomic claiming.
- If you need multi-statement atomicity, use `d.pool.BeginTx(ctx, nil)`.

## Idempotency
- Steps check input hash before re-executing: `computeFileHash()` → compare with stored hash → skip if unchanged.
- `policy_overrides_json` is included in hash computation so changed overrides force re-execution.
- `InsertAsset` is upsert-safe (ON CONFLICT UPDATE).

## Locking
- `locked_by` + `locked_at` on runs table. TTL = 5 minutes.
- Always `ReleaseRun` in defer/finally, even on error.
- Never hold a lock across sleep/wait calls.

## Naming
- Step functions: `stepScript`, `stepTTS`, `stepRender`, `stepThumbnail`, `stepPackage`.
- QA functions: `qaScript`, `qaVoice`, `qaThumbnail`, `qaRender`, `qaPackage`.
- Fix plan IDs: `{category}_{strategy}` e.g. `script_wc_low_computed`, `voice_slow_down`.
- FailType constants: `Fail{Name}` e.g. `FailWordcountLow`, `FailAudioDurationOff`.
- DB methods on `*DB` receiver: `GetRun`, `UpdateRunStatus`, `InsertAsset`, etc.

## Status Transitions
- Only `UpdateRunStatus` changes status. Never write status directly.
- Forward: `domain.NextStatus(currentStatus)`.
- Retry: set status to `TargetStatus` from FixPlan (always same or earlier stage).
- Never skip stages (e.g. PENDING → RENDERED is invalid).

## Policy
- `PolicyForDuration(durationMin)` creates a duration-scaled policy.
- Override-carrier fields (zero = use provider default): `LLMTemperature`, `TTSSpeedFactor`, `EdgeRateDelta`, etc.
- `applyOverrides(map[string]any, *Policy)` maps fix engine keys to policy fields.

## Worker Loop
- `RunWorker` is the outer loop: claim → `processOneStage` → release → repeat.
- `processOneStage` is one stage only. Returns after step+QA+decision.
- Terminal states (`DONE`, `FAILED`, `NEEDS_REVIEW`) are no-ops in `processOneStage`.

## UI
- Single file: `web/index.html`. Vanilla JS, no framework.
- i18n: add keys to all 5 language maps (`en`, `tr`, `pt`, `es`, `it`).
- API calls use `fetch()` to relative URLs.
