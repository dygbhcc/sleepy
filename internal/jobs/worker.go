package jobs

import (
	"context"
	"fmt"
	"log"
	"time"

	"sleepy/internal/db"
	"sleepy/internal/domain"
	"sleepy/internal/providers/image"
	"sleepy/internal/providers/llm"
	"sleepy/internal/render"
	"sleepy/internal/storage"
)

// TTSSynthesizer is implemented by both tts.Client and tts.EdgeClient.
type TTSSynthesizer interface {
	Synthesize(ctx context.Context, text string, outPath string) error
}

// LanguageAwareTTS extends TTSSynthesizer with per-call language support.
type LanguageAwareTTS interface {
	TTSSynthesizer
	SynthesizeWithLang(ctx context.Context, text string, outPath string, lang string) error
}

// EdgeTTSOverridable is implemented by Edge TTS clients that support rate overrides.
type EdgeTTSOverridable interface {
	SynthesizeWithOpts(ctx context.Context, text string, outPath string, rateDelta int, lang string) error
}

// ElevenLabsOverridable is implemented by ElevenLabs clients that support parameter overrides.
type ElevenLabsOverridable interface {
	SynthesizeWithOpts(ctx context.Context, text string, outPath string, speedFactor, stability, similarityBoost float64) error
}

// Deps bundles every dependency the worker needs.
type Deps struct {
	DB        *db.DB
	Store     storage.Store
	LLM       llm.ScriptGenerator
	TTS       TTSSynthesizer
	Image     *image.Client
	Render    render.RenderConfig
	FixEngine *FixEngine // nil = legacy Decide() path
	InflightLimits InflightLimits // per-stage concurrency caps (0 = unlimited)
}

// InflightLimits caps how many runs can be actively processed per stage.
// A zero value means unlimited.
type InflightLimits struct {
	Script int // max locked runs in PENDING (script generation)
	TTS    int // max locked runs in SCRIPTED (TTS synthesis)
	Render int // max locked runs in THUMBNAILED (video render)
}

// RunWorker is a global worker loop that claims the next eligible run,
// processes exactly ONE stage, then releases the lock and loops.
// This allows multiple workers to process different runs concurrently,
// and prevents any single run from monopolizing a worker.
func RunWorker(ctx context.Context, deps Deps, pollInterval time.Duration, oneShot bool, workerID string) {
	if workerID == "" {
		workerID = fmt.Sprintf("worker-%d", time.Now().UnixNano())
	}
	if oneShot {
		log.Printf("worker[%s]: started in one-shot mode", workerID)
	} else {
		log.Printf("worker[%s]: started, polling every %s", workerID, pollInterval)
	}

	for {
		select {
		case <-ctx.Done():
			log.Printf("worker[%s]: shutting down", workerID)
			return
		default:
		}

		// Claim the next eligible run (atomic, skip-locked).
		run, err := deps.DB.ClaimNextRun(ctx, workerID,
			deps.InflightLimits.Script, deps.InflightLimits.TTS, deps.InflightLimits.Render)
		if err != nil {
			log.Printf("worker[%s]: claim error: %v", workerID, err)
			sleep(ctx, pollInterval)
			continue
		}
		if run == nil {
			if oneShot {
				log.Printf("worker[%s]: no eligible runs, exiting (one-shot)", workerID)
				return
			}
			sleep(ctx, pollInterval)
			continue
		}

		log.Printf("worker[%s]: claimed run %s (status=%s)", workerID, run.ID, run.Status)

		// Process exactly one stage, then release.
		err = processOneStage(ctx, deps, run, workerID)
		if err != nil {
			log.Printf("worker[%s]: run %s stage error: %v", workerID, run.ID, err)
		}

		// Always release the lock, even on error.
		if releaseErr := deps.DB.ReleaseRun(ctx, run.ID); releaseErr != nil {
			log.Printf("worker[%s]: failed to release run %s: %v", workerID, run.ID, releaseErr)
		}

		if oneShot {
			log.Printf("worker[%s]: exiting (one-shot)", workerID)
			return
		}
	}
}

// processOneStage executes exactly one pipeline stage for the given run,
// runs the QA gate, and either advances the run or handles the failure.
func processOneStage(ctx context.Context, deps Deps, run *domain.Run, workerID string) error {
	policy := PolicyForDuration(run.DurationMin)

	// Load persisted fix overrides (survive worker restarts).
	loadPersistedOverrides(run.PolicyOverridesJSON, &policy)
	if run.PolicyOverridesJSON != "" && run.PolicyOverridesJSON != "{}" {
		log.Printf("worker[%s]: loaded persisted overrides for run %s", workerID, run.ID)
	}

	// Terminal states — nothing to do.
	if run.Status == domain.StatusDone || run.Status == domain.StatusFailed || run.Status == domain.StatusNeedsReview {
		return nil
	}

	var stepErr error
	var report QAReport
	stage := run.Status

	switch stage {
	case domain.StatusPending:
		_ = deps.DB.IncrementAttempt(ctx, run.ID, "script_attempt")
		stepErr = stepScript(ctx, deps, run, policy)
		if stepErr == nil {
			report = qaScript(ctx, deps, run, policy)
		}

	case domain.StatusScripted:
		_ = deps.DB.IncrementAttempt(ctx, run.ID, "voice_attempt")
		stepErr = stepTTS(ctx, deps, run, policy)
		if stepErr == nil {
			report = qaVoice(ctx, deps, run, policy)
		}

	case domain.StatusVoiced:
		stepErr = stepThumbnail(ctx, deps, run)
		if stepErr == nil {
			report = qaThumbnail(ctx, deps, run)
		}

	case domain.StatusThumbnailed:
		_ = deps.DB.IncrementAttempt(ctx, run.ID, "render_attempt")
		stepErr = stepRender(ctx, deps, run, policy)
		if stepErr == nil {
			report = qaRender(ctx, deps, run, policy)
		}

	case domain.StatusRendered:
		_ = deps.DB.IncrementAttempt(ctx, run.ID, "package_attempt")
		stepErr = stepPackage(ctx, deps, run)
		if stepErr == nil {
			report = qaPackage(ctx, deps, run)
		}

	case domain.StatusPackaged:
		report = qaPackage(ctx, deps, run)
		if report.Pass {
			clearFixState(ctx, deps.DB, run.ID)
			if err := deps.DB.UpdateRunStatus(ctx, run.ID, domain.StatusDone); err != nil {
				return fmt.Errorf("advance to DONE: %w", err)
			}
			log.Printf("worker[%s]: run %s → DONE", workerID, run.ID)
			return nil
		}

	default:
		return fmt.Errorf("unexpected run status: %s", run.Status)
	}

	// Handle step execution errors (transient vs fatal).
	if stepErr != nil {
		_ = deps.DB.UpdateRunLastError(ctx, run.ID, stepErr.Error())

		if isTransientError(stepErr) {
			run, _ = deps.DB.GetRun(ctx, run.ID)
			totalAttempts := run.ScriptAttempt + run.VoiceAttempt + run.RenderAttempt + run.PackageAttempt
			if totalAttempts > 20 {
				return deps.DB.SetNeedsReview(ctx, run.ID, fmt.Sprintf("transient error after %d total attempts: %v", totalAttempts, stepErr))
			}
			log.Printf("worker[%s]: transient error on run %s, will retry on next claim: %v", workerID, run.ID, stepErr)
			return nil
		}

		report = QAReport{
			Stage:     string(stage),
			RunID:     run.ID,
			Timestamp: time.Now(),
			Pass:      false,
			FailType:  FailUnknown,
			Checks:    []QACheck{{Name: "step_execution", Pass: false, Details: stepErr.Error()}},
		}
	}

	// Write QA report.
	if err := writeQAReport(ctx, deps, run, report); err != nil {
		log.Printf("worker[%s]: failed to write QA report: %v", workerID, err)
	}

	// Re-read run for latest attempt counts.
	run, err := deps.DB.GetRun(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("get run for decision: %w", err)
	}

	// If QA passed, record outcome (success), clear fix state, advance.
	if report.Pass {
		recordFixOutcome(ctx, deps, run, stage, report, true)
		clearFixState(ctx, deps.DB, run.ID)

		next := domain.NextStatus(stage)
		if err := deps.DB.UpdateRunStatus(ctx, run.ID, next); err != nil {
			return fmt.Errorf("advance to %s: %w", next, err)
		}
		log.Printf("worker[%s]: run %s → %s (QA passed)", workerID, run.ID, next)
		return nil
	}

	// QA failed — use FixEngine if available, else legacy Decide().
	var fix FixPlan
	if deps.FixEngine != nil {
		fix = deps.FixEngine.DecideFix(stage, report, run, policy)
		log.Printf("worker[%s]: run %s fix_engine → action=%s plan=%s", workerID, run.ID, fix.Action, fix.ID)
	} else {
		decision := Decide(stage, report, run, policy)
		fix = FixPlan{
			ID:           "legacy",
			Action:       decision.Action,
			TargetStatus: decision.TargetStatus,
			Stage:        stage,
			FailType:     report.FailType,
		}
		log.Printf("worker[%s]: run %s legacy → action=%s reason=%s", workerID, run.ID, decision.Action, decision.Reason)
	}

	_ = deps.DB.UpdateRunLastError(ctx, run.ID, fmt.Sprintf("fix=%s action=%s failType=%s", fix.ID, fix.Action, fix.FailType))

	switch fix.Action {
	case "advance":
		clearFixState(ctx, deps.DB, run.ID)
		next := domain.NextStatus(stage)
		if err := deps.DB.UpdateRunStatus(ctx, run.ID, next); err != nil {
			return fmt.Errorf("advance to %s: %w", next, err)
		}
		log.Printf("worker[%s]: run %s → %s", workerID, run.ID, next)

	case "retry", "loopback":
		// Track which fix plan is active for attempts_to_pass computation.
		if fix.ID != run.ActiveFixPlanID {
			currentAttempt := currentAttemptForStage(stage, run)
			_ = deps.DB.UpdateActiveFixPlan(ctx, run.ID, fix.ID, currentAttempt)
		}

		// Persist and apply overrides.
		if fix.Overrides != nil {
			if err := persistAndApply(ctx, deps.DB, run.ID, fix.Overrides, &policy); err != nil {
				log.Printf("worker[%s]: failed to persist overrides: %v", workerID, err)
			}
		}

		if err := deps.DB.UpdateRunStatus(ctx, run.ID, fix.TargetStatus); err != nil {
			return fmt.Errorf("reset to %s: %w", fix.TargetStatus, err)
		}
		log.Printf("worker[%s]: run %s → %s (%s, plan=%s)", workerID, run.ID, fix.TargetStatus, fix.Action, fix.ID)

	case "needs_review":
		recordFixOutcome(ctx, deps, run, stage, report, false)
		if err := deps.DB.SetNeedsReview(ctx, run.ID, fmt.Sprintf("fix engine: %s (%s)", fix.ID, fix.FailType)); err != nil {
			return fmt.Errorf("set needs review: %w", err)
		}
		log.Printf("worker[%s]: run %s → NEEDS_REVIEW (plan=%s)", workerID, run.ID, fix.ID)

	default:
		return fmt.Errorf("unknown fix action: %s", fix.Action)
	}

	return nil
}

// recordFixOutcome logs an outcome if the fix engine is active and a fix plan was tracked.
func recordFixOutcome(ctx context.Context, deps Deps, run *domain.Run, stage domain.RunStatus, report QAReport, success bool) {
	if deps.FixEngine == nil || run.ActiveFixPlanID == "" {
		return
	}

	attemptsToPass := ComputeAttemptsToPass(
		currentAttemptForStage(stage, run),
		run.ActiveFixStartAttempt,
	)

	outcome := FixOutcome{
		RunID:          run.ID,
		Stage:          string(stage),
		FailType:       string(report.FailType),
		FixPlanID:      run.ActiveFixPlanID,
		AttemptsToPass: attemptsToPass,
		Success:        success,
		Timestamp:      time.Now(),
	}

	deps.FixEngine.RecordOutcome(outcome)
	if err := LogFixOutcome(ctx, deps.DB, outcome); err != nil {
		log.Printf("worker: failed to log fix outcome: %v", err)
	}
}

// currentAttemptForStage returns the current attempt count for the given stage.
func currentAttemptForStage(stage domain.RunStatus, run *domain.Run) int {
	switch stage {
	case domain.StatusPending:
		return run.ScriptAttempt
	case domain.StatusScripted:
		return run.VoiceAttempt
	case domain.StatusThumbnailed:
		return run.RenderAttempt
	case domain.StatusRendered:
		return run.PackageAttempt
	default:
		return 0
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
