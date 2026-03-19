package domain

import "time"

// Run is a single episode generation pipeline execution.
type Run struct {
	ID          string
	Series      string
	Episode     string
	Style       string
	Language    string
	DurationMin int
	Status      RunStatus
	ErrorText   string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// Autopilot fields
	ScriptAttempt  int
	VoiceAttempt   int
	RenderAttempt  int
	PackageAttempt int
	YouTubeAttempt int
	LastError      string
	NeedsReview    bool
	ScriptHash     string
	VoiceHash      string
	RenderHash     string

	// Run locking (TTL-based)
	LockedBy *string
	LockedAt *time.Time

	// Voice approval gate
	VoiceApproved bool

	// Dynamic title (AI-generated unless user-provided)
	Title       string
	TitleLocked bool // true = user-provided title, never overwrite

	// YouTube upload
	YouTubeVideoID string

	// Instagram upload
	ContentType      string // "sleep_narration" or "lifestyle_reel"
	DurationSec      int    // precise duration in seconds (for Reels)
	InstagramMediaID string

	// Fix engine fields
	ActiveFixPlanID      string
	ActiveFixStartAttempt int
	PolicyOverridesJSON  string
}

// Asset is a file produced by a pipeline step.
type Asset struct {
	ID        string
	RunID     string
	Kind      string
	Path      string
	CreatedAt time.Time
}

// Job is a queued unit of work.
type Job struct {
	ID         string
	RunID      string
	JobType    string
	Status     JobStatus
	ErrorText  string
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// WorkerSettings holds the singleton worker configuration.
type WorkerSettings struct {
	Mode              string

	// Throughput / safety knobs (HT-01).
	WorkerMode           string // "SAFE" (default) or "THROUGHPUT"
	RequireVoiceApproval bool   // true = gate TTS behind approval (default true)
	MaxInflightScript    int    // max concurrent script workers (default 1)
	MaxInflightTTS       int    // max concurrent TTS workers (default 1)
	MaxInflightRender    int    // max concurrent render workers (default 1)

	OpenAIAPIKey      string
	OpenAIBaseURL     string
	OpenAIModel       string
	ElevenLabsAPIKey  string
	ElevenLabsVoiceID string
	ElevenLabsModelID string
	ElevenLabsSpeed   float64
	EdgeVoice         string
	EdgeRate          string
	Normalize         bool
	MusicPath         string // path to ambient music file (empty = no music)

	// YouTube upload
	YouTubeEnabled      bool
	YouTubePrivacy      string // "unlisted", "public", "private"
	YouTubeClientID     string
	YouTubeClientSecret string
	YouTubeAccessToken  string
	YouTubeRefreshToken string
	YouTubeTokenExpiry  time.Time

	// Instagram
	InstagramEnabled     bool
	InstagramAccessToken string
	InstagramUserID      string
	PexelsAPIKey         string

	UpdatedAt         time.Time
}

// Asset kind constants.
const (
	AssetScriptMD     = "script_md"
	AssetScriptSSML   = "script_ssml"
	AssetNarrationWAV = "narration_wav"
	AssetThumbnailPNG = "thumbnail_png"
	AssetVideoMP4     = "video_mp4"
	AssetMetadataJSON = "metadata_json"
	AssetEpisodePack  = "episode_pack_zip"
	AssetQAReport     = "qa_report"

	// Reels-specific
	AssetReelsMP4    = "reels_mp4"
	AssetCaptionsSRT = "captions_srt"
	AssetBRollJSON   = "broll_json"
)

// Content type constants.
const (
	ContentSleepNarration = "sleep_narration"
	ContentLifestyleReel  = "lifestyle_reel"
)
