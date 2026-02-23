package jobs

import (
	"context"
	"time"

	"sleepy/internal/db"
)

// FixOutcome records the result of applying a FixPlan.
type FixOutcome struct {
	RunID          string
	Stage          string
	FailType       string
	FixPlanID      string
	AttemptsToPass int
	CostEstimate   float64
	Success        bool
	Timestamp      time.Time
}

// LogFixOutcome persists a fix outcome to the database.
func LogFixOutcome(ctx context.Context, d *db.DB, outcome FixOutcome) error {
	return d.InsertFixOutcome(ctx, db.FixOutcomeRow{
		RunID:          outcome.RunID,
		Stage:          outcome.Stage,
		FailType:       outcome.FailType,
		FixPlanID:      outcome.FixPlanID,
		AttemptsToPass: outcome.AttemptsToPass,
		CostEstimate:   outcome.CostEstimate,
		Success:        outcome.Success,
		Timestamp:      outcome.Timestamp,
	})
}

// LoadFixOutcomes retrieves all historical fix outcomes for scorer bootstrapping.
func LoadFixOutcomes(ctx context.Context, d *db.DB) ([]FixOutcome, error) {
	rows, err := d.ListFixOutcomes(ctx)
	if err != nil {
		return nil, err
	}
	outcomes := make([]FixOutcome, len(rows))
	for i, r := range rows {
		outcomes[i] = FixOutcome{
			RunID:          r.RunID,
			Stage:          r.Stage,
			FailType:       r.FailType,
			FixPlanID:      r.FixPlanID,
			AttemptsToPass: r.AttemptsToPass,
			CostEstimate:   r.CostEstimate,
			Success:        r.Success,
			Timestamp:      r.Timestamp,
		}
	}
	return outcomes, nil
}

// ComputeAttemptsToPass calculates how many attempts were spent on the current fix plan.
func ComputeAttemptsToPass(currentAttempt, startAttempt int) int {
	n := currentAttempt - startAttempt + 1
	if n < 1 {
		return 1
	}
	return n
}
