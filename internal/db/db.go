package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"

	"sleepy/internal/domain"
)

// DB wraps a Postgres connection pool.
type DB struct {
	pool *sql.DB
}

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

// GetRun loads a run by ID.
func (d *DB) GetRun(ctx context.Context, id string) (*domain.Run, error) {
	r := &domain.Run{}
	err := d.pool.QueryRowContext(ctx,
		`SELECT id, series, episode, style, duration_min, status, error_text, created_at, updated_at
		 FROM runs WHERE id = $1`, id,
	).Scan(&r.ID, &r.Series, &r.Episode, &r.Style, &r.DurationMin,
		&r.Status, &r.ErrorText, &r.CreatedAt, &r.UpdatedAt)
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
		`UPDATE runs SET status = $1, error_text = $2, updated_at = now() WHERE id = $3`,
		string(domain.StatusFailed), errText, id,
	)
	if err != nil {
		return fmt.Errorf("fail run: %w", err)
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
			`SELECT id, series, episode, style, duration_min, status, error_text, created_at, updated_at
			 FROM runs WHERE status = $1 ORDER BY created_at DESC`, status)
	} else {
		rows, err = d.pool.QueryContext(ctx,
			`SELECT id, series, episode, style, duration_min, status, error_text, created_at, updated_at
			 FROM runs ORDER BY created_at DESC`)
	}
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var runs []domain.Run
	for rows.Next() {
		var r domain.Run
		if err := rows.Scan(&r.ID, &r.Series, &r.Episode, &r.Style, &r.DurationMin,
			&r.Status, &r.ErrorText, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// CreateRun inserts a new run and returns it.
func (d *DB) CreateRun(ctx context.Context, series, episode, style string, durationMin int) (*domain.Run, error) {
	r := &domain.Run{}
	err := d.pool.QueryRowContext(ctx,
		`INSERT INTO runs (series, episode, style, duration_min)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, series, episode, style, duration_min, status, error_text, created_at, updated_at`,
		series, episode, style, durationMin,
	).Scan(&r.ID, &r.Series, &r.Episode, &r.Style, &r.DurationMin,
		&r.Status, &r.ErrorText, &r.CreatedAt, &r.UpdatedAt)
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
