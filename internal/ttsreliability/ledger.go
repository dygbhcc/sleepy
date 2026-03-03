package ttsreliability

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Ledger tracks TTS synthesis attempts and costs in the database.
type Ledger struct {
	db *sql.DB
}

// NewLedger creates a ledger backed by the given database connection.
func NewLedger(db *sql.DB) *Ledger {
	return &Ledger{db: db}
}

// RecordAttempt inserts a row into tts_attempts for every synthesis attempt.
func (l *Ledger) RecordAttempt(ctx context.Context, runID string, chunkIdx, attemptNum, charCount int,
	success bool, costUSD float64, failType FailType, metrics QAMetrics, settings TTSSettings,
	idempotencyKey, artifactPath string) error {

	metricsJSON, _ := json.Marshal(metrics)
	settingsJSON, _ := json.Marshal(settings)

	_, err := l.db.ExecContext(ctx, `
		INSERT INTO tts_attempts (run_id, chunk_index, attempt_num, idempotency_key,
			settings_json, metrics_json, qa_pass, qa_fail_type,
			char_count, cost_usd, artifact_path)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		runID, chunkIdx, attemptNum, idempotencyKey,
		settingsJSON, metricsJSON, success, string(failType),
		charCount, costUSD, artifactPath,
	)
	if err != nil {
		return fmt.Errorf("record attempt: %w", err)
	}
	return nil
}

// RecordCost inserts a row into tts_cost_ledger.
func (l *Ledger) RecordCost(ctx context.Context, runID string, chunkIdx, charCount int, costUSD float64) error {
	_, err := l.db.ExecContext(ctx, `
		INSERT INTO tts_cost_ledger (run_id, chunk_index, char_count, cost_usd)
		VALUES ($1, $2, $3, $4)`,
		runID, chunkIdx, charCount, costUSD,
	)
	if err != nil {
		return fmt.Errorf("record cost: %w", err)
	}
	return nil
}

// GetTotalCost returns the cumulative cost for a run.
func (l *Ledger) GetTotalCost(ctx context.Context, runID string) (float64, error) {
	var total sql.NullFloat64
	err := l.db.QueryRowContext(ctx,
		`SELECT SUM(cost_usd) FROM tts_cost_ledger WHERE run_id = $1`, runID,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("get total cost: %w", err)
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Float64, nil
}

// GetTotalAttempts returns the total number of synthesis attempts for a run.
func (l *Ledger) GetTotalAttempts(ctx context.Context, runID string) (int, error) {
	var count int
	err := l.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tts_attempts WHERE run_id = $1`, runID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("get total attempts: %w", err)
	}
	return count, nil
}

// GetAttemptKey returns the idempotency key for a specific attempt, or "" if not found.
func (l *Ledger) GetAttemptKey(ctx context.Context, runID string, chunkIdx, attemptNum int) (string, error) {
	var key sql.NullString
	err := l.db.QueryRowContext(ctx,
		`SELECT idempotency_key FROM tts_attempts WHERE run_id=$1 AND chunk_index=$2 AND attempt_num=$3 LIMIT 1`,
		runID, chunkIdx, attemptNum,
	).Scan(&key)
	if err != nil {
		return "", err
	}
	if !key.Valid {
		return "", nil
	}
	return key.String, nil
}

// IdempotencyKey builds a deterministic key for deduplication.
func IdempotencyKey(runID string, chunkIdx, attemptNum int, settings TTSSettings) string {
	sJSON, _ := json.Marshal(settings)
	return fmt.Sprintf("%s:chunk%d:attempt%d:%s", runID, chunkIdx, attemptNum, string(sJSON))
}
