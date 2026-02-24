package jobs

// Policy defines the QA thresholds and retry limits for autopilot.
type Policy struct {
	// Script word counts (absolute, not per-minute).
	MinWords   int // minimum acceptable word count
	MaxWords   int // maximum acceptable word count
	TargetWords int // ideal target (used for prompt generation)

	// Banned phrases — script QA will reject any match.
	BannedPhrases []string

	// Retry limits per stage.
	MaxScriptAttempt  int
	MaxVoiceAttempt   int
	MaxRenderAttempt  int
	MaxPackageAttempt int

	// Audio/video tolerances.
	AudioTolerance float64 // fraction: acceptable deviation from target audio duration (0.15 = 15%)
	VideoDriftSec  float64 // max seconds video duration can differ from audio duration

	// Override-carrier fields (set by fix engine, zero = use provider defaults).
	LLMTemperature      float64 // 0 = provider default
	LLMExtraInstruction string  // appended to system prompt
	TTSSpeedFactor      float64 // 0 = no change; <1 slower, >1 faster
	EdgeRateDelta       int     // delta added to edge-tts rate, e.g. -15 → slower
	TTSStability        float64 // 0 = provider default
	TTSSimilarityBoost  float64 // 0 = provider default

	// Version tag for canonical hashing (bump when policy changes).
	PolicyVersion string
}

// DefaultPolicy returns production-grade defaults for a ~5 minute episode.
func DefaultPolicy() Policy {
	return Policy{
		MinWords:          500,
		MaxWords:          900,
		TargetWords:       650,
		BannedPhrases:     defaultBannedPhrases,
		MaxScriptAttempt:  6,
		MaxVoiceAttempt:   6,
		MaxRenderAttempt:  4,
		MaxPackageAttempt: 3,
		AudioTolerance:    0.50,
		VideoDriftSec:     5.0,
		PolicyVersion:     "v1",
	}
}

// PolicyForDuration returns a policy scaled to the requested duration in minutes.
func PolicyForDuration(durationMin int) Policy {
	p := DefaultPolicy()
	p.MinWords = durationMin * 100
	p.MaxWords = durationMin * 160
	p.TargetWords = durationMin * 130
	return p
}

var defaultBannedPhrases = []string{
	"suddenly",
	"blood",
	"scream",
	"terror",
	"panic",
	"kill",
	"dead",
	"gun",
	"fight",
	"attack",
	"explosion",
	"horror",
	"nightmare",
	"violent",
	"murder",
	"death",
	"war",
	"battle",
	"weapon",
	"destroy",
}
