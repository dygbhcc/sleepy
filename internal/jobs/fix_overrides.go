package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"sleepy/internal/db"
)

// applyOverrides maps fix plan override keys onto the mutable Policy fields.
func applyOverrides(overrides map[string]any, policy *Policy) {
	for k, v := range overrides {
		switch k {
		case "llm_target_words":
			if n, ok := toInt(v); ok && n > 0 {
				policy.TargetWords = n
				// Widen bounds to accommodate the adjusted target.
				if policy.MinWords > n-50 {
					policy.MinWords = n - 50
				}
				if policy.MaxWords < n+50 {
					policy.MaxWords = n + 50
				}
			}
		case "llm_word_count_factor":
			if f, ok := toFloat(v); ok && f > 0 {
				policy.TargetWords = int(float64(policy.TargetWords) * f)
				policy.MinWords = int(float64(policy.MinWords) * f)
				policy.MaxWords = int(float64(policy.MaxWords) * f)
			}
		case "llm_temperature":
			if f, ok := toFloat(v); ok {
				policy.LLMTemperature = f
			}
		case "llm_extra_instruction":
			if s, ok := v.(string); ok {
				policy.LLMExtraInstruction = s
			}
		case "tts_speed_factor":
			if f, ok := toFloat(v); ok {
				policy.TTSSpeedFactor = f
			}
		case "edge_rate_delta":
			if n, ok := toInt(v); ok {
				policy.EdgeRateDelta = n
			}
		case "tts_stability":
			if f, ok := toFloat(v); ok {
				policy.TTSStability = f
			}
		case "tts_similarity_boost":
			if f, ok := toFloat(v); ok {
				policy.TTSSimilarityBoost = f
			}

		// Meta keys consumed by FixEngine.DecideFix or stepScript — not applied to policy.
		case "target_words_mode", "target_words_buffer", "target_words_abs_buffer", "direction", "continuation":
			// Handled elsewhere; skip.
		default:
			log.Printf("fix_overrides: unknown override key %q", k)
		}
	}
}

// persistAndApply stores the overrides in the DB and applies them to the local policy.
func persistAndApply(ctx context.Context, d *db.DB, runID string, overrides map[string]any, policy *Policy) error {
	data, err := json.Marshal(overrides)
	if err != nil {
		return fmt.Errorf("marshal overrides: %w", err)
	}
	if err := d.UpdatePolicyOverrides(ctx, runID, string(data)); err != nil {
		return fmt.Errorf("persist overrides: %w", err)
	}
	applyOverrides(overrides, policy)
	return nil
}

// loadPersistedOverrides reads stored overrides from the run and applies them to the policy.
func loadPersistedOverrides(run_overrides string, policy *Policy) {
	if run_overrides == "" || run_overrides == "{}" {
		return
	}
	var overrides map[string]any
	if err := json.Unmarshal([]byte(run_overrides), &overrides); err != nil {
		log.Printf("fix_overrides: failed to unmarshal persisted overrides: %v", err)
		return
	}
	applyOverrides(overrides, policy)
}

// clearFixState resets fix-engine tracking columns after a stage advance.
func clearFixState(ctx context.Context, d *db.DB, runID string) {
	_ = d.UpdatePolicyOverrides(ctx, runID, "{}")
	_ = d.UpdateActiveFixPlan(ctx, runID, "", 0)
}

// --- type conversion helpers ---

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case float32:
		return float64(n), true
	default:
		return 0, false
	}
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	default:
		return 0, false
	}
}
