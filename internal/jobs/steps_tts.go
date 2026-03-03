package jobs

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"sleepy/internal/domain"
	"sleepy/internal/providers/tts"
	"sleepy/internal/ttsreliability"
)

// needsPlainText returns true if the text contains SSML tags
// that edge-tts cannot handle.
func needsPlainText(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "<speak") || strings.Contains(t, "<break")
}

func stepTTS(ctx context.Context, deps Deps, run *domain.Run, policy Policy) error {
	log.Printf("step_tts: synthesizing audio for run %s", run.ID)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// Check idempotency: if narration.wav already exists and script hasn't changed, skip.
	// This protects against wasting TTS credits (especially ElevenLabs).
	scriptAsset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetScriptMD)
	if err != nil {
		return fmt.Errorf("no script found for TTS: %w", err)
	}
	currentHash, _ := computeFileHash(scriptAsset.Path)

	// Strong guard: if narration.wav exists with non-zero size, only re-generate
	// if the script content has actually changed. Override-based retries (speed tweaks)
	// are allowed because they produce different audio from the same script.
	hasOverrides := policy.TTSSpeedFactor != 0 || policy.EdgeRateDelta != 0 ||
		policy.TTSStability != 0 || policy.TTSSimilarityBoost != 0

	if currentHash != "" && currentHash == run.VoiceHash && !hasOverrides {
		if audioAsset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetNarrationWAV); err == nil {
			if info, err := os.Stat(audioAsset.Path); err == nil && info.Size() > 0 {
				log.Printf("step_tts: skipping (input hash unchanged, narration exists: %s)", currentHash[:12])
				return nil
			}
		}
	}

	// Try SSML first (for ElevenLabs), fall back to plain markdown (for Edge TTS).
	var text string
	asset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetScriptSSML)
	if err == nil {
		raw, readErr := os.ReadFile(asset.Path)
		if readErr == nil {
			text = string(raw)
		}
	}
	// If SSML is empty or starts with <speak>, and we have a markdown script,
	// use that instead — Edge TTS doesn't support SSML.
	if text == "" || needsPlainText(text) {
		raw, readErr := os.ReadFile(scriptAsset.Path)
		if readErr == nil && len(raw) > 0 {
			text = string(raw)
			log.Println("step_tts: using markdown script (plain text)")
		}
	}
	if text == "" {
		return fmt.Errorf("no script text found for TTS")
	}

	outPath := deps.Store.Path(run.ID, "narration.wav")

	if hasOverrides {
		log.Printf("step_tts: applying fix overrides speed=%.2f rateDelta=%d stability=%.2f similarity=%.2f",
			policy.TTSSpeedFactor, policy.EdgeRateDelta, policy.TTSStability, policy.TTSSimilarityBoost)
	}

	// ElevenLabs: use the reliability layer (per-chunk QA, retries, budget guard).
	if elClient, ok := deps.TTS.(*tts.Client); ok {
		provider := ttsreliability.NewElevenLabsAdapter(elClient)
		orch := ttsreliability.NewOrchestrator(provider, deps.DB.Pool(), deps.Render.FFmpegBin)
		result := orch.Run(ctx, ttsreliability.TTSJob{
			RunID:      run.ID,
			Text:       text,
			OutputPath: outPath,
			FFmpegBin:  deps.Render.FFmpegBin,
			Settings: ttsreliability.TTSSettings{
				Speed:           policy.TTSSpeedFactor,
				Stability:       policy.TTSStability,
				SimilarityBoost: policy.TTSSimilarityBoost,
			},
		})
		if !result.Success {
			return fmt.Errorf("tts reliability: %s (attempts=%d cost=$%.4f) %s",
				result.FailType, result.TotalAttempts, result.TotalCostUSD, result.Details)
		}
		log.Printf("step_tts: reliability layer done (chunks=%d attempts=%d cost=$%.4f)",
			len(result.Chunks), result.TotalAttempts, result.TotalCostUSD)
	} else if edgeTTS, ok := deps.TTS.(EdgeTTSOverridable); ok && (hasOverrides || run.Language != "") {
		if err := edgeTTS.SynthesizeWithOpts(ctx, text, outPath, policy.EdgeRateDelta, run.Language); err != nil {
			return fmt.Errorf("tts synthesize: %w", err)
		}
	} else if laTTS, ok := deps.TTS.(LanguageAwareTTS); ok && run.Language != "" {
		if err := laTTS.SynthesizeWithLang(ctx, text, outPath, run.Language); err != nil {
			return fmt.Errorf("tts synthesize: %w", err)
		}
	} else if err := deps.TTS.Synthesize(ctx, text, outPath); err != nil {
		return fmt.Errorf("tts synthesize: %w", err)
	}

	if err := deps.DB.InsertAsset(ctx, run.ID, domain.AssetNarrationWAV, outPath); err != nil {
		return fmt.Errorf("record narration asset: %w", err)
	}

	// Store voice hash (keyed on script input) for idempotency.
	if currentHash != "" {
		_ = deps.DB.UpdateRunHash(ctx, run.ID, "voice_hash", currentHash)
	}

	log.Printf("step_tts: done → %s", outPath)
	return nil
}
