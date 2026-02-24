# Task Template

Copy this template into every Claude prompt. Fill in the blanks. Delete unused sections.

---

```markdown
## Goal
<!-- One sentence. What observable behavior changes? -->

## Context
<!-- Which pipeline stage is affected? What invariant matters? Link to PROJECT_BRIEF.md if needed. -->

## Files to Touch
<!-- Explicit list. Claude must NOT touch files outside this list. -->
- `internal/jobs/___`
- `internal/db/db.go` (if schema change)
- `internal/db/migrations/0XX___.sql` (if schema change)
- `internal/domain/model.go` (if new fields)
- `cmd/api/main.go` (if new endpoint)
- `web/index.html` (if UI change)

## Constraints
- Do NOT restructure existing files or move functions between packages.
- Do NOT add new dependencies.
- Do NOT change function signatures of exported functions unless listed above.
- Preserve all existing QA gate behavior.
- Migration must be idempotent (`IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS`).

## Output Format
Return a **unified diff** (`diff -u`) that I can apply with `git apply`.
If multiple files, concatenate diffs with file headers.
No prose outside the diff block.

## Acceptance Criteria
<!-- Concrete, testable checks. -->
1. `go build ./...` passes.
2. `go vet ./...` passes.
3. Migration applies cleanly: `psql -d sleepy -f internal/db/migrations/0XX___.sql`
4. Worker processes a run through the affected stage without error.
5. <!-- Add scenario-specific check -->

## Non-goals
<!-- Explicitly list what this task must NOT do. -->
- Do not optimize unrelated code.
- Do not add tests (unless task is specifically about tests).
- Do not refactor existing naming or style.
```
