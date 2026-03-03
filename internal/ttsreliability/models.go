package ttsreliability

import "context"

// TTSJob describes a complete TTS synthesis request.
type TTSJob struct {
	RunID      string
	Text       string
	Settings   TTSSettings
	OutputPath string // final WAV destination
	FFmpegBin  string
}

// TTSSettings holds per-request TTS configuration.
type TTSSettings struct {
	Speed           float64 // 0 = provider default
	Stability       float64 // 0 = provider default
	SimilarityBoost float64 // 0 = provider default
	ModelID         string  // empty = provider default
}

// TTSChunk is a segment of text to be synthesized independently.
type TTSChunk struct {
	Index     int
	Text      string
	WordCount int
}

// ChunkResult captures the outcome of synthesizing one chunk.
type ChunkResult struct {
	Index     int
	AudioPath string
	Metrics   QAMetrics
	Pass      bool
	FailType  FailType
	Attempt   int
}

// QAMetrics holds audio quality measurements for a single chunk.
type QAMetrics struct {
	LUFS          float64 // integrated loudness (EBU R128)
	PeakDB        float64 // peak sample level in dB
	RMSDb         float64 // RMS level in dB
	FlatFactor    float64 // >0 indicates clipped/flat samples
	SilenceSec    float64 // total seconds of detected silence
	SpectralProxy float64 // PeakDB - RMSDb (frequency spread indicator)
	DurationSec   float64
}

// QAResult is the pass/fail outcome of a QA check.
type QAResult struct {
	Pass     bool
	FailType FailType
	Details  string
}

// TTSResult is the final outcome of orchestrator.Run().
type TTSResult struct {
	FinalAudioPath string
	Success        bool
	FailType       FailType
	Details        string
	TotalAttempts  int
	TotalCostUSD   float64
	Chunks         []ChunkResult
}

// FixAction describes what to do after a chunk QA failure.
type FixAction struct {
	Action      string // "retry", "retry_smaller", "give_up"
	PostProcess string // "loudnorm", "" (empty = none)
	NewSettings TTSSettings
	Reason      string
}

// TTSProvider abstracts TTS synthesis for testability.
type TTSProvider interface {
	SynthesizeChunk(ctx context.Context, text string, settings TTSSettings, outPath string) error
}

// FailType categorizes QA failures.
type FailType string
