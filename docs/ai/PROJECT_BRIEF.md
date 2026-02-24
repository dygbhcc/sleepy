# Project Brief — Sleepy

## What
Automated sleep-video production pipeline. A Go backend generates scripts (LLM), synthesizes narration (TTS), creates thumbnails, renders video (FFmpeg), and packages episodes — all orchestrated by a single-stage-per-iteration worker loop with QA gates and a self-healing FixEngine.

## Pipeline State Machine
```
PENDING → SCRIPTED → [VOICE APPROVAL GATE] → VOICED → THUMBNAILED → RENDERED → PACKAGED → DONE
          stepScript   user clicks approve     stepTTS   stepThumbnail  stepRender  stepPackage
          qaScript                              qaVoice   qaThumbnail    qaRender    qaPackage
```
Terminal states: `DONE`, `FAILED`, `NEEDS_REVIEW`.

## Key Tables
| Table | Purpose |
|---|---|
| `runs` | Pipeline state, attempt counters, fix engine state, voice_approved, policy_overrides_json |
| `assets` | Files produced per step (script_md, narration_wav, thumbnail_png, video_mp4, etc.) |
| `jobs` | Legacy job queue (retained for retry endpoint) |
| `fix_outcomes` | Fix plan success/failure history for scorer |
| `settings` | Singleton worker config (mode, API keys, TTS params) |

## Key Invariants
1. **One stage per iteration.** Worker claims a run, processes ONE stage, releases lock. Never processes two stages in one claim.
2. **Status = next step.** `PENDING` triggers `stepScript`, `SCRIPTED` triggers `stepTTS`, etc. Setting status IS the retry mechanism.
3. **Voice approval gate.** `ClaimNextRun` skips `SCRIPTED` runs where `voice_approved = false`.
4. **Atomic claiming.** `FOR UPDATE SKIP LOCKED` prevents two workers from processing the same run.
5. **Lock TTL.** 5-minute expiry on `locked_at` — stale locks are reclaimed automatically.
6. **Idempotency.** Steps check input hashes before re-executing. Changed overrides = new hash = step re-runs.
7. **FixEngine is optional.** `deps.FixEngine == nil` falls back to legacy `Decide()` path.
8. **Overrides survive restarts.** `policy_overrides_json` persisted in DB, loaded on each `processOneStage`.

## Repo Layout
```
cmd/api/main.go          — HTTP server (routes, handlers)
cmd/worker/main.go       — Standalone worker binary
internal/jobs/           — Worker loop, QA, steps, fix engine, policy
internal/db/             — PostgreSQL CRUD + migrations/
internal/domain/         — Run, Asset, state machine
internal/providers/      — LLM, TTS (ElevenLabs + Edge), image
internal/worker/         — In-process worker manager
web/index.html           — Single-file SPA dashboard
```

## Providers
| Mode | LLM | TTS | Cost |
|---|---|---|---|
| test | Groq (Llama 3.3 70B) | Edge TTS | Free |
| prod | OpenAI GPT-4o | ElevenLabs | Paid (voice gate) |
