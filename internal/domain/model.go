package domain

import "time"

// Run is a single episode generation pipeline execution.
type Run struct {
	ID          string
	Series      string
	Episode     string
	Style       string
	DurationMin int
	Status      RunStatus
	ErrorText   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
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

// Asset kind constants.
const (
	AssetScriptMD    = "script_md"
	AssetScriptSSML  = "script_ssml"
	AssetNarrationWAV = "narration_wav"
	AssetThumbnailPNG = "thumbnail_png"
	AssetVideoMP4    = "video_mp4"
	AssetMetadataJSON = "metadata_json"
	AssetEpisodePack = "episode_pack_zip"
)
