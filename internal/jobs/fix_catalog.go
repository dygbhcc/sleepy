package jobs

import "sleepy/internal/domain"

// FixCatalog holds all known fix strategies, keyed by (stage, failType).
type FixCatalog struct {
	entries map[catalogKey][]FixPlan
}

type catalogKey struct {
	Stage    domain.RunStatus
	FailType FailType
}

// DefaultCatalog returns the production fix catalog.
func DefaultCatalog() *FixCatalog {
	c := &FixCatalog{entries: make(map[catalogKey][]FixPlan)}
	c.register(scriptFixes()...)
	c.register(voiceFixes()...)
	c.register(renderFixes()...)
	return c
}

func (c *FixCatalog) register(plans ...FixPlan) {
	for _, p := range plans {
		key := catalogKey{Stage: p.Stage, FailType: p.FailType}
		c.entries[key] = append(c.entries[key], p)
	}
}

// CandidatesFor returns all fix plans matching the given stage and fail type.
func (c *FixCatalog) CandidatesFor(stage domain.RunStatus, ft FailType) []FixPlan {
	return c.entries[catalogKey{Stage: stage, FailType: ft}]
}

// --- Script fixes (detected at PENDING stage, QA runs after stepScript) ---

func scriptFixes() []FixPlan {
	return []FixPlan{
		// WORDCOUNT_LOW: compute adjusted TargetWords from diagnostics.
		{
			ID: "script_wc_low_computed", Stage: domain.StatusPending,
			FailType: FailWordcountLow, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"target_words_mode":   "computed",
				"target_words_buffer": 0.10,
				"llm_temperature":     0.5,
			},
		},
		// WORDCOUNT_LOW (aggressive): larger buffer.
		{
			ID: "script_wc_low_aggressive", Stage: domain.StatusPending,
			FailType: FailWordcountLow, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"target_words_mode":   "computed",
				"target_words_buffer": 0.20,
				"llm_temperature":     0.4,
			},
		},
		// WORDCOUNT_LOW (factor fallback): when no diag data.
		{
			ID: "script_wc_low_factor", Stage: domain.StatusPending,
			FailType: FailWordcountLow, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"llm_word_count_factor": 1.20,
				"llm_temperature":       0.5,
			},
		},
		// WORDCOUNT_HIGH: compute adjusted TargetWords.
		{
			ID: "script_wc_high_computed", Stage: domain.StatusPending,
			FailType: FailWordcountHigh, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"target_words_mode":   "computed",
				"target_words_buffer": 0.10,
				"llm_temperature":     0.5,
			},
		},
		// WORDCOUNT_HIGH (aggressive).
		{
			ID: "script_wc_high_aggressive", Stage: domain.StatusPending,
			FailType: FailWordcountHigh, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"target_words_mode":   "computed",
				"target_words_buffer": 0.20,
				"llm_temperature":     0.3,
			},
		},
		// BANNED_PHRASE: lower temperature, extra safety instruction.
		{
			ID: "script_banned_conservative", Stage: domain.StatusPending,
			FailType: FailBannedPhrase, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"llm_temperature":       0.3,
				"llm_extra_instruction": "CRITICAL: Absolutely avoid ALL banned and high-tension words. Use only the gentlest, most calming vocabulary.",
			},
		},
		// BANNED_PHRASE: ultra-safe.
		{
			ID: "script_banned_ultra_safe", Stage: domain.StatusPending,
			FailType: FailBannedPhrase, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"llm_temperature":       0.2,
				"llm_extra_instruction": "ABSOLUTE RULE: Do not use any word associated with tension, conflict, danger, or excitement. Every word must be maximally soft and peaceful.",
			},
		},
		// PACING_FAIL: slow down.
		{
			ID: "script_pacing_slow", Stage: domain.StatusPending,
			FailType: FailPacingFail, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"llm_temperature":       0.4,
				"llm_extra_instruction": "Slow the pacing further. Use longer sentences with more pauses. Every paragraph should feel like drifting into sleep.",
			},
		},
	}
}

// --- Voice fixes (detected at SCRIPTED stage, QA runs after stepTTS) ---

func voiceFixes() []FixPlan {
	return []FixPlan{
		// Audio too short: slow down TTS.
		{
			ID: "voice_slow_down", Stage: domain.StatusScripted,
			FailType: FailAudioDurationOff, Action: "retry",
			TargetStatus: domain.StatusScripted,
			Overrides: map[string]any{
				"direction":        "short",
				"tts_speed_factor": 0.85,
				"edge_rate_delta":  -15,
			},
		},
		// Audio too short: loopback to rescript with more words.
		{
			ID: "voice_rescript_more", Stage: domain.StatusScripted,
			FailType: FailAudioDurationOff, Action: "loopback",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"direction":         "short",
				"target_words_mode": "duration_derived",
			},
		},
		// Audio too long: speed up TTS.
		{
			ID: "voice_speed_up", Stage: domain.StatusScripted,
			FailType: FailAudioDurationOff, Action: "retry",
			TargetStatus: domain.StatusScripted,
			Overrides: map[string]any{
				"direction":        "long",
				"tts_speed_factor": 1.15,
				"edge_rate_delta":  15,
			},
		},
		// Audio too long: loopback to rescript with fewer words.
		{
			ID: "voice_rescript_less", Stage: domain.StatusScripted,
			FailType: FailAudioDurationOff, Action: "loopback",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"direction":         "long",
				"target_words_mode": "duration_derived",
			},
		},
		// Audio clipping: increase stability.
		{
			ID: "voice_clipping_stability", Stage: domain.StatusScripted,
			FailType: FailAudioClipping, Action: "retry",
			TargetStatus: domain.StatusScripted,
			Overrides: map[string]any{
				"tts_stability":        0.90,
				"tts_similarity_boost": 0.65,
			},
		},
	}
}

// --- Render fixes (detected at THUMBNAILED stage) ---

func renderFixes() []FixPlan {
	return []FixPlan{
		// Duration mismatch: plain retry.
		{
			ID: "render_duration_retry", Stage: domain.StatusThumbnailed,
			FailType: FailDurationMismatch, Action: "retry",
			TargetStatus: domain.StatusThumbnailed,
		},
		// Duration mismatch: loopback to re-voice.
		{
			ID: "render_duration_revoice", Stage: domain.StatusThumbnailed,
			FailType: FailDurationMismatch, Action: "loopback",
			TargetStatus: domain.StatusScripted,
			Overrides: map[string]any{
				"tts_speed_factor": 0.95,
			},
		},
		// Render failure: plain retry.
		{
			ID: "render_fail_retry", Stage: domain.StatusThumbnailed,
			FailType: FailRenderFail, Action: "retry",
			TargetStatus: domain.StatusThumbnailed,
		},
	}
}
