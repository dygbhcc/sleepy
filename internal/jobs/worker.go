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

// RunWorker polls the job queue and processes RUN_PIPELINE jobs until ctx is
// cancelled.
func RunWorker(ctx context.Context, deps Deps, pollInterval time.Duration, oneShot bool) {
	if oneShot {
		log.Println("worker: started in one-shot mode (exit after job completes)")
	} else {
		log.Println("worker: started, polling every", pollInterval)
	}
	for {
		select {
		case <-ctx.Done():
			log.Println("worker: shutting down")
			return
		default:
		}

		job, err := deps.DB.DequeueJob(ctx)
		if err != nil {
			log.Printf("worker: dequeue error: %v", err)
			sleep(ctx, pollInterval)
			continue
		}
		if job == nil {
			if oneShot {
				log.Println("worker: no pending jobs, exiting (one-shot mode)")
				return
			}
			sleep(ctx, pollInterval)
			continue
		}

		log.Printf("worker: picked up job %s (run=%s type=%s)", job.ID, job.RunID, job.JobType)

		if job.JobType != JobTypeRunPipeline {
			msg := fmt.Sprintf("unknown job type: %s", job.JobType)
			log.Printf("worker: %s", msg)
			_ = deps.DB.FailJob(ctx, job.ID, msg)
			continue
		}

		if err := runPipeline(ctx, deps, job); err != nil {
			log.Printf("worker: run %s failed: %v", job.RunID, err)
			_ = deps.DB.FailJob(ctx, job.ID, err.Error())
			_ = deps.DB.FailRun(ctx, job.RunID, err.Error())
		} else {
			_ = deps.DB.CompleteJob(ctx, job.ID)
			log.Printf("worker: run %s completed successfully", job.RunID)
		}

		if oneShot {
			log.Println("worker: exiting (one-shot mode)")
			return
		}
	}
}

// runPipeline loops through pipeline steps based on the run's current status
// until the run reaches DONE or an error occurs.
func runPipeline(ctx context.Context, deps Deps, job *domain.Job) error {
	for {
		run, err := deps.DB.GetRun(ctx, job.RunID)
		if err != nil {
			return fmt.Errorf("get run: %w", err)
		}

		if run.Status == domain.StatusDone || run.Status == domain.StatusFailed {
			return nil
		}

		var stepErr error

		switch run.Status {
		case domain.StatusPending:
			stepErr = stepScript(ctx, deps, run)
		case domain.StatusScripted:
			stepErr = stepTTS(ctx, deps, run)
		case domain.StatusVoiced:
			stepErr = stepThumbnail(ctx, deps, run)
		case domain.StatusThumbnailed:
			stepErr = stepRender(ctx, deps, run)
		case domain.StatusRendered:
			stepErr = stepPackage(ctx, deps, run)
		case domain.StatusPackaged:
			// Terminal advancement.
			stepErr = nil
		default:
			return fmt.Errorf("unexpected run status: %s", run.Status)
		}

		if stepErr != nil {
			return fmt.Errorf("step %s failed: %w", run.Status, stepErr)
		}

		next := domain.NextStatus(run.Status)
		if err := deps.DB.UpdateRunStatus(ctx, run.ID, next); err != nil {
			return fmt.Errorf("advance to %s: %w", next, err)
		}
		log.Printf("worker: run %s → %s", run.ID, next)
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
