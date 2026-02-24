# PR Review Checklist

Use this for every diff before applying. Answer each question with YES/NO/NA.

## State Machine
- [ ] Does any code change `run.Status` outside of `UpdateRunStatus`?
- [ ] Can a status transition skip a stage (e.g. PENDING → RENDERED)?
- [ ] Does a retry/loopback set `TargetStatus` to a same-or-earlier stage?
- [ ] Are terminal states (DONE, FAILED, NEEDS_REVIEW) handled as no-ops in `processOneStage`?

## Concurrency
- [ ] Is `ClaimNextRun` still using `FOR UPDATE SKIP LOCKED`?
- [ ] Are locks released in ALL code paths (including error returns)?
- [ ] Is any shared state (FixScorer) protected by mutex?
- [ ] Can two workers process the same run simultaneously? (Must be NO)

## Database
- [ ] Do `runColumns` and `scanRun` have the same field count and order?
- [ ] Are new columns added with `IF NOT EXISTS` and sensible defaults?
- [ ] Does migration work on a fresh DB AND on an existing DB?
- [ ] Are foreign keys using the correct type (UUID for run_id)?

## Idempotency
- [ ] Will re-running a step with unchanged inputs produce the same result?
- [ ] Are policy overrides included in the hash computation?
- [ ] Does `InsertAsset` handle duplicates gracefully?

## Retries & Fix Engine
- [ ] Is the global attempt ceiling (20) respected?
- [ ] Are per-stage limits checked before selecting a fix plan?
- [ ] Does `isStageExhausted` cover the new FailType (if any)?
- [ ] Are fix outcomes logged on both success and needs_review?
- [ ] Are overrides cleared (`clearFixState`) when advancing to next stage?

## Security
- [ ] No SQL injection (all queries use `$1` placeholders)?
- [ ] No path traversal in asset serving?
- [ ] No secrets logged or returned in API responses?
- [ ] File writes use `deps.Store` — no raw `os.Create` to arbitrary paths?

## Voice Approval Gate
- [ ] Does `ClaimNextRun` still skip unapproved SCRIPTED runs?
- [ ] Is `voice_approved` reset on retry (if applicable)?

## UI (if touched)
- [ ] Are i18n keys added to ALL 5 languages?
- [ ] Does the table colspan match the column count?
- [ ] Are user inputs escaped with `esc()` before rendering?
