package jobs

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"sleepy/internal/domain"
)

// needsPlainText returns true if the text contains SSML tags
// that edge-tts cannot handle.
func needsPlainText(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "<speak") || strings.Contains(t, "<break")
}

func stepTTS(ctx context.Context, deps Deps, run *domain.Run) error {
	log.Printf("step_tts: synthesizing audio for run %s", run.ID)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// Check idempotency: if script hasn't changed since last voice attempt, skip.
	scriptAsset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetScriptMD)
	if err != nil {
		return fmt.Errorf("no script found for TTS: %w", err)
	}
	currentHash, _ := computeFileHash(scriptAsset.Path)
	if currentHash != "" && currentHash == run.VoiceHash {
		// Input hasn't changed and we already have audio — check if it exists.
		if audioAsset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetNarrationWAV); err == nil {
			if info, err := os.Stat(audioAsset.Path); err == nil && info.Size() > 0 {
				log.Printf("step_tts: skipping (input hash unchanged: %s)", currentHash[:12])
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
	// Use language-aware synthesis if the TTS provider supports it.
	if laTTS, ok := deps.TTS.(LanguageAwareTTS); ok && run.Language != "" {
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
