package ttsreliability

import (
	"fmt"
	"math"
)

// Thresholds holds configurable QA thresholds. Zero values use package defaults.
type Thresholds struct {
	ClippingPeakDB    float64
	FlatFactor        float64
	SilenceRatio      float64
	MinRMSDb          float64
	MaxLUFSDelta      float64
	MaxSpectralDelta  float64
	MinSpectralProxy  float64
}

// DefaultThresholds returns production defaults.
func DefaultThresholds() Thresholds {
	return Thresholds{
		ClippingPeakDB:   ClippingPeakDB,
		FlatFactor:       FlatFactorThreshold,
		SilenceRatio:     SilenceRatioThreshold,
		MinRMSDb:         MinRMSThresholdDB,
		MaxLUFSDelta:     MaxLUFSDelta,
		MaxSpectralDelta: MaxSpectralDelta,
		MinSpectralProxy: MinSpectralProxy,
	}
}

// QAChunk validates a single chunk's audio metrics against thresholds.
func QAChunk(metrics QAMetrics, th Thresholds) QAResult {
	// Clipping: flat factor or peak at digital max.
	if metrics.FlatFactor > th.FlatFactor || metrics.PeakDB >= th.ClippingPeakDB {
		return QAResult{
			Pass:     false,
			FailType: FailClipping,
			Details:  fmt.Sprintf("clipping: flat=%.3f peak=%.1fdB", metrics.FlatFactor, metrics.PeakDB),
		}
	}

	// Silence anomaly: too much silence relative to duration.
	if metrics.DurationSec > 0 {
		silenceRatio := metrics.SilenceSec / metrics.DurationSec
		if silenceRatio > th.SilenceRatio {
			return QAResult{
				Pass:     false,
				FailType: FailSilenceAnomaly,
				Details:  fmt.Sprintf("silence %.0f%% > %.0f%%", silenceRatio*100, th.SilenceRatio*100),
			}
		}
	}

	// Low volume: RMS too quiet.
	if metrics.RMSDb < th.MinRMSDb {
		return QAResult{
			Pass:     false,
			FailType: FailLowVolumeDrift,
			Details:  fmt.Sprintf("RMS %.1fdB < %.1fdB", metrics.RMSDb, th.MinRMSDb),
		}
	}

	// Robotic artifact: narrow spectral spread (muffled/mechanical).
	if metrics.SpectralProxy < th.MinSpectralProxy && metrics.SpectralProxy > 0 {
		return QAResult{
			Pass:     false,
			FailType: FailRoboticArtifact,
			Details:  fmt.Sprintf("spectral proxy %.1fdB < %.1fdB (narrow frequency range)", metrics.SpectralProxy, th.MinSpectralProxy),
		}
	}

	return QAResult{Pass: true}
}

// QAFinal validates cross-chunk uniformity after all chunks pass individual QA.
func QAFinal(results []ChunkResult, th Thresholds) QAResult {
	if len(results) < 2 {
		return QAResult{Pass: true}
	}

	// Check LUFS uniformity.
	var minLUFS, maxLUFS float64 = math.MaxFloat64, -math.MaxFloat64
	var minSpectral, maxSpectral float64 = math.MaxFloat64, -math.MaxFloat64

	for _, r := range results {
		if r.Metrics.LUFS < minLUFS {
			minLUFS = r.Metrics.LUFS
		}
		if r.Metrics.LUFS > maxLUFS {
			maxLUFS = r.Metrics.LUFS
		}
		if r.Metrics.SpectralProxy < minSpectral {
			minSpectral = r.Metrics.SpectralProxy
		}
		if r.Metrics.SpectralProxy > maxSpectral {
			maxSpectral = r.Metrics.SpectralProxy
		}
	}

	lufsSpread := maxLUFS - minLUFS
	if lufsSpread > th.MaxLUFSDelta {
		return QAResult{
			Pass:     false,
			FailType: FailLoudnessNonuniform,
			Details:  fmt.Sprintf("LUFS spread %.1fdB > %.1fdB (min=%.1f max=%.1f)", lufsSpread, th.MaxLUFSDelta, minLUFS, maxLUFS),
		}
	}

	spectralSpread := maxSpectral - minSpectral
	if spectralSpread > th.MaxSpectralDelta {
		return QAResult{
			Pass:     false,
			FailType: FailWeirdTimbreShift,
			Details:  fmt.Sprintf("spectral spread %.1fdB > %.1fdB (min=%.1f max=%.1f)", spectralSpread, th.MaxSpectralDelta, minSpectral, maxSpectral),
		}
	}

	return QAResult{Pass: true}
}
