# Definition of Done

Every change must pass ALL checks before merge.

## Build
- [ ] `go build ./...` — zero errors
- [ ] `go vet ./...` — zero warnings

## Migration
- [ ] New migration file exists in `internal/db/migrations/` (if schema changed)
- [ ] Migration applies cleanly: `psql -d sleepy -f internal/db/migrations/NNN_*.sql`
- [ ] Migration is idempotent (re-running produces no errors)

## Runtime Smoke Test
- [ ] API starts: `go run cmd/api/main.go` → listens on :8080
- [ ] Worker starts: click "Start Worker" in UI or run `cmd/worker/main.go`
- [ ] Create a 1-min test run via UI
- [ ] Run reaches SCRIPTED without error (script step passes QA)
- [ ] Approve voice → run advances past VOICED
- [ ] Run reaches DONE (all stages pass)
- [ ] No panic/fatal in worker logs

## State Machine Integrity
- [ ] No status is skipped (verify via progress bar in UI)
- [ ] Retry loops back to correct status (not forward)
- [ ] NEEDS_REVIEW runs are NOT auto-claimed by worker

## Regression
- [ ] Existing runs in DB are not broken by migration
- [ ] Legacy `Decide()` path still works when `FixEngine == nil`
- [ ] Voice approval gate still blocks unapproved SCRIPTED runs
