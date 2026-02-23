package jobs

import (
	"fmt"
	"log"
	"math"

	"sleepy/internal/domain"
)

// FixPlan describes a single corrective action the engine will attempt.
type FixPlan struct {
	ID           string             // catalog key, e.g. "script_wc_low_computed"
	Stage        domain.RunStatus   // stage where the failure was detected
	FailType     FailType           // which QA failure this addresses
	Action       string             // "retry" | "loopback" | "needs_review" | "advance"
	TargetStatus domain.RunStatus   // status to reset to for retry/loopback
	Overrides    map[string]any     // consumed by applyOverrides
}

// FixEngine selects the best fix for a QA failure.
type FixEngine struct {
	catalog *FixCatalog
	scorer  *FixScorer
}

// NewFixEngine creates a FixEngine with the default catalog and scorer.
func NewFixEngine(outcomes []FixOutcome) *FixEngine {
	return &FixEngine{
		catalog: DefaultCatalog(),
		scorer:  NewFixScorer(outcomes),
	}
}

// DecideFix selects the best fix plan given the current QA failure context.
func (fe *FixEngine) DecideFix(
	stage domain.RunStatus,
	report QAReport,
	run *domain.Run,
	policy Policy,
) FixPlan {
	if report.Pass {
		return FixPlan{Action: "advance", Stage: stage}
	}

	// Safety: global attempt ceiling.
	totalAttempts := run.ScriptAttempt + run.VoiceAttempt + run.RenderAttempt + run.PackageAttempt
	if totalAttempts >= 10 {
		return FixPlan{
			ID: "global_max_attempts", Action: "needs_review",
			Stage: stage, FailType: report.FailType,
		}
	}

	// Per-stage exhaustion check.
	if isStageExhausted(stage, report.FailType, run, policy) {
		return FixPlan{
			ID: "stage_max_attempts", Action: "needs_review",
			Stage: stage, FailType: report.FailType,
		}
	}

	// Get candidates from catalog.
	candidates := fe.catalog.CandidatesFor(stage, report.FailType)
	if len(candidates) == 0 {
		return fe.fallbackRetry(stage, report.FailType)
	}

	// Direction-aware filtering for AUDIO_DURATION_OFF.
	if report.FailType == FailAudioDurationOff {
		candidates = filterByDirection(candidates, report)
	}

	// Score and pick.
	best := fe.scorer.Best(candidates)

	// Validate target.
	if best.Action == "retry" || best.Action == "loopback" {
		if !isValidTarget(stage, best.TargetStatus) {
			log.Printf("fix: invalid target %s for stage %s, falling back", best.TargetStatus, stage)
			return fe.fallbackRetry(stage, report.FailType)
		}
	}

	// Compute explicit TargetWords if requested.
	if mode, ok := best.Overrides["target_words_mode"].(string); ok {
		tw := computeTargetWords(mode, report, policy)
		if tw > 0 {
			best.Overrides["llm_target_words"] = tw
		}
	}

	return best
}

// RecordOutcome updates the in-memory scorer.
func (fe *FixEngine) RecordOutcome(outcome FixOutcome) {
	fe.scorer.Record(outcome)
}

// --- Safety helpers ---

func isStageExhausted(stage domain.RunStatus, ft FailType, run *domain.Run, policy Policy) bool {
	switch {
	case ft == FailBannedPhrase || ft == FailWordcountLow || ft == FailWordcountHigh || ft == FailPacingFail:
		return run.ScriptAttempt >= policy.MaxScriptAttempt
	case ft == FailAudioDurationOff || ft == FailAudioClipping:
		return run.VoiceAttempt >= policy.MaxVoiceAttempt
	case ft == FailRenderFail || ft == FailDurationMismatch:
		return run.RenderAttempt >= policy.MaxRenderAttempt
	case ft == FailMetadataInvalid:
		return run.PackageAttempt >= policy.MaxPackageAttempt
	case ft == FailMissingFile:
		return isMissingFileExhausted(stage, run, policy)
	default:
		return true
	}
}

func isMissingFileExhausted(stage domain.RunStatus, run *domain.Run, policy Policy) bool {
	switch stage {
	case domain.StatusPending:
		return run.ScriptAttempt >= policy.MaxScriptAttempt
	case domain.StatusScripted:
		return run.VoiceAttempt >= policy.MaxVoiceAttempt
	case domain.StatusThumbnailed:
		return run.RenderAttempt >= policy.MaxRenderAttempt
	case domain.StatusRendered:
		return run.PackageAttempt >= policy.MaxPackageAttempt
	default:
		return true
	}
}

func isValidTarget(currentStage, targetStatus domain.RunStatus) bool {
	order := map[domain.RunStatus]int{
		domain.StatusPending:     0,
		domain.StatusScripted:    1,
		domain.StatusVoiced:      2,
		domain.StatusThumbnailed: 3,
		domain.StatusRendered:    4,
		domain.StatusPackaged:    5,
	}
	cur, ok1 := order[currentStage]
	tgt, ok2 := order[targetStatus]
	if !ok1 || !ok2 {
		return false
	}
	return tgt <= cur
}

func (fe *FixEngine) fallbackRetry(stage domain.RunStatus, ft FailType) FixPlan {
	return FixPlan{
		ID:           fmt.Sprintf("fallback_%s_%s", stage, ft),
		Stage:        stage,
		FailType:     ft,
		Action:       "retry",
		TargetStatus: statusForStage(stage),
	}
}

// --- Direction-aware filtering ---

func filterByDirection(candidates []FixPlan, report QAReport) []FixPlan {
	diag := extractDiag(report, "audio_duration")
	if diag == nil {
		return candidates
	}
	deltaSec := floatFromDiag(diag, "delta_sec")

	var filtered []FixPlan
	for _, c := range candidates {
		dir, _ := c.Overrides["direction"].(string)
		switch {
		case deltaSec < 0 && (dir == "short" || dir == "either" || dir == ""):
			filtered = append(filtered, c)
		case deltaSec >= 0 && (dir == "long" || dir == "either" || dir == ""):
			filtered = append(filtered, c)
		}
	}
	if len(filtered) == 0 {
		return candidates
	}
	return filtered
}

// --- TargetWords computation ---

func computeTargetWords(mode string, report QAReport, policy Policy) int {
	switch mode {
	case "computed":
		return computeTargetWordsFromWC(report, policy)
	case "duration_derived":
		return computeTargetWordsFromDuration(report, policy)
	default:
		return 0
	}
}

// computeTargetWordsFromWC uses wordcount diagnostics to compute an adjusted target.
func computeTargetWordsFromWC(report QAReport, policy Policy) int {
	diag := extractDiag(report, "wordcount")
	if diag == nil {
		return 0
	}
	actual := intFromDiag(diag, "actual_words")
	target := intFromDiag(diag, "target_words")
	if actual == 0 || target == 0 {
		return 0
	}

	gap := target - actual // positive = too few words
	buffer := int(math.Abs(float64(gap)) * 0.10)

	adjusted := target + sign(gap)*buffer
	return clamp(adjusted, policy.MinWords, policy.MaxWords)
}

// computeTargetWordsFromDuration derives a word target from audio duration diagnostics.
// target_words = actual_words * (target_sec / actual_sec)
func computeTargetWordsFromDuration(report QAReport, policy Policy) int {
	durDiag := extractDiag(report, "audio_duration")
	wcDiag := extractDiag(report, "wordcount")
	if durDiag == nil {
		return 0
	}

	actualSec := floatFromDiag(durDiag, "actual_sec")
	targetSec := floatFromDiag(durDiag, "target_sec")
	if actualSec <= 0 || targetSec <= 0 {
		return 0
	}

	// Get actual word count from wordcount diag or estimate from policy.
	actualWords := policy.TargetWords
	if wcDiag != nil {
		if aw := intFromDiag(wcDiag, "actual_words"); aw > 0 {
			actualWords = aw
		}
	}

	ratio := targetSec / actualSec
	adjusted := int(float64(actualWords) * ratio)
	return clamp(adjusted, policy.MinWords, policy.MaxWords*2) // allow wider range for duration fixes
}

// --- Diagnostic extraction helpers ---

func extractDiag(report QAReport, checkName string) map[string]any {
	for _, c := range report.Checks {
		if c.Name == checkName && c.Diag != nil {
			return c.Diag
		}
	}
	return nil
}

func intFromDiag(diag map[string]any, key string) int {
	v, ok := diag[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

func floatFromDiag(diag map[string]any, key string) float64 {
	v, ok := diag[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	default:
		return 0
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func sign(v int) int {
	if v > 0 {
		return 1
	}
	if v < 0 {
		return -1
	}
	return 0
}
