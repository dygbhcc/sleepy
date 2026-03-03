package ttsreliability

import "testing"

func TestQAChunk_Pass(t *testing.T) {
	th := DefaultThresholds()
	m := QAMetrics{
		PeakDB:        -3.0,
		RMSDb:         -18.0,
		FlatFactor:    0.0,
		SilenceSec:    5.0,
		SpectralProxy: 15.0,
		DurationSec:   120.0,
	}
	r := QAChunk(m, th)
	if !r.Pass {
		t.Errorf("expected pass, got fail: %s (%s)", r.FailType, r.Details)
	}
}

func TestQAChunk_Clipping_FlatFactor(t *testing.T) {
	th := DefaultThresholds()
	m := QAMetrics{
		PeakDB:        -3.0,
		RMSDb:         -18.0,
		FlatFactor:    0.5,
		DurationSec:   120.0,
		SpectralProxy: 15.0,
	}
	r := QAChunk(m, th)
	if r.Pass || r.FailType != FailClipping {
		t.Errorf("expected CLIPPING, got pass=%v type=%s", r.Pass, r.FailType)
	}
}

func TestQAChunk_Clipping_PeakDB(t *testing.T) {
	th := DefaultThresholds()
	m := QAMetrics{
		PeakDB:        0.0,
		RMSDb:         -18.0,
		FlatFactor:    0.0,
		DurationSec:   120.0,
		SpectralProxy: 18.0,
	}
	r := QAChunk(m, th)
	if r.Pass || r.FailType != FailClipping {
		t.Errorf("expected CLIPPING, got pass=%v type=%s", r.Pass, r.FailType)
	}
}

func TestQAChunk_SilenceAnomaly(t *testing.T) {
	th := DefaultThresholds()
	m := QAMetrics{
		PeakDB:        -3.0,
		RMSDb:         -18.0,
		SilenceSec:    50.0,
		DurationSec:   100.0, // 50% silence
		SpectralProxy: 15.0,
	}
	r := QAChunk(m, th)
	if r.Pass || r.FailType != FailSilenceAnomaly {
		t.Errorf("expected SILENCE_ANOMALY, got pass=%v type=%s", r.Pass, r.FailType)
	}
}

func TestQAChunk_LowVolume(t *testing.T) {
	th := DefaultThresholds()
	m := QAMetrics{
		PeakDB:        -30.0,
		RMSDb:         -45.0,
		DurationSec:   120.0,
		SpectralProxy: 15.0,
	}
	r := QAChunk(m, th)
	if r.Pass || r.FailType != FailLowVolumeDrift {
		t.Errorf("expected LOW_VOLUME_DRIFT, got pass=%v type=%s", r.Pass, r.FailType)
	}
}

func TestQAChunk_RoboticArtifact(t *testing.T) {
	th := DefaultThresholds()
	m := QAMetrics{
		PeakDB:        -3.0,
		RMSDb:         -5.0,
		DurationSec:   120.0,
		SpectralProxy: 2.0, // very narrow spectral spread
	}
	r := QAChunk(m, th)
	if r.Pass || r.FailType != FailRoboticArtifact {
		t.Errorf("expected ROBOTIC_ARTIFACT, got pass=%v type=%s", r.Pass, r.FailType)
	}
}

func TestQAChunk_BoundaryValues(t *testing.T) {
	th := DefaultThresholds()
	// Exactly at threshold should pass (threshold is exclusive).
	m := QAMetrics{
		PeakDB:        -0.2,  // below -0.1 threshold
		RMSDb:         -18.0,
		FlatFactor:    0.0,
		SilenceSec:    30.0,
		DurationSec:   100.0, // exactly 30%
		SpectralProxy: 15.0,
	}
	r := QAChunk(m, th)
	if !r.Pass {
		t.Errorf("expected pass at boundary, got fail: %s", r.FailType)
	}
}

func TestQAFinal_Pass(t *testing.T) {
	th := DefaultThresholds()
	results := []ChunkResult{
		{Metrics: QAMetrics{LUFS: -16.0, SpectralProxy: 14.0}},
		{Metrics: QAMetrics{LUFS: -17.5, SpectralProxy: 13.5}},
	}
	r := QAFinal(results, th)
	if !r.Pass {
		t.Errorf("expected pass, got fail: %s", r.FailType)
	}
}

func TestQAFinal_LoudnessNonuniform(t *testing.T) {
	th := DefaultThresholds()
	results := []ChunkResult{
		{Metrics: QAMetrics{LUFS: -12.0, SpectralProxy: 14.0}},
		{Metrics: QAMetrics{LUFS: -20.0, SpectralProxy: 14.0}},
	}
	r := QAFinal(results, th)
	if r.Pass || r.FailType != FailLoudnessNonuniform {
		t.Errorf("expected LOUDNESS_NONUNIFORM, got pass=%v type=%s", r.Pass, r.FailType)
	}
}

func TestQAFinal_TimbreShift(t *testing.T) {
	th := DefaultThresholds()
	results := []ChunkResult{
		{Metrics: QAMetrics{LUFS: -16.0, SpectralProxy: 5.0}},
		{Metrics: QAMetrics{LUFS: -16.5, SpectralProxy: 12.0}},
	}
	r := QAFinal(results, th)
	if r.Pass || r.FailType != FailWeirdTimbreShift {
		t.Errorf("expected WEIRD_TIMBRE_SHIFT, got pass=%v type=%s", r.Pass, r.FailType)
	}
}

func TestQAFinal_SingleChunk(t *testing.T) {
	th := DefaultThresholds()
	results := []ChunkResult{
		{Metrics: QAMetrics{LUFS: -16.0, SpectralProxy: 14.0}},
	}
	r := QAFinal(results, th)
	if !r.Pass {
		t.Errorf("single chunk should always pass final QA, got: %s", r.FailType)
	}
}
