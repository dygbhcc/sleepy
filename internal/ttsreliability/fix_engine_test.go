package ttsreliability

import (
	"math"
	"testing"
)

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 0.001
}

func TestDecideFix_LowVolumeDrift(t *testing.T) {
	s := TTSSettings{Speed: 0.80}

	// Attempt 1: loudnorm post-process.
	fix := DecideFix(FailLowVolumeDrift, 1, s)
	if fix.Action != "retry" || fix.PostProcess != "loudnorm" {
		t.Errorf("attempt 1: got action=%s postprocess=%s", fix.Action, fix.PostProcess)
	}

	// Attempt 2: retry smaller + loudnorm.
	fix = DecideFix(FailLowVolumeDrift, 2, s)
	if fix.Action != "retry_smaller" || fix.PostProcess != "loudnorm" {
		t.Errorf("attempt 2: got action=%s postprocess=%s", fix.Action, fix.PostProcess)
	}

	// Attempt 3: give up.
	fix = DecideFix(FailLowVolumeDrift, 3, s)
	if fix.Action != "give_up" {
		t.Errorf("attempt 3: expected give_up, got %s", fix.Action)
	}
}

func TestDecideFix_Clipping(t *testing.T) {
	s := TTSSettings{Stability: 0.80}

	// Attempt 1: reduce stability.
	fix := DecideFix(FailClipping, 1, s)
	if fix.Action != "retry" {
		t.Errorf("attempt 1: expected retry, got %s", fix.Action)
	}
	if !approxEqual(fix.NewSettings.Stability, 0.70) {
		t.Errorf("attempt 1: expected stability 0.70, got %.2f", fix.NewSettings.Stability)
	}

	// Attempt 2: loudnorm.
	fix = DecideFix(FailClipping, 2, s)
	if fix.PostProcess != "loudnorm" {
		t.Errorf("attempt 2: expected loudnorm, got %s", fix.PostProcess)
	}

	// Attempt 3: give up.
	fix = DecideFix(FailClipping, 3, s)
	if fix.Action != "give_up" {
		t.Errorf("attempt 3: expected give_up, got %s", fix.Action)
	}
}

func TestDecideFix_RoboticArtifact(t *testing.T) {
	s := TTSSettings{Stability: 0.80, ModelID: "eleven_monolingual_v1"}

	// Attempt 1: reset defaults.
	fix := DecideFix(FailRoboticArtifact, 1, s)
	if fix.Action != "retry" {
		t.Errorf("attempt 1: expected retry, got %s", fix.Action)
	}
	if fix.NewSettings.Stability != 0 {
		t.Errorf("attempt 1: expected reset stability to 0, got %.2f", fix.NewSettings.Stability)
	}

	// Attempt 2: retry smaller.
	fix = DecideFix(FailRoboticArtifact, 2, s)
	if fix.Action != "retry_smaller" {
		t.Errorf("attempt 2: expected retry_smaller, got %s", fix.Action)
	}

	// Attempt 3: switch model.
	fix = DecideFix(FailRoboticArtifact, 3, s)
	if fix.NewSettings.ModelID == s.ModelID {
		t.Errorf("attempt 3: expected model switch from %s", s.ModelID)
	}
}

func TestDecideFix_WeirdTimbreShift(t *testing.T) {
	s := TTSSettings{SimilarityBoost: 0.75}

	// Attempt 1: increase similarity boost.
	fix := DecideFix(FailWeirdTimbreShift, 1, s)
	if !approxEqual(fix.NewSettings.SimilarityBoost, 0.85) {
		t.Errorf("attempt 1: expected similarity 0.85, got %.2f", fix.NewSettings.SimilarityBoost)
	}

	// Attempt 2: switch model.
	fix = DecideFix(FailWeirdTimbreShift, 2, s)
	if fix.NewSettings.ModelID == "" {
		t.Errorf("attempt 2: expected model switch")
	}

	// Attempt 3: give up.
	fix = DecideFix(FailWeirdTimbreShift, 3, s)
	if fix.Action != "give_up" {
		t.Errorf("attempt 3: expected give_up, got %s", fix.Action)
	}
}

func TestDecideFix_SilenceAnomaly(t *testing.T) {
	s := TTSSettings{}

	fix := DecideFix(FailSilenceAnomaly, 1, s)
	if fix.Action != "retry_smaller" {
		t.Errorf("attempt 1: expected retry_smaller, got %s", fix.Action)
	}

	fix = DecideFix(FailSilenceAnomaly, 2, s)
	if fix.Action != "retry" {
		t.Errorf("attempt 2: expected retry, got %s", fix.Action)
	}

	fix = DecideFix(FailSilenceAnomaly, 3, s)
	if fix.Action != "give_up" {
		t.Errorf("attempt 3: expected give_up, got %s", fix.Action)
	}
}

func TestDecideFix_DefaultStability(t *testing.T) {
	// When stability is zero (provider default), clipping fix should set it to 0.70.
	s := TTSSettings{}
	fix := DecideFix(FailClipping, 1, s)
	if !approxEqual(fix.NewSettings.Stability, 0.70) {
		t.Errorf("expected stability 0.70, got %.2f", fix.NewSettings.Stability)
	}
}

func TestSwitchModel(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"", "eleven_multilingual_v2"},
		{"eleven_monolingual_v1", "eleven_multilingual_v2"},
		{"eleven_multilingual_v2", "eleven_turbo_v2_5"},
		{"eleven_turbo_v2_5", "eleven_multilingual_v2"},
	}
	for _, tt := range tests {
		got := switchModel(tt.input)
		if got != tt.want {
			t.Errorf("switchModel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
