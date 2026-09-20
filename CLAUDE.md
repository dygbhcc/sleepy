# CLAUDE.md

Guidance for Claude Code (and any AI assistant) working in this repository. This file is
generated from `docs/ai/PROJECT_BRIEF.md` and the rest of `docs/ai/`, which remain the
source of truth — read them before any non-trivial change, and update both places if
behavior changes.

## What this is

Sleepy is a Go backend that automates production of sleep-narration YouTube episodes:
LLM script generation, TTS narration, thumbnail generation, FFmpeg rendering, and
packaging — orchestrated by a single-stage-per-iteration worker loop with QA gates and
a self-healing FixEngine.

## Pipeline state machine

```
PENDING → SCRIPTED → [VOICE APPROVAL GATE] → VOICED → THUMBNAILED → RENDERED → PACKAGED → UPLOADED → DONE
          stepScript   user clicks approve     stepTTS   stepThumbnail  stepRender  stepPackage stepYouTube
          qaScript                              qaVoice   qaThumbnail    qaRender    qaPackage
```

Terminal states: `DONE`, `FAILED`, `NEEDS_REVIEW`. Status transitions live in
`internal/domain/state.go` (`RunStatus`, `NextStatus`).

## Repo layout

```
cmd/api/main.go          HTTP server (routes, handlers)
cmd/worker/main.go       Standalone worker binary
cmd/seed/main.go         Seed/dev data
internal/jobs/           Worker loop, QA, steps, fix engine, policy
internal/db/             PostgreSQL CRUD + internal/db/migrations/
internal/domain/         Run, Asset, state machine
internal/providers/      LLM, TTS (Kokoro local, Edge, ElevenLabs legacy), image, youtube
internal/render/         ffmpeg / ffprobe wrappers
internal/storage/        asset storage (local filesystem)
internal/ttsreliability/ TTS chunking, idempotency ledger, QA
internal/worker/         in-process worker manager
web/index.html           single-file SPA dashboard (vanilla JS, no framework)
scripts/                 dev-up.sh, kokoro_tts.py, sql helpers
```

## Providers

| Mode | LLM | TTS | Cost | Notes |
|---|---|---|---|---|
| test | Groq (Llama 3.3 70B) | Edge TTS | Free | dev/testing only |
| kokoro | OpenAI GPT-4o-mini (or Groq, configurable) | Kokoro TTS (local, via `scripts/kokoro_tts.py` + `internal/providers/tts/kokoro.go`) | Free — no TTS API key | **primary TTS mode going forward** |
| prod | OpenAI GPT-4o | ElevenLabs | Paid (LLM + TTS) | legacy/optional; no longer the default |

Mode is selected per-worker in Settings (`mode`: `test` / `kokoro` / `prod`, wired in
`internal/worker/manager.go`). The voice approval gate (invariant 3 below) applies to
every mode, regardless of TTS engine. Kokoro voice/speed (`kokoro_voice`,
`kokoro_speed`; defaults `af_sky` / `0.85`) are set in Settings, not env vars — no
`ELEVENLABS_API_KEY` is needed unless running in legacy `prod` mode.

## Key invariants — do not break these

1. **One stage per iteration.** The worker claims a run, processes ONE stage, releases
   the lock. Never process two stages in one claim.
2. **Status = next step.** `PENDING` triggers `stepScript`, `SCRIPTED` triggers
   `stepTTS`, etc. Setting status IS the retry mechanism.
3. **Voice approval gate.** `ClaimNextRun` must keep skipping `SCRIPTED` runs where
   `voice_approved = false`.
4. **Atomic claiming.** `FOR UPDATE SKIP LOCKED` prevents two workers from claiming the
   same run — never remove or weaken this.
5. **Lock TTL.** 5-minute expiry on `locked_at`; stale locks are reclaimed automatically.
   Always release the lock in defer/finally, on every code path, including errors.
6. **Idempotency.** Steps check input hashes before re-executing
   (`computeFileHash()` → compare stored hash → skip if unchanged).
   `policy_overrides_json` is part of the hash, so changed overrides force a re-run.
   `InsertAsset` must stay upsert-safe (`ON CONFLICT UPDATE`).
7. **FixEngine is optional.** `deps.FixEngine == nil` must keep falling back to the
   legacy `Decide()` path.
8. **Overrides survive restarts.** `policy_overrides_json` is persisted in the DB and
   loaded on each `processOneStage`.
9. **Never skip stages.** e.g. `PENDING → RENDERED` is invalid. Only `UpdateRunStatus`
   may change status; forward moves use `domain.NextStatus`, retries/loopbacks set
   `TargetStatus` to the same or an earlier stage — never forward.

## Go conventions

- Errors: wrap with context — `fmt.Errorf("step_render: %w", err)`. Use
  `errs.NewTransient(provider, statusCode, err)` for retryable errors (429/502/503/504)
  and check with `errs.IsTransient(err)` (never a direct type assertion). Step functions
  return `error`; the worker decides retry vs. `NEEDS_REVIEW`.
- DB: every query takes `context.Context` first. Use `ExecContext` /
  `QueryRowContext` / `QueryContext` — never bare `Exec`. `runColumns` order must match
  `scanRun` field order exactly. New columns: `ALTER TABLE runs ADD COLUMN IF NOT
  EXISTS ... DEFAULT ...;`. New tables: `CREATE TABLE IF NOT EXISTS ...;`. Migration
  files go in `internal/db/migrations/NNN_description.sql`, numbered sequentially, and
  must be idempotent (apply cleanly on both a fresh DB and an existing one).
- Transactions: no explicit transactions currently — single-statement atomicity
  suffices. `ClaimNextRun` uses `FOR UPDATE SKIP LOCKED`. Use `d.pool.BeginTx` only if
  you genuinely need multi-statement atomicity.
- Naming: step functions `stepScript`, `stepTTS`, `stepRender`, `stepThumbnail`,
  `stepPackage`, `stepYouTube`; QA functions `qaScript`, `qaVoice`, `qaThumbnail`,
  `qaRender`, `qaPackage`; fix plan IDs `{category}_{strategy}` (e.g.
  `script_wc_low_computed`, `voice_slow_down`); FailType constants `Fail{Name}` (e.g.
  `FailWordcountLow`, `FailAudioDurationOff`); DB methods on `*DB`
  (`GetRun`, `UpdateRunStatus`, `InsertAsset`, ...).
- UI: single file `web/index.html`, vanilla JS, no framework. i18n keys must be added
  to all 5 language maps (`en`, `tr`, `pt`, `es`, `it`). API calls use `fetch()` to
  relative URLs. Escape user input with `esc()` before rendering.
- Security: all queries use `$1`-style placeholders (no string-built SQL). No path
  traversal in asset serving. No secrets logged or returned in API responses. File
  writes go through `deps.Store` — never raw `os.Create` to an arbitrary path.

## QA gate (script safety)

Scripts are auto-validated for sleep-content safety and rejected/retried on:
high-tension words (suddenly, blood, scream, terror, panic, etc.), excessive
exclamation marks (>2) or ALL-CAPS words (>3), average sentence length above 25 words,
high repetition ratio (<0.30 unique/total). LLM generation auto-retries up to 2 times
with feedback on failure.

## Working on a task

Use `docs/ai/TASK_TEMPLATE.md` as the shape for any task/prompt: state the goal in one
sentence, name the exact files to touch (do not touch files outside that list), list
constraints (no restructuring, no new dependencies, no signature changes unless listed,
preserve QA gate behavior, idempotent migrations), and prefer returning a **unified
diff** with no prose outside it unless the user asks for something else.

Non-goals unless explicitly requested: do not optimize unrelated code, do not add
tests, do not refactor unrelated naming/style, do not restructure files or move
functions between packages, do not add new dependencies.

Before calling anything done, run through `docs/ai/DEFINITION_OF_DONE.md`:

- `go build ./...` and `go vet ./...` clean.
- New/changed migrations exist under `internal/db/migrations/`, apply cleanly, and are
  idempotent.
- Runtime smoke test: API starts (`go run cmd/api/main.go`, :8080), worker starts, a
  1-minute test run reaches `SCRIPTED`, voice approval advances it past `VOICED`, and it
  reaches `DONE` with no panic/fatal in the worker logs.
- State machine integrity: no stage skipped, retries loop to the correct (not forward)
  status, `NEEDS_REVIEW` runs are not auto-claimed.
- Regression: existing DB runs aren't broken by the migration, the legacy `Decide()`
  path still works when `FixEngine == nil`, the voice approval gate still blocks
  unapproved `SCRIPTED` runs.

Before applying any diff, self-check it against `docs/ai/PR_REVIEW_CHECKLIST.md`
(state machine, concurrency/locking, DB column consistency, idempotency, fix-engine
attempt limits, security, voice approval gate, UI i18n/escaping/colspan).

## Local dev

```bash
bash scripts/dev-up.sh   # start Postgres + run migrations
pip install kokoro soundfile numpy   # once, for local Kokoro TTS (default TTS engine)

export PG_DSN="postgres://sleepy:sleepy@localhost:5432/sleepy?sslmode=disable"
export OPENAI_API_KEY="sk-..."
export ASSET_ROOT="./tmp/assets"
# ELEVENLABS_API_KEY / ELEVENLABS_VOICE_ID only needed for legacy "prod" mode.

go run ./cmd/worker
go run ./cmd/api          # listens on :8080
# In Settings (UI or API), set mode to "kokoro" (default) to use local Kokoro TTS.
```

See `README.md` for the full env var reference and the manual run/enqueue SQL, and
`docs/ai/BACKLOG_HIGH_THROUGHPUT.md` for the in-flight throughput-scaling work
(parallel worker pool, priority queue, batch script generation, etc.) if a task touches
concurrency or claiming.
