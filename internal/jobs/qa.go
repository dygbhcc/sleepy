package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"os"
	"strings"
	"time"

	"sleepy/internal/domain"
	"sleepy/internal/errs"
	"sleepy/internal/providers/llm"
	"sleepy/internal/render"
)

// FailType categorises the reason a QA gate did not pass.
type FailType string

const (
	FailNone             FailType = ""
	FailBannedPhrase     FailType = "BANNED_PHRASE"
	FailWordcountLow     FailType = "WORDCOUNT_LOW"
	FailWordcountHigh    FailType = "WORDCOUNT_HIGH"
	FailPacingFail       FailType = "PACING_FAIL"
	FailAudioDurationOff FailType = "AUDIO_DURATION_OFF"
	FailAudioClipping    FailType = "AUDIO_CLIPPING"
	FailRenderFail       FailType = "RENDER_FAIL"
	FailDurationMismatch FailType = "DURATION_MISMATCH"
	FailMissingFile      FailType = "MISSING_FILE"
	FailMetadataInvalid  FailType = "METADATA_INVALID"
	FailRateLimited      FailType = "RATE_LIMITED"
	FailUnknown          FailType = "UNKNOWN"
)

// QACheck is a single pass/fail assertion within a QA report.
type QACheck struct {
	Name    string         `json:"name"`
	Pass    bool           `json:"pass"`
	Details string         `json:"details"`
	Diag    map[string]any `json:"diag,omitempty"` // structured diagnostics for fix engine
}

// QAReport is the structured result of a stage gate.
type QAReport struct {
	Stage     string    `json:"stage"`
	RunID     string    `json:"run_id"`
	Timestamp time.Time `json:"timestamp"`
	Pass      bool      `json:"pass"`
	FailType  FailType  `json:"fail_type,omitempty"`
	Checks    []QACheck `json:"checks"`
}

// Decision tells the autopilot loop what to do after a QA gate.
type Decision struct {
	Action       string           // "advance", "retry", "loopback", "needs_review"
	TargetStatus domain.RunStatus // for retry/loopback: which status to reset to
	Reason       string
}

// --- QA validators per stage ---

// qaScript validates the generated script against policy.
func qaScript(ctx context.Context, deps Deps, run *domain.Run, policy Policy) QAReport {
	report := QAReport{
		Stage:     "SCRIPTED",
		RunID:     run.ID,
		Timestamp: time.Now(),
		Pass:      true,
	}

	asset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetScriptMD)
	if err != nil {
		report.Pass = false
		report.FailType = FailMissingFile
		report.Checks = append(report.Checks, QACheck{Name: "script_exists", Pass: false, Details: "script.md not found"})
		return report
	}

	raw, err := os.ReadFile(asset.Path)
	if err != nil {
		report.Pass = false
		report.FailType = FailMissingFile
		report.Checks = append(report.Checks, QACheck{Name: "script_readable", Pass: false, Details: err.Error()})
		return report
	}

	text := string(raw)
	words := strings.Fields(text)
	wordCount := len(words)

	// Word count range check (direct counts from policy).
	wcDiag := map[string]any{
		"actual_words": wordCount,
		"min_words":    policy.MinWords,
		"max_words":    policy.MaxWords,
		"target_words": policy.TargetWords,
		"delta_words":  wordCount - policy.TargetWords,
	}
	if wordCount < policy.MinWords {
		report.Pass = false
		report.FailType = FailWordcountLow
		report.Checks = append(report.Checks, QACheck{
			Name: "wordcount", Pass: false,
			Details: fmt.Sprintf("word count %d < minimum %d", wordCount, policy.MinWords),
			Diag:    wcDiag,
		})
	} else if wordCount > policy.MaxWords {
		report.Pass = false
		report.FailType = FailWordcountHigh
		report.Checks = append(report.Checks, QACheck{
			Name: "wordcount", Pass: false,
			Details: fmt.Sprintf("word count %d > maximum %d", wordCount, policy.MaxWords),
			Diag:    wcDiag,
		})
	} else {
		report.Checks = append(report.Checks, QACheck{
			Name: "wordcount", Pass: true,
			Details: fmt.Sprintf("%d words (range %d–%d)", wordCount, policy.MinWords, policy.MaxWords),
			Diag:    wcDiag,
		})
	}

	// Banned phrases check (from policy).
	lowerText := strings.ToLower(text)
	for _, phrase := range policy.BannedPhrases {
		if strings.Contains(lowerText, strings.ToLower(phrase)) {
			report.Pass = false
			report.FailType = FailBannedPhrase
			report.Checks = append(report.Checks, QACheck{
				Name: "banned_phrase", Pass: false,
				Details: fmt.Sprintf("script contains banned phrase: %q", phrase),
			})
		}
	}

	// Sleep-safety QA (additional checks from llm package).
	qaResult := llm.RunQA(text)
	if !qaResult.Pass {
		report.Pass = false
		for _, f := range qaResult.Failures {
			ft := FailPacingFail
			if strings.Contains(f, "high-tension") {
				ft = FailBannedPhrase
			}
			report.FailType = ft
			report.Checks = append(report.Checks, QACheck{Name: "sleep_safety", Pass: false, Details: f})
		}
	} else {
		report.Checks = append(report.Checks, QACheck{Name: "sleep_safety", Pass: true, Details: "all checks passed"})
	}

	return report
}

// qaVoice validates the synthesised audio.
func qaVoice(ctx context.Context, deps Deps, run *domain.Run, policy Policy) QAReport {
	report := QAReport{
		Stage:     "VOICED",
		RunID:     run.ID,
		Timestamp: time.Now(),
		Pass:      true,
	}

	asset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetNarrationWAV)
	if err != nil {
		report.Pass = false
		report.FailType = FailMissingFile
		report.Checks = append(report.Checks, QACheck{Name: "audio_exists", Pass: false, Details: "narration.wav not found"})
		return report
	}

	info, err := os.Stat(asset.Path)
	if err != nil || info.Size() == 0 {
		report.Pass = false
		report.FailType = FailMissingFile
		report.Checks = append(report.Checks, QACheck{Name: "audio_exists", Pass: false, Details: "file missing or empty"})
		return report
	}
	report.Checks = append(report.Checks, QACheck{Name: "audio_exists", Pass: true, Details: fmt.Sprintf("size=%d bytes", info.Size())})

	// Duration check.
	dur, err := render.ProbeDuration(ctx, deps.Render.FFprobeBin, asset.Path)
	if err != nil {
		report.Pass = false
		report.FailType = FailUnknown
		report.Checks = append(report.Checks, QACheck{Name: "audio_probe", Pass: false, Details: err.Error()})
		return report
	}

	targetSec := float64(run.DurationMin) * 60.0
	tolerance := targetSec * policy.AudioTolerance
	diff := math.Abs(dur - targetSec)

	durDiag := map[string]any{
		"actual_sec":    dur,
		"target_sec":    targetSec,
		"delta_sec":     dur - targetSec, // negative = too short, positive = too long
		"tolerance_sec": tolerance,
	}
	if diff > tolerance {
		report.Pass = false
		report.FailType = FailAudioDurationOff
		report.Checks = append(report.Checks, QACheck{
			Name: "audio_duration", Pass: false,
			Details: fmt.Sprintf("duration %.1fs, target %.1fs ±%.0f%% (diff %.1fs > tolerance %.1fs)",
				dur, targetSec, policy.AudioTolerance*100, diff, tolerance),
			Diag: durDiag,
		})
	} else {
		report.Checks = append(report.Checks, QACheck{
			Name: "audio_duration", Pass: true,
			Details: fmt.Sprintf("duration %.1fs (target %.1fs, diff %.1fs within ±%.0f%%)", dur, targetSec, diff, policy.AudioTolerance*100),
			Diag: durDiag,
		})
	}

	return report
}

// qaThumbnail validates the generated thumbnail.
func qaThumbnail(ctx context.Context, deps Deps, run *domain.Run) QAReport {
	report := QAReport{
		Stage:     "THUMBNAILED",
		RunID:     run.ID,
		Timestamp: time.Now(),
		Pass:      true,
	}

	asset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetThumbnailPNG)
	if err != nil {
		report.Pass = false
		report.FailType = FailMissingFile
		report.Checks = append(report.Checks, QACheck{Name: "thumbnail_exists", Pass: false, Details: "thumbnail.png not found"})
		return report
	}

	info, err := os.Stat(asset.Path)
	if err != nil || info.Size() == 0 {
		report.Pass = false
		report.FailType = FailMissingFile
		report.Checks = append(report.Checks, QACheck{Name: "thumbnail_valid", Pass: false, Details: "file missing or empty"})
		return report
	}
	report.Checks = append(report.Checks, QACheck{Name: "thumbnail_valid", Pass: true, Details: fmt.Sprintf("size=%d bytes", info.Size())})

	return report
}

// qaRender validates the rendered video.
func qaRender(ctx context.Context, deps Deps, run *domain.Run, policy Policy) QAReport {
	report := QAReport{
		Stage:     "RENDERED",
		RunID:     run.ID,
		Timestamp: time.Now(),
		Pass:      true,
	}

	videoAsset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetVideoMP4)
	if err != nil {
		report.Pass = false
		report.FailType = FailMissingFile
		report.Checks = append(report.Checks, QACheck{Name: "video_exists", Pass: false, Details: "video.mp4 not found"})
		return report
	}

	info, err := os.Stat(videoAsset.Path)
	if err != nil || info.Size() == 0 {
		report.Pass = false
		report.FailType = FailMissingFile
		report.Checks = append(report.Checks, QACheck{Name: "video_valid", Pass: false, Details: "file missing or empty"})
		return report
	}
	report.Checks = append(report.Checks, QACheck{Name: "video_valid", Pass: true, Details: fmt.Sprintf("size=%d bytes", info.Size())})

	// Compare video duration to audio duration.
	videoDur, err := render.ProbeDuration(ctx, deps.Render.FFprobeBin, videoAsset.Path)
	if err != nil {
		report.Pass = false
		report.FailType = FailRenderFail
		report.Checks = append(report.Checks, QACheck{Name: "video_probe", Pass: false, Details: err.Error()})
		return report
	}

	audioAsset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetNarrationWAV)
	if err != nil {
		report.Pass = false
		report.FailType = FailMissingFile
		report.Checks = append(report.Checks, QACheck{Name: "audio_for_compare", Pass: false, Details: "narration.wav not found for comparison"})
		return report
	}

	audioDur, err := render.ProbeDuration(ctx, deps.Render.FFprobeBin, audioAsset.Path)
	if err != nil {
		report.Pass = false
		report.FailType = FailRenderFail
		report.Checks = append(report.Checks, QACheck{Name: "audio_probe_for_compare", Pass: false, Details: err.Error()})
		return report
	}

	drift := math.Abs(videoDur - audioDur)
	if drift > policy.VideoDriftSec {
		report.Pass = false
		report.FailType = FailDurationMismatch
		report.Checks = append(report.Checks, QACheck{
			Name: "duration_match", Pass: false,
			Details: fmt.Sprintf("video=%.1fs audio=%.1fs drift=%.1fs (max %.1fs)", videoDur, audioDur, drift, policy.VideoDriftSec),
		})
	} else {
		report.Checks = append(report.Checks, QACheck{
			Name: "duration_match", Pass: true,
			Details: fmt.Sprintf("video=%.1fs audio=%.1fs drift=%.1fs", videoDur, audioDur, drift),
		})
	}

	return report
}

// qaPackage validates the final package.
func qaPackage(ctx context.Context, deps Deps, run *domain.Run) QAReport {
	report := QAReport{
		Stage:     "PACKAGED",
		RunID:     run.ID,
		Timestamp: time.Now(),
		Pass:      true,
	}

	required := []struct {
		kind string
		name string
	}{
		{domain.AssetScriptMD, "script.md"},
		{domain.AssetNarrationWAV, "narration.wav"},
		{domain.AssetThumbnailPNG, "thumbnail.png"},
		{domain.AssetVideoMP4, "video.mp4"},
		{domain.AssetMetadataJSON, "metadata.json"},
		{domain.AssetEpisodePack, "episode_pack.zip"},
	}

	for _, req := range required {
		asset, err := deps.DB.GetAsset(ctx, run.ID, req.kind)
		if err != nil {
			report.Pass = false
			report.FailType = FailMissingFile
			report.Checks = append(report.Checks, QACheck{
				Name: "file_" + req.name, Pass: false, Details: req.name + " not found in DB",
			})
			continue
		}
		info, err := os.Stat(asset.Path)
		if err != nil || info.Size() == 0 {
			report.Pass = false
			report.FailType = FailMissingFile
			report.Checks = append(report.Checks, QACheck{
				Name: "file_" + req.name, Pass: false, Details: req.name + " missing or empty on disk",
			})
		} else {
			report.Checks = append(report.Checks, QACheck{
				Name: "file_" + req.name, Pass: true, Details: fmt.Sprintf("ok (%d bytes)", info.Size()),
			})
		}
	}

	return report
}

// --- Stage-aware Decision function ---

// Decide maps a (stage, QA report, run state) to an autopilot action.
// The stage parameter makes FailMissingFile route differently per stage.
func Decide(stage domain.RunStatus, report QAReport, run *domain.Run, policy Policy) Decision {
	if report.Pass {
		return Decision{Action: "advance", Reason: "QA passed"}
	}

	switch report.FailType {
	case FailBannedPhrase, FailWordcountLow, FailWordcountHigh, FailPacingFail:
		if run.ScriptAttempt < policy.MaxScriptAttempt {
			return Decision{
				Action:       "retry",
				TargetStatus: domain.StatusPending,
				Reason:       fmt.Sprintf("script QA failed (%s), retrying (attempt %d/%d)", report.FailType, run.ScriptAttempt, policy.MaxScriptAttempt),
			}
		}
		return Decision{Action: "needs_review", Reason: fmt.Sprintf("script QA failed (%s) after %d attempts", report.FailType, run.ScriptAttempt)}

	case FailAudioDurationOff, FailAudioClipping:
		if run.VoiceAttempt < policy.MaxVoiceAttempt {
			return Decision{
				Action:       "retry",
				TargetStatus: domain.StatusScripted,
				Reason:       fmt.Sprintf("voice QA failed (%s), retrying (attempt %d/%d)", report.FailType, run.VoiceAttempt, policy.MaxVoiceAttempt),
			}
		}
		return Decision{Action: "needs_review", Reason: fmt.Sprintf("voice QA failed (%s) after %d attempts", report.FailType, run.VoiceAttempt)}

	case FailRenderFail, FailDurationMismatch:
		if run.RenderAttempt < policy.MaxRenderAttempt {
			return Decision{
				Action:       "retry",
				TargetStatus: domain.StatusThumbnailed,
				Reason:       fmt.Sprintf("render QA failed (%s), retrying (attempt %d/%d)", report.FailType, run.RenderAttempt, policy.MaxRenderAttempt),
			}
		}
		return Decision{Action: "needs_review", Reason: fmt.Sprintf("render QA failed (%s) after %d attempts", report.FailType, run.RenderAttempt)}

	case FailMissingFile:
		// Stage-aware routing: missing file in different stages means different things.
		return decideMissingFile(stage, report, run, policy)

	case FailMetadataInvalid:
		if run.PackageAttempt < policy.MaxPackageAttempt {
			return Decision{Action: "retry", TargetStatus: domain.StatusRendered, Reason: "metadata invalid, retrying package"}
		}
		return Decision{Action: "needs_review", Reason: "metadata invalid after max attempts"}

	case FailRateLimited:
		return Decision{Action: "retry", TargetStatus: statusForStage(stage), Reason: "rate limited, will retry with backoff"}

	default:
		return Decision{Action: "needs_review", Reason: fmt.Sprintf("unknown fail type: %s", report.FailType)}
	}
}

// decideMissingFile routes FailMissingFile based on which stage we're in.
// A missing file at SCRIPTED means the script step failed to produce output → retry script.
// A missing file at VOICED means TTS failed → retry voice.
// A missing file at RENDERED means render failed → retry render.
// A missing file at PACKAGED means an upstream artifact vanished → needs review.
func decideMissingFile(stage domain.RunStatus, report QAReport, run *domain.Run, policy Policy) Decision {
	switch stage {
	case domain.StatusPending:
		if run.ScriptAttempt < policy.MaxScriptAttempt {
			return Decision{Action: "retry", TargetStatus: domain.StatusPending, Reason: "script output missing, retrying"}
		}
	case domain.StatusScripted:
		if run.VoiceAttempt < policy.MaxVoiceAttempt {
			return Decision{Action: "retry", TargetStatus: domain.StatusScripted, Reason: "voice output missing, retrying"}
		}
	case domain.StatusVoiced:
		// Thumbnail missing — no attempt counter, just retry once.
		return Decision{Action: "retry", TargetStatus: domain.StatusVoiced, Reason: "thumbnail output missing, retrying"}
	case domain.StatusThumbnailed:
		if run.RenderAttempt < policy.MaxRenderAttempt {
			return Decision{Action: "retry", TargetStatus: domain.StatusThumbnailed, Reason: "render output missing, retrying"}
		}
	case domain.StatusRendered:
		if run.PackageAttempt < policy.MaxPackageAttempt {
			return Decision{Action: "retry", TargetStatus: domain.StatusRendered, Reason: "package output missing, retrying"}
		}
	case domain.StatusPackaged:
		// At package QA, missing files means upstream artifacts vanished.
		return Decision{Action: "needs_review", Reason: fmt.Sprintf("missing file at final QA: %s", failDetails(report))}
	}
	return Decision{Action: "needs_review", Reason: fmt.Sprintf("missing file at %s: %s", stage, failDetails(report))}
}

// statusForStage returns the run status to reset to when retrying the given stage.
func statusForStage(stage domain.RunStatus) domain.RunStatus {
	switch stage {
	case domain.StatusPending:
		return domain.StatusPending
	case domain.StatusScripted:
		return domain.StatusScripted
	case domain.StatusVoiced:
		return domain.StatusVoiced
	case domain.StatusThumbnailed:
		return domain.StatusThumbnailed
	case domain.StatusRendered:
		return domain.StatusRendered
	case domain.StatusPackaged:
		return domain.StatusPackaged
	default:
		return domain.StatusPending
	}
}

func failDetails(report QAReport) string {
	for _, c := range report.Checks {
		if !c.Pass {
			return c.Details
		}
	}
	return "unknown"
}

// --- QA report writer ---

func writeQAReport(ctx context.Context, deps Deps, run *domain.Run, report QAReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal qa report: %w", err)
	}

	filename := fmt.Sprintf("qa_report_%s.json", strings.ToLower(report.Stage))
	path, err := deps.Store.WriteBytes(run.ID, filename, data)
	if err != nil {
		return fmt.Errorf("write qa report: %w", err)
	}

	if err := deps.DB.InsertAsset(ctx, run.ID, domain.AssetQAReport, path); err != nil {
		return fmt.Errorf("record qa report asset: %w", err)
	}

	status := "PASS"
	if !report.Pass {
		status = fmt.Sprintf("FAIL (%s)", report.FailType)
	}
	log.Printf("qa: %s stage=%s %s", run.ID, report.Stage, status)
	return nil
}

// --- Canonical input hashing ---

// canonicalInputHash computes SHA-256 of a canonical JSON representation of step inputs.
// This allows skipping re-execution when inputs haven't changed.
func canonicalInputHash(stepName string, policyVersion string, providerParams map[string]string, upstreamHashes ...string) string {
	canonical := struct {
		Step            string            `json:"step"`
		PolicyVersion   string            `json:"policy_version"`
		ProviderParams  map[string]string `json:"provider_params"`
		UpstreamHashes  []string          `json:"upstream_hashes"`
	}{
		Step:           stepName,
		PolicyVersion:  policyVersion,
		ProviderParams: providerParams,
		UpstreamHashes: upstreamHashes,
	}

	data, _ := json.Marshal(canonical)
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:])
}

// computeFileHash returns the SHA-256 hex digest of a file's contents.
func computeFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// --- Transient error detection (typed) ---

// isTransientError checks if an error is a typed TransientError using errors.As.
func isTransientError(err error) bool {
	return errs.IsTransient(err)
}

// --- Backoff helpers ---

// sleepWithBackoff waits for an exponential backoff duration with jitter.
func sleepWithBackoff(ctx context.Context, attempt int) error {
	base := time.Duration(math.Pow(2, float64(attempt))) * time.Second
	jitter := time.Duration(rand.Intn(1000)) * time.Millisecond
	wait := base + jitter
	if wait > 60*time.Second {
		wait = 60 * time.Second
	}
	log.Printf("backoff: waiting %s (attempt %d)", wait, attempt)
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
