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

// Deps bundles every dependency the worker needs.
type Deps struct {
	DB     *db.DB
	Store  storage.Store
	LLM    *llm.Client
	TTS    TTSSynthesizer
	Image  *image.Client
	Render render.RenderConfig
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
		run, err := deps.DB.ClaimNextRun(ctx, workerID)
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
		stepErr = stepScript(ctx, deps, run)
		if stepErr == nil {
			report = qaScript(ctx, deps, run, policy)
		}

	case domain.StatusScripted:
		_ = deps.DB.IncrementAttempt(ctx, run.ID, "voice_attempt")
		stepErr = stepTTS(ctx, deps, run)
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
		stepErr = stepRender(ctx, deps, run)
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
		// Final advancement to DONE — run final QA.
		report = qaPackage(ctx, deps, run)
		if report.Pass {
			if err := deps.DB.UpdateRunStatus(ctx, run.ID, domain.StatusDone); err != nil {
				return fmt.Errorf("advance to DONE: %w", err)
			}
			log.Printf("worker[%s]: run %s → DONE", workerID, run.ID)
			return nil
		}
		// Final QA failed — fall through to decision handling below.

	default:
		return fmt.Errorf("unexpected run status: %s", run.Status)
	}

	// Handle step execution errors (transient vs fatal).
	if stepErr != nil {
		_ = deps.DB.UpdateRunLastError(ctx, run.ID, stepErr.Error())

		if isTransientError(stepErr) {
			// Re-read for updated attempt counters.
			run, _ = deps.DB.GetRun(ctx, run.ID)
			totalAttempts := run.ScriptAttempt + run.VoiceAttempt + run.RenderAttempt + run.PackageAttempt
			if totalAttempts > 10 {
				return deps.DB.SetNeedsReview(ctx, run.ID, fmt.Sprintf("transient error after %d total attempts: %v", totalAttempts, stepErr))
			}
			// Don't sleep here — just leave the run in the same status.
			// The next poll iteration will pick it up after pollInterval.
			log.Printf("worker[%s]: transient error on run %s, will retry on next claim: %v", workerID, run.ID, stepErr)
			return nil
		}

		// Non-transient step error — create a synthetic QA report for decision.
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

	// If QA passed, advance to next status.
	if report.Pass {
		next := domain.NextStatus(stage)
		if err := deps.DB.UpdateRunStatus(ctx, run.ID, next); err != nil {
			return fmt.Errorf("advance to %s: %w", next, err)
		}
		log.Printf("worker[%s]: run %s → %s (QA passed)", workerID, run.ID, next)
		return nil
	}

	// QA failed — re-read run for latest attempt counts.
	run, err := deps.DB.GetRun(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("get run for decision: %w", err)
	}

	// Stage-aware decision.
	decision := Decide(stage, report, run, policy)
	log.Printf("worker[%s]: run %s decision=%s reason=%s", workerID, run.ID, decision.Action, decision.Reason)
	_ = deps.DB.UpdateRunLastError(ctx, run.ID, decision.Reason)

	switch decision.Action {
	case "advance":
		next := domain.NextStatus(stage)
		if err := deps.DB.UpdateRunStatus(ctx, run.ID, next); err != nil {
			return fmt.Errorf("advance to %s: %w", next, err)
		}
		log.Printf("worker[%s]: run %s → %s", workerID, run.ID, next)

	case "retry":
		// Leave at target status — next poll will re-claim and re-execute.
		if err := deps.DB.UpdateRunStatus(ctx, run.ID, decision.TargetStatus); err != nil {
			return fmt.Errorf("reset to %s for retry: %w", decision.TargetStatus, err)
		}
		log.Printf("worker[%s]: run %s reset to %s for retry", workerID, run.ID, decision.TargetStatus)

	case "loopback":
		if err := deps.DB.UpdateRunStatus(ctx, run.ID, decision.TargetStatus); err != nil {
			return fmt.Errorf("loopback to %s: %w", decision.TargetStatus, err)
		}
		log.Printf("worker[%s]: run %s looped back to %s", workerID, run.ID, decision.TargetStatus)

	case "needs_review":
		if err := deps.DB.SetNeedsReview(ctx, run.ID, decision.Reason); err != nil {
			return fmt.Errorf("set needs review: %w", err)
		}
		log.Printf("worker[%s]: run %s → NEEDS_REVIEW: %s", workerID, run.ID, decision.Reason)

	default:
		return fmt.Errorf("unknown decision action: %s", decision.Action)
	}

	return nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
