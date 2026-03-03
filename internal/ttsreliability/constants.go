package ttsreliability

// Chunk size limits (words).
const (
	DefaultMinWordsPerChunk = 500
	DefaultMaxWordsPerChunk = 900
)

// Retry / budget limits.
const (
	MaxAttemptsPerChunk = 3
	BudgetMultiplier    = 2 // max total attempts = chunk_count * BudgetMultiplier
)

// QA thresholds.
const (
	ClippingPeakDB        = -0.1  // peak at or above → clipping
	FlatFactorThreshold   = 0.0   // any flat factor > 0 → clipping
	SilenceRatioThreshold = 0.30  // >30% silence → anomaly
	MinRMSThresholdDB     = -40.0 // RMS below → too quiet
	MaxLUFSDelta          = 3.0   // max LUFS difference between chunks
	MaxSpectralDelta      = 3.0   // max spectral proxy delta between chunks
	MinSpectralProxy      = 5.0   // below → robotic/muffled
	TargetLUFS            = -16.0 // EBU R128 target
)

// Cost estimation.
const (
	CostPerCharUSD = 0.00018 // ElevenLabs approximate per-character cost
)

// Fail type constants.
const (
	FailNone              FailType = ""
	FailClipping          FailType = "CLIPPING"
	FailSilenceAnomaly    FailType = "SILENCE_ANOMALY"
	FailLowVolumeDrift    FailType = "LOW_VOLUME_DRIFT"
	FailWeirdTimbreShift  FailType = "WEIRD_TIMBRE_SHIFT"
	FailRoboticArtifact   FailType = "ROBOTIC_ARTIFACT"
	FailLoudnessNonuniform FailType = "LOUDNESS_NONUNIFORM"
	FailBudgetExhausted   FailType = "BUDGET_EXHAUSTED"
	FailProviderError     FailType = "PROVIDER_ERROR"
)
