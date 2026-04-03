package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"golang.org/x/oauth2"

	"sleepy/internal/domain"
)

// DB wraps a Postgres connection pool.
type DB struct {
	pool *sql.DB
}

// Pool returns the underlying *sql.DB for direct access (e.g., ttsreliability ledger).
func (d *DB) Pool() *sql.DB { return d.pool }

// Open connects to Postgres and verifies the connection.
func Open(dsn string) (*DB, error) {
	pool, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	pool.SetMaxOpenConns(5)
	pool.SetMaxIdleConns(2)
	pool.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Close shuts down the connection pool.
func (d *DB) Close() error { return d.pool.Close() }

// ---------- runs ----------

const runColumns = `id, series, episode, style, language, duration_min, status, error_text, created_at, updated_at,
	script_attempt, voice_attempt, render_attempt, package_attempt, youtube_attempt, last_error, needs_review,
	script_hash, voice_hash, render_hash, locked_by, locked_at,
	voice_approved, title, title_locked, youtube_video_id, active_fix_plan_id, active_fix_start_attempt, policy_overrides_json`

func scanRun(row interface{ Scan(...any) error }, r *domain.Run) error {
	return row.Scan(&r.ID, &r.Series, &r.Episode, &r.Style, &r.Language, &r.DurationMin,
		&r.Status, &r.ErrorText, &r.CreatedAt, &r.UpdatedAt,
		&r.ScriptAttempt, &r.VoiceAttempt, &r.RenderAttempt, &r.PackageAttempt, &r.YouTubeAttempt,
		&r.LastError, &r.NeedsReview, &r.ScriptHash, &r.VoiceHash, &r.RenderHash,
		&r.LockedBy, &r.LockedAt,
		&r.VoiceApproved, &r.Title, &r.TitleLocked, &r.YouTubeVideoID, &r.ActiveFixPlanID, &r.ActiveFixStartAttempt, &r.PolicyOverridesJSON)
}

// GetRun loads a run by ID.
func (d *DB) GetRun(ctx context.Context, id string) (*domain.Run, error) {
	r := &domain.Run{}
	err := scanRun(d.pool.QueryRowContext(ctx,
		`SELECT `+runColumns+` FROM runs WHERE id = $1`, id), r)
	if err != nil {
		return nil, fmt.Errorf("get run %s: %w", id, err)
	}
	return r, nil
}

// UpdateRunStatus advances a run to the given status.
func (d *DB) UpdateRunStatus(ctx context.Context, id string, status domain.RunStatus) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE runs SET status = $1, updated_at = now() WHERE id = $2`,
		string(status), id,
	)
	if err != nil {
		return fmt.Errorf("update run status: %w", err)
	}
	return nil
}

// FailRun marks a run as FAILED with an error message.
func (d *DB) FailRun(ctx context.Context, id string, errText string) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE runs SET status = $1, error_text = $2, last_error = $2, updated_at = now() WHERE id = $3`,
		string(domain.StatusFailed), errText, id,
	)
	if err != nil {
		return fmt.Errorf("fail run: %w", err)
	}
	return nil
}

// IncrementAttempt atomically increments a specific attempt counter.
// column must be one of: script_attempt, voice_attempt, render_attempt, package_attempt.
func (d *DB) IncrementAttempt(ctx context.Context, id string, column string) error {
	// Whitelist column names to prevent SQL injection.
	allowed := map[string]bool{
		"script_attempt": true, "voice_attempt": true,
		"render_attempt": true, "package_attempt": true,
		"youtube_attempt": true,
	}
	if !allowed[column] {
		return fmt.Errorf("invalid attempt column: %s", column)
	}
	_, err := d.pool.ExecContext(ctx,
		fmt.Sprintf(`UPDATE runs SET %s = %s + 1, updated_at = now() WHERE id = $1`, column, column),
		id,
	)
	if err != nil {
		return fmt.Errorf("increment %s: %w", column, err)
	}
	return nil
}

// SetNeedsReview marks a run as needing human review.
func (d *DB) SetNeedsReview(ctx context.Context, id string, reason string) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE runs SET status = $1, needs_review = TRUE, last_error = $2, updated_at = now() WHERE id = $3`,
		string(domain.StatusNeedsReview), reason, id,
	)
	if err != nil {
		return fmt.Errorf("set needs review: %w", err)
	}
	return nil
}

// UpdateRunHash stores the input hash for a given step.
// column must be one of: script_hash, voice_hash, render_hash.
func (d *DB) UpdateRunHash(ctx context.Context, id string, column string, hash string) error {
	allowed := map[string]bool{
		"script_hash": true, "voice_hash": true, "render_hash": true,
	}
	if !allowed[column] {
		return fmt.Errorf("invalid hash column: %s", column)
	}
	_, err := d.pool.ExecContext(ctx,
		fmt.Sprintf(`UPDATE runs SET %s = $1, updated_at = now() WHERE id = $2`, column),
		hash, id,
	)
	if err != nil {
		return fmt.Errorf("update %s: %w", column, err)
	}
	return nil
}

// UpdateRunTitle sets the title for a run. If locked is true, the title is
// marked as user-provided and will not be overwritten by AI generation.
func (d *DB) UpdateRunTitle(ctx context.Context, id string, title string, locked bool) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE runs SET title = $1, title_locked = $2, updated_at = now() WHERE id = $3`,
		title, locked, id,
	)
	if err != nil {
		return fmt.Errorf("update title: %w", err)
	}
	return nil
}

// UpdateRunEpisode sets the episode name for a run.
func (d *DB) UpdateRunEpisode(ctx context.Context, id string, episode string) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE runs SET episode = $1, updated_at = now() WHERE id = $2`,
		episode, id,
	)
	if err != nil {
		return fmt.Errorf("update episode: %w", err)
	}
	return nil
}

// ResetRunToStatus resets a run back to a given status, clearing error state.
func (d *DB) ResetRunToStatus(ctx context.Context, id string, status domain.RunStatus) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE runs SET status = $1, error_text = '', last_error = '', needs_review = FALSE, updated_at = now() WHERE id = $2`,
		string(status), id,
	)
	if err != nil {
		return fmt.Errorf("reset run to %s: %w", status, err)
	}
	return nil
}

// UpdateRunLastError sets the last_error field without changing status.
func (d *DB) UpdateRunLastError(ctx context.Context, id string, errText string) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE runs SET last_error = $1, updated_at = now() WHERE id = $2`,
		errText, id,
	)
	if err != nil {
		return fmt.Errorf("update last_error: %w", err)
	}
	return nil
}

// ResetAttempts resets all attempt counters to zero (used when retrying from UI).
func (d *DB) ResetAttempts(ctx context.Context, id string) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE runs SET script_attempt=0, voice_attempt=0, render_attempt=0, package_attempt=0, youtube_attempt=0,
		 needs_review=FALSE, last_error='', updated_at=now() WHERE id = $1`, id,
	)
	if err != nil {
		return fmt.Errorf("reset attempts: %w", err)
	}
	return nil
}

// ---------- fix engine ----------

// FixOutcomeRow is the DB-layer representation of a fix outcome.
type FixOutcomeRow struct {
	RunID          string
	Stage          string
	FailType       string
	FixPlanID      string
	AttemptsToPass int
	CostEstimate   float64
	Success        bool
	Timestamp      time.Time
}

// InsertFixOutcome persists a fix outcome record.
func (d *DB) InsertFixOutcome(ctx context.Context, o FixOutcomeRow) error {
	_, err := d.pool.ExecContext(ctx,
		`INSERT INTO fix_outcomes (run_id, stage, fail_type, fix_plan_id, attempts_to_pass, cost_estimate, success)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		o.RunID, o.Stage, o.FailType, o.FixPlanID, o.AttemptsToPass, o.CostEstimate, o.Success,
	)
	if err != nil {
		return fmt.Errorf("insert fix outcome: %w", err)
	}
	return nil
}

// ListFixOutcomes loads all historical fix outcomes for scorer bootstrap.
func (d *DB) ListFixOutcomes(ctx context.Context) ([]FixOutcomeRow, error) {
	rows, err := d.pool.QueryContext(ctx,
		`SELECT run_id, stage, fail_type, fix_plan_id, attempts_to_pass, cost_estimate, success, created_at
		 FROM fix_outcomes ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list fix outcomes: %w", err)
	}
	defer rows.Close()

	var results []FixOutcomeRow
	for rows.Next() {
		var r FixOutcomeRow
		if err := rows.Scan(&r.RunID, &r.Stage, &r.FailType, &r.FixPlanID,
			&r.AttemptsToPass, &r.CostEstimate, &r.Success, &r.Timestamp); err != nil {
			return nil, fmt.Errorf("scan fix outcome: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// UpdateActiveFixPlan sets the active fix plan and the starting attempt count.
func (d *DB) UpdateActiveFixPlan(ctx context.Context, id, planID string, startAttempt int) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE runs SET active_fix_plan_id = $1, active_fix_start_attempt = $2, updated_at = now()
		 WHERE id = $3`, planID, startAttempt, id)
	if err != nil {
		return fmt.Errorf("update active fix plan: %w", err)
	}
	return nil
}

// UpdatePolicyOverrides persists policy overrides as JSON on a run.
func (d *DB) UpdatePolicyOverrides(ctx context.Context, id, overridesJSON string) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE runs SET policy_overrides_json = $1, updated_at = now() WHERE id = $2`,
		overridesJSON, id)
	if err != nil {
		return fmt.Errorf("update policy overrides: %w", err)
	}
	return nil
}

// ApproveVoice marks a run as approved for TTS synthesis.
func (d *DB) ApproveVoice(ctx context.Context, id string) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE runs SET voice_approved = true, updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("approve voice: %w", err)
	}
	return nil
}

// ---------- run locking ----------

// ClaimNextRun atomically claims the next eligible run for processing.
// A run is eligible if it is not in a terminal state and is either unlocked
// or has an expired lock (older than 5 minutes).
// Inflight limits exclude statuses where the number of actively-locked runs >= cap.
// Returns nil, nil if no run is available.
func (d *DB) ClaimNextRun(ctx context.Context, workerID string, maxScript, maxTTS, maxRender int) (*domain.Run, error) {
	// Build per-stage inflight exclusions.
	inflightGate := buildInflightGate(maxScript, maxTTS, maxRender)

	r := &domain.Run{}
	err := scanRun(d.pool.QueryRowContext(ctx,
		`UPDATE runs
		 SET locked_by = $1, locked_at = now(), updated_at = now()
		 WHERE id = (
		     SELECT id FROM runs
		     WHERE status NOT IN ('DONE','FAILED','NEEDS_REVIEW')
		       AND NOT (status = 'SCRIPTED' AND voice_approved = false)
		       `+inflightGate+`
		       AND (locked_by IS NULL OR locked_at < now() - interval '5 minutes')
		     ORDER BY created_at ASC
		     LIMIT 1
		     FOR UPDATE SKIP LOCKED
		 )
		 RETURNING `+runColumns, workerID), r)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim next run: %w", err)
	}
	return r, nil
}

// buildInflightGate returns SQL AND clauses that exclude statuses where the
// number of actively-locked runs has reached the per-stage cap. 0 = unlimited.
func buildInflightGate(maxScript, maxTTS, maxRender int) string {
	var b strings.Builder
	// stage → status mapping: script=PENDING, tts=SCRIPTED, render=THUMBNAILED
	type pair struct {
		status string
		limit  int
	}
	for _, p := range []pair{
		{"PENDING", maxScript},
		{"SCRIPTED", maxTTS},
		{"THUMBNAILED", maxRender},
	} {
		if p.limit > 0 {
			fmt.Fprintf(&b,
				`AND NOT (status = '%s' AND (SELECT count(*) FROM runs r2 WHERE r2.status = '%s' AND r2.locked_by IS NOT NULL AND r2.locked_at >= now() - interval '5 minutes') >= %d) `,
				p.status, p.status, p.limit)
		}
	}
	return b.String()
}

// ReleaseRun releases the lock on a run after processing one stage.
func (d *DB) ReleaseRun(ctx context.Context, id string) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE runs SET locked_by = NULL, locked_at = NULL, updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("release run: %w", err)
	}
	return nil
}

// RenewLock extends the lock TTL for a run being processed.
func (d *DB) RenewLock(ctx context.Context, id string, workerID string) error {
	res, err := d.pool.ExecContext(ctx,
		`UPDATE runs SET locked_at = now(), updated_at = now() WHERE id = $1 AND locked_by = $2`, id, workerID)
	if err != nil {
		return fmt.Errorf("renew lock: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("lock lost for run %s (worker %s)", id, workerID)
	}
	return nil
}

// ---------- assets ----------

// InsertAsset records a produced asset.
func (d *DB) InsertAsset(ctx context.Context, runID, kind, path string) error {
	_, err := d.pool.ExecContext(ctx,
		`INSERT INTO assets (run_id, kind, path) VALUES ($1, $2, $3)`,
		runID, kind, path,
	)
	if err != nil {
		return fmt.Errorf("insert asset: %w", err)
	}
	return nil
}

// GetAsset retrieves an asset by run and kind.
func (d *DB) GetAsset(ctx context.Context, runID, kind string) (*domain.Asset, error) {
	a := &domain.Asset{}
	err := d.pool.QueryRowContext(ctx,
		`SELECT id, run_id, kind, path, created_at
		 FROM assets WHERE run_id = $1 AND kind = $2
		 ORDER BY created_at DESC LIMIT 1`, runID, kind,
	).Scan(&a.ID, &a.RunID, &a.Kind, &a.Path, &a.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get asset %s/%s: %w", runID, kind, err)
	}
	return a, nil
}

// UpdateAssetPath updates the file path of an existing asset.
func (d *DB) UpdateAssetPath(ctx context.Context, id, path string) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE assets SET path = $1 WHERE id = $2`, path, id,
	)
	if err != nil {
		return fmt.Errorf("update asset path: %w", err)
	}
	return nil
}

// ---------- job_queue ----------

// DequeueJob atomically claims the oldest PENDING job.
// Returns nil, nil when the queue is empty.
func (d *DB) DequeueJob(ctx context.Context) (*domain.Job, error) {
	j := &domain.Job{}
	err := d.pool.QueryRowContext(ctx,
		`UPDATE job_queue
		 SET status = 'RUNNING', started_at = now()
		 WHERE id = (
		     SELECT id FROM job_queue
		     WHERE status = 'PENDING'
		     ORDER BY created_at ASC
		     LIMIT 1
		     FOR UPDATE SKIP LOCKED
		 )
		 RETURNING id, run_id, job_type, status, error_text, created_at, started_at, finished_at`,
	).Scan(&j.ID, &j.RunID, &j.JobType, &j.Status, &j.ErrorText,
		&j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dequeue job: %w", err)
	}
	return j, nil
}

// CompleteJob marks a job as successfully finished.
func (d *DB) CompleteJob(ctx context.Context, id string) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE job_queue SET status = 'DONE', finished_at = now() WHERE id = $1`, id,
	)
	return err
}

// FailJob marks a job as failed with an error message.
func (d *DB) FailJob(ctx context.Context, id string, errText string) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE job_queue SET status = 'FAILED', error_text = $1, finished_at = now() WHERE id = $2`,
		errText, id,
	)
	return err
}

// ---------- API helpers ----------

// ListRuns returns all runs ordered by created_at DESC.
// If status is non-empty, only runs matching that status are returned.
func (d *DB) ListRuns(ctx context.Context, status string) ([]domain.Run, error) {
	var rows *sql.Rows
	var err error
	if status != "" {
		rows, err = d.pool.QueryContext(ctx,
			`SELECT `+runColumns+` FROM runs WHERE status = $1 ORDER BY created_at DESC`, status)
	} else {
		rows, err = d.pool.QueryContext(ctx,
			`SELECT `+runColumns+` FROM runs ORDER BY created_at DESC`)
	}
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var runs []domain.Run
	for rows.Next() {
		var r domain.Run
		if err := scanRun(rows, &r); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// HasRunToday returns true if a run with the given style was already created today.
func (d *DB) HasRunToday(ctx context.Context, style string) (bool, error) {
	var exists bool
	err := d.pool.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM runs WHERE style=$1 AND created_at::date = CURRENT_DATE)`,
		style,
	).Scan(&exists)
	return exists, err
}

// CreateRun inserts a new run and returns it.
func (d *DB) CreateRun(ctx context.Context, series, episode, style, language string, durationMin int) (*domain.Run, error) {
	r := &domain.Run{}
	err := scanRun(d.pool.QueryRowContext(ctx,
		`INSERT INTO runs (series, episode, style, language, duration_min)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING `+runColumns,
		series, episode, style, language, durationMin), r)
	if err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	return r, nil
}

// EnqueueJob inserts a new job into the queue for the given run.
func (d *DB) EnqueueJob(ctx context.Context, runID, jobType string) error {
	_, err := d.pool.ExecContext(ctx,
		`INSERT INTO job_queue (run_id, job_type) VALUES ($1, $2)`,
		runID, jobType,
	)
	if err != nil {
		return fmt.Errorf("enqueue job: %w", err)
	}
	return nil
}

// ListAssets returns all assets for a run.
func (d *DB) ListAssets(ctx context.Context, runID string) ([]domain.Asset, error) {
	rows, err := d.pool.QueryContext(ctx,
		`SELECT id, run_id, kind, path, created_at
		 FROM assets WHERE run_id = $1 ORDER BY created_at ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list assets: %w", err)
	}
	defer rows.Close()

	var assets []domain.Asset
	for rows.Next() {
		var a domain.Asset
		if err := rows.Scan(&a.ID, &a.RunID, &a.Kind, &a.Path, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan asset: %w", err)
		}
		assets = append(assets, a)
	}
	return assets, rows.Err()
}

// ListJobs returns all jobs for a run ordered by created_at.
func (d *DB) ListJobs(ctx context.Context, runID string) ([]domain.Job, error) {
	rows, err := d.pool.QueryContext(ctx,
		`SELECT id, run_id, job_type, status, error_text, created_at, started_at, finished_at
		 FROM job_queue WHERE run_id = $1 ORDER BY created_at ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var jobs []domain.Job
	for rows.Next() {
		var j domain.Job
		if err := rows.Scan(&j.ID, &j.RunID, &j.JobType, &j.Status, &j.ErrorText,
			&j.CreatedAt, &j.StartedAt, &j.FinishedAt); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ---------- worker_settings ----------

// GetWorkerSettings reads the singleton worker settings row.
func (d *DB) GetWorkerSettings(ctx context.Context) (*domain.WorkerSettings, error) {
	s := &domain.WorkerSettings{}
	err := d.pool.QueryRowContext(ctx,
		`SELECT mode,
		        worker_mode, require_voice_approval, max_inflight_script, max_inflight_tts, max_inflight_render,
		        openai_api_key, openai_base_url, openai_model,
		        elevenlabs_api_key, elevenlabs_voice_id, elevenlabs_model_id, elevenlabs_speed,
		        edge_voice, edge_rate, normalize, music_path,
		        youtube_enabled, youtube_privacy, youtube_client_id, youtube_client_secret,
		        youtube_access_token, youtube_refresh_token, youtube_token_expiry,
		        updated_at
		 FROM worker_settings WHERE id = 1`,
	).Scan(&s.Mode,
		&s.WorkerMode, &s.RequireVoiceApproval, &s.MaxInflightScript, &s.MaxInflightTTS, &s.MaxInflightRender,
		&s.OpenAIAPIKey, &s.OpenAIBaseURL, &s.OpenAIModel,
		&s.ElevenLabsAPIKey, &s.ElevenLabsVoiceID, &s.ElevenLabsModelID, &s.ElevenLabsSpeed,
		&s.EdgeVoice, &s.EdgeRate, &s.Normalize, &s.MusicPath,
		&s.YouTubeEnabled, &s.YouTubePrivacy, &s.YouTubeClientID, &s.YouTubeClientSecret,
		&s.YouTubeAccessToken, &s.YouTubeRefreshToken, &s.YouTubeTokenExpiry,
		&s.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get worker settings: %w", err)
	}
	return s, nil
}

// SaveWorkerSettings updates the singleton worker settings row.
func (d *DB) SaveWorkerSettings(ctx context.Context, s *domain.WorkerSettings) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE worker_settings SET
			mode = $1,
			worker_mode = $2, require_voice_approval = $3, max_inflight_script = $4, max_inflight_tts = $5, max_inflight_render = $6,
			openai_api_key = $7, openai_base_url = $8, openai_model = $9,
			elevenlabs_api_key = $10, elevenlabs_voice_id = $11, elevenlabs_model_id = $12, elevenlabs_speed = $13,
			edge_voice = $14, edge_rate = $15, normalize = $16, music_path = $17,
			youtube_enabled = $18, youtube_privacy = $19, youtube_client_id = $20, youtube_client_secret = $21,
			youtube_access_token = $22, youtube_refresh_token = $23, youtube_token_expiry = $24,
			updated_at = now()
		 WHERE id = 1`,
		s.Mode,
		s.WorkerMode, s.RequireVoiceApproval, s.MaxInflightScript, s.MaxInflightTTS, s.MaxInflightRender,
		s.OpenAIAPIKey, s.OpenAIBaseURL, s.OpenAIModel,
		s.ElevenLabsAPIKey, s.ElevenLabsVoiceID, s.ElevenLabsModelID, s.ElevenLabsSpeed,
		s.EdgeVoice, s.EdgeRate, s.Normalize, s.MusicPath,
		s.YouTubeEnabled, s.YouTubePrivacy, s.YouTubeClientID, s.YouTubeClientSecret,
		s.YouTubeAccessToken, s.YouTubeRefreshToken, s.YouTubeTokenExpiry,
	)
	if err != nil {
		return fmt.Errorf("save worker settings: %w", err)
	}
	return nil
}

// ---------- YouTube token store ----------

// GetYouTubeToken reads the OAuth2 token from worker_settings.
func (d *DB) GetYouTubeToken(ctx context.Context) (*oauth2.Token, error) {
	var access, refresh string
	var expiry time.Time
	err := d.pool.QueryRowContext(ctx,
		`SELECT youtube_access_token, youtube_refresh_token, youtube_token_expiry FROM worker_settings WHERE id = 1`,
	).Scan(&access, &refresh, &expiry)
	if err != nil {
		return nil, fmt.Errorf("get youtube token: %w", err)
	}
	if refresh == "" {
		return nil, nil
	}
	return &oauth2.Token{
		AccessToken:  access,
		RefreshToken: refresh,
		Expiry:       expiry,
		TokenType:    "Bearer",
	}, nil
}

// SaveYouTubeToken persists an OAuth2 token to worker_settings.
func (d *DB) SaveYouTubeToken(ctx context.Context, tok *oauth2.Token) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE worker_settings SET youtube_access_token = $1, youtube_refresh_token = $2, youtube_token_expiry = $3, updated_at = now() WHERE id = 1`,
		tok.AccessToken, tok.RefreshToken, tok.Expiry,
	)
	if err != nil {
		return fmt.Errorf("save youtube token: %w", err)
	}
	return nil
}

// ClearYouTubeToken removes the stored YouTube OAuth2 token.
func (d *DB) ClearYouTubeToken(ctx context.Context) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE worker_settings SET youtube_access_token = '', youtube_refresh_token = '', youtube_token_expiry = '1970-01-01', updated_at = now() WHERE id = 1`,
	)
	if err != nil {
		return fmt.Errorf("clear youtube token: %w", err)
	}
	return nil
}

// SetYouTubeVideoID records the YouTube video ID for a run.
func (d *DB) SetYouTubeVideoID(ctx context.Context, runID, videoID string) error {
	_, err := d.pool.ExecContext(ctx,
		`UPDATE runs SET youtube_video_id = $1, updated_at = now() WHERE id = $2`,
		videoID, runID,
	)
	if err != nil {
		return fmt.Errorf("set youtube video id: %w", err)
	}
	return nil
}

// DeleteRun deletes a run and all associated assets and jobs (via CASCADE).
func (d *DB) DeleteRun(ctx context.Context, id string) error {
	res, err := d.pool.ExecContext(ctx, `DELETE FROM runs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete run: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
