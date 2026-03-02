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
		// WORDCOUNT_LOW: continue existing script instead of regenerating.
		{
			ID: "script_wc_low_continue", Stage: domain.StatusPending,
			FailType: FailWordcountLow, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"continuation":    true,
				"llm_temperature": 0.7,
			},
		},
		// WORDCOUNT_LOW (continuation with richer detail).
		{
			ID: "script_wc_low_continue_expand", Stage: domain.StatusPending,
			FailType: FailWordcountLow, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"continuation":          true,
				"llm_temperature":       0.8,
				"llm_extra_instruction": "Add rich sensory details, new scenes, and diverse imagery in the continuation.",
			},
		},
		// WORDCOUNT_LOW (full regen fallback): if continuation failed twice.
		{
			ID: "script_wc_low_regen", Stage: domain.StatusPending,
			FailType: FailWordcountLow, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"target_words_mode":       "min_padded",
				"target_words_abs_buffer": 300,
				"llm_temperature":         0.7,
				"llm_extra_instruction":   "Write a complete script from scratch. Expand descriptions, add sensory detail, and use longer paragraphs.",
			},
		},
		// WORDCOUNT_HIGH: set target to max_words - 150 via diag.
		{
			ID: "script_wc_high_max_capped", Stage: domain.StatusPending,
			FailType: FailWordcountHigh, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"target_words_mode":       "max_capped",
				"target_words_abs_buffer": 150,
				"llm_temperature":         0.5,
				"llm_extra_instruction":   "The previous script exceeded the word limit. REMOVE entire sections if needed. Be more concise. Stop writing when you reach the target word count. Hard cap at MaxWords.",
			},
		},
		// WORDCOUNT_HIGH (aggressive): larger buffer, lower temp, forceful instruction.
		{
			ID: "script_wc_high_aggressive", Stage: domain.StatusPending,
			FailType: FailWordcountHigh, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"target_words_mode":       "max_capped",
				"target_words_abs_buffer": 250,
				"llm_temperature":         0.3,
				"llm_extra_instruction":   "CRITICAL: The script MUST be shorter. Drastically reduce paragraph count. REMOVE sections until within range. Every sentence must be essential. STOP IMMEDIATELY once within range. Do NOT add any content after reaching the word limit.",
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
		// PACING_FAIL: higher temperature for variety + anti-repetition + word cap.
		{
			ID: "script_pacing_variety", Stage: domain.StatusPending,
			FailType: FailPacingFail, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"target_words_mode":       "max_capped",
				"target_words_abs_buffer": 200,
				"llm_temperature":         0.8,
				"llm_extra_instruction":   "Vary your imagery and phrasing throughout. Never repeat a sentence or close paraphrase. Each paragraph must introduce fresh sensory details. Maintain a slow, drifting pace but with diverse vocabulary and scenes. IMPORTANT: Achieve variety by REPLACING repetitive content, not by adding more. Stay within the word limit.",
			},
		},
		// PACING_FAIL (aggressive): maximize variety + word cap.
		{
			ID: "script_pacing_aggressive", Stage: domain.StatusPending,
			FailType: FailPacingFail, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"target_words_mode":       "max_capped",
				"target_words_abs_buffer": 200,
				"llm_temperature":         0.9,
				"llm_extra_instruction":   "CRITICAL: The previous script had repetitive patterns. Every paragraph MUST use completely different imagery. No two paragraphs should describe similar scenes. Vary sentence length and structure. CRITICAL: Do NOT write more words to fix repetition. Write FEWER, more diverse paragraphs. Stay strictly within the word limit.",
			},
		},
		// TITLE_INVALID: regenerate script + title.
		{
			ID: "script_title_retry", Stage: domain.StatusPending,
			FailType: FailTitleInvalid, Action: "retry",
			TargetStatus: domain.StatusPending,
			Overrides: map[string]any{
				"llm_temperature": 0.6,
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
		// Duration mismatch: loopback to re-voice (plain retry to THUMBNAILED
		// is a no-op because render skips via input-hash cache).
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
