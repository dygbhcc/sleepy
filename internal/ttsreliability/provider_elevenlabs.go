package ttsreliability

import (
	"context"
	"fmt"

	"sleepy/internal/providers/tts"
)

// ElevenLabsAdapter wraps the existing tts.Client to implement TTSProvider.
type ElevenLabsAdapter struct {
	client *tts.Client
}

// NewElevenLabsAdapter creates an adapter around the existing ElevenLabs client.
func NewElevenLabsAdapter(client *tts.Client) *ElevenLabsAdapter {
	return &ElevenLabsAdapter{client: client}
}

// SynthesizeChunk synthesizes a single text chunk to an MP3/WAV file.
// Chunks are small enough (500-900 words) that ElevenLabs' internal chunking
// won't be triggered.
func (a *ElevenLabsAdapter) SynthesizeChunk(ctx context.Context, text string, settings TTSSettings, outPath string) error {
	hasOverrides := settings.Speed != 0 || settings.Stability != 0 || settings.SimilarityBoost != 0

	if hasOverrides {
		if err := a.client.SynthesizeWithOpts(ctx, text, outPath,
			settings.Speed, settings.Stability, settings.SimilarityBoost); err != nil {
			return fmt.Errorf("elevenlabs chunk: %w", err)
		}
		return nil
	}

	if err := a.client.Synthesize(ctx, text, outPath); err != nil {
		return fmt.Errorf("elevenlabs chunk: %w", err)
	}
	return nil
}
