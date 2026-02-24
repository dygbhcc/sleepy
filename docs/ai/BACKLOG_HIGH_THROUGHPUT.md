# Backlog: High Throughput Mode

Goal: Process 50+ runs/day instead of ~5. Eliminate bottlenecks in claiming, parallelism, provider rate limits, and observability.

---

## HT-01: Parallel Worker Pool

**Goal:** Run N worker goroutines concurrently instead of 1.

**Files:** `internal/worker/manager.go`, `internal/jobs/worker.go`

**Acceptance Criteria:**
- Manager accepts `concurrency int` parameter
- Spawns N goroutines, each calling `RunWorker` with unique workerID
- `go build ./...` passes
- 3 runs created simultaneously → all 3 progress in parallel

**Risks:** Lock contention on `ClaimNextRun`. Mitigated by `SKIP LOCKED`.

**Claude Patch Prompt:**
```
Read internal/worker/manager.go and internal/jobs/worker.go.
Add a Concurrency field to Manager. In Start(), launch N goroutines each calling
RunWorker with workerID = "worker-{i}". Do NOT change RunWorker's signature or
the claiming logic. Return a unified diff. No architecture rewrites.
```

---

## HT-02: Priority Queue Ordering

**Goal:** Process high-priority runs before low-priority ones.

**Files:** `internal/db/migrations/007_priority.sql`, `internal/db/db.go`, `internal/domain/model.go`, `cmd/api/main.go`, `web/index.html`

**Acceptance Criteria:**
- `runs.priority` column (INT, default 0, higher = sooner)
- `ClaimNextRun` orders by `priority DESC, created_at ASC`
- UI shows priority field on create form
- Migration is idempotent

**Risks:** Starvation of low-priority runs. Acceptable for now.

**Claude Patch Prompt:**
```
Read internal/db/db.go (ClaimNextRun, runColumns, scanRun, CreateRun).
Add an INT priority column to runs (migration 007). Add Priority int to domain.Run.
Update runColumns, scanRun, CreateRun, ClaimNextRun (ORDER BY priority DESC, created_at ASC).
Add priority field to the create API handler and UI form. Return unified diff only.
```

---

## HT-03: Batch Script Generation

**Goal:** Generate scripts for multiple runs in a single LLM call to reduce API round-trips.

**Files:** `internal/providers/llm/llm.go`, `internal/jobs/steps_script.go`

**Acceptance Criteria:**
- New `GenerateScripts(ctx, []ScriptRequest) ([]*ScriptResult, error)` method
- Falls back to single-call if batch fails
- Existing single-call path unchanged

**Risks:** LLM may produce lower quality in batch. Mitigated by QA gate.

**Claude Patch Prompt:**
```
Read internal/providers/llm/llm.go.
Add a GenerateScripts method that concatenates up to 3 ScriptRequests into one prompt,
parses multiple ---SSML--- delimited sections. If parsing fails, fall back to calling
GenerateScript individually. Do NOT modify GenerateScript. Return unified diff.
```

---

## HT-04: TTS Queue with Rate Limiting

**Goal:** Prevent ElevenLabs 429s by queuing TTS calls with a token bucket.

**Files:** `internal/jobs/steps_tts.go`, `internal/jobs/worker.go`

**Acceptance Criteria:**
- Add `TTSRateLimiter` (token bucket, 1 req/sec default) to Deps
- `stepTTS` acquires a token before calling Synthesize
- Nil limiter = no limiting (backward compatible)
- No new dependencies (use `time.Ticker` or `golang.org/x/time/rate` if already in go.mod)

**Risks:** Slower individual runs. Acceptable — prevents 429 cascades.

**Claude Patch Prompt:**
```
Read internal/jobs/steps_tts.go and internal/jobs/worker.go (Deps struct).
Add a TTSRateLimiter field to Deps (interface with Acquire(ctx) error).
In stepTTS, call limiter.Acquire before TTS synthesis (skip if nil).
Implement a simple token-bucket limiter in a new file internal/jobs/ratelimit.go.
Do NOT change TTS provider code. Return unified diff.
```

---

## HT-05: Stage-Level Metrics

**Goal:** Track per-stage duration and success rate for bottleneck identification.

**Files:** `internal/db/migrations/008_metrics.sql`, `internal/db/db.go`, `internal/jobs/worker.go`

**Acceptance Criteria:**
- `stage_metrics` table: run_id, stage, duration_ms, success, created_at
- `processOneStage` records start time, writes metric after step+QA
- Queryable: `SELECT stage, avg(duration_ms), count(*) FROM stage_metrics GROUP BY stage`

**Risks:** Write amplification. Mitigated: 1 INSERT per stage execution, async-safe.

**Claude Patch Prompt:**
```
Read internal/jobs/worker.go (processOneStage) and internal/db/db.go.
Create migration 008_metrics.sql with stage_metrics table.
Add InsertStageMetric(ctx, runID, stage string, durationMs int64, success bool) to DB.
In processOneStage, record time.Now() before the switch, compute duration after,
insert metric. Do NOT change step functions. Return unified diff.
```

---

## HT-06: Stale Run Reaper

**Goal:** Auto-recover runs stuck with expired locks that weren't released cleanly.

**Files:** `internal/jobs/worker.go`

**Acceptance Criteria:**
- New `reapStaleRuns(ctx, db)` function
- Runs locked > 10 minutes with non-terminal status → release lock
- Called once per minute from `RunWorker` (separate goroutine)
- Logged: "reaped stale run {id} locked by {worker} for {duration}"

**Risks:** Could release a legitimately slow run (e.g. 30-min render). Mitigated: 10-min threshold is 2x the lock TTL.

**Claude Patch Prompt:**
```
Read internal/jobs/worker.go and internal/db/db.go (ReleaseRun).
Add a reapStaleRuns function that queries runs WHERE locked_at < now() - interval '10 minutes'
AND status NOT IN terminal states, and calls ReleaseRun for each. Launch as a goroutine
in RunWorker with a 1-minute ticker. Do NOT change ClaimNextRun. Return unified diff.
```

---

## HT-07: Dashboard Throughput Widget

**Goal:** Show runs/hour and average time-to-DONE on the UI dashboard.

**Files:** `cmd/api/main.go`, `internal/db/db.go`, `web/index.html`

**Acceptance Criteria:**
- New API: `GET /api/stats/throughput` → `{ runs_completed_24h, avg_duration_sec, runs_per_hour }`
- DB query: count DONE runs in last 24h, avg(updated_at - created_at)
- UI: new card in stats area showing throughput numbers
- i18n for all 5 languages

**Risks:** None significant.

**Claude Patch Prompt:**
```
Read cmd/api/main.go, internal/db/db.go, web/index.html (renderStats).
Add GetThroughputStats(ctx) method to DB that returns completed count, avg duration
for runs that reached DONE in the last 24 hours. Add API handler and route.
Add a throughput card to the UI stats area. Add i18n keys. Return unified diff.
```

---

## HT-08: Skip Unchanged Steps

**Goal:** Use canonical input hashing to skip steps whose inputs haven't changed (e.g. after a loopback that only changed TTS params, don't regenerate script).

**Files:** `internal/jobs/steps_script.go`, `internal/jobs/steps_tts.go`, `internal/jobs/qa.go`

**Acceptance Criteria:**
- `stepScript` computes `canonicalInputHash` including policy overrides
- If hash matches `run.ScriptHash`, skip step and reuse existing asset
- Same pattern for `stepTTS` with `run.VoiceHash`
- Logged: "step_script: skipping (input hash unchanged)"

**Risks:** Stale assets served if hash collision (astronomically unlikely with SHA-256).

**Claude Patch Prompt:**
```
Read internal/jobs/steps_script.go, internal/jobs/steps_tts.go, internal/jobs/qa.go (canonicalInputHash).
In stepScript, before calling LLM, compute canonicalInputHash with series, episode, style,
language, target_words, policy_overrides_json. Compare with run.ScriptHash. If match and
script asset exists on disk, skip. Same for stepTTS with voice-relevant params.
Do NOT change canonicalInputHash signature. Return unified diff.
```

---

## HT-09: Webhook on DONE

**Goal:** POST a webhook when a run reaches DONE so external systems can react.

**Files:** `internal/db/migrations/009_webhook.sql`, `internal/db/db.go`, `internal/domain/model.go`, `internal/jobs/worker.go`, `cmd/api/main.go`, `web/index.html`

**Acceptance Criteria:**
- `settings.webhook_url` field (nullable TEXT)
- When run reaches DONE, POST JSON `{run_id, series, episode, status}` to webhook URL
- Fire-and-forget (5s timeout, log errors, don't block pipeline)
- UI: webhook URL field in settings modal

**Risks:** Webhook endpoint down. Mitigated: fire-and-forget, logged.

**Claude Patch Prompt:**
```
Read internal/jobs/worker.go (processOneStage, where status advances to DONE),
internal/domain/model.go (WorkerSettings), cmd/api/main.go (settings handlers).
Add webhook_url to settings table (migration 009), WorkerSettings, settings API.
In worker, after setting status to DONE, fire a goroutine that POSTs to webhook_url
with 5s timeout. Add webhook_url input to UI settings. Return unified diff.
```

---

## HT-10: Bulk Create Runs

**Goal:** Create multiple runs from a CSV/list in one action.

**Files:** `cmd/api/main.go`, `internal/db/db.go`, `web/index.html`

**Acceptance Criteria:**
- New API: `POST /api/runs/bulk` — accepts array of run specs
- Creates up to 20 runs in a single transaction
- Returns array of created runs
- UI: "Bulk Create" button that accepts textarea (one line per episode: series,episode,style,language,duration)
- Validation errors returned per-row, valid rows still created

**Risks:** Accidental mass creation. Mitigated: cap at 20 per request.

**Claude Patch Prompt:**
```
Read cmd/api/main.go (handleCreateRun), internal/db/db.go (CreateRun).
Add POST /api/runs/bulk handler that accepts JSON array of {series,episode,style,language,duration_min}.
Cap at 20. Use a transaction to insert all. Return created runs array.
Add a "Bulk Create" button to the UI that shows a textarea, parses CSV lines,
and POSTs to the bulk endpoint. Add i18n keys. Return unified diff.
```

---

## Execution Order

```
HT-01 (parallel workers)     — unblocks everything
HT-06 (stale reaper)         — safety net for HT-01
HT-04 (TTS rate limiter)     — prevents 429 cascades with HT-01
HT-02 (priority queue)       — control over what runs first
HT-08 (skip unchanged)       — avoid redundant LLM/TTS calls on retries
HT-05 (stage metrics)        — measure before further optimization
HT-07 (throughput widget)    — visualize improvements
HT-03 (batch scripts)        — reduce LLM round-trips
HT-10 (bulk create)          — feed the pipeline faster
HT-09 (webhook on DONE)      — integrate with external systems
```
