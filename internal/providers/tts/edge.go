package tts

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
)

// EdgeConfig holds settings for the free Microsoft Edge TTS.
type EdgeConfig struct {
	Voice     string // e.g. "en-US-AndrewNeural"; default "en-US-AndrewNeural"
	Rate      string // e.g. "-20%"; default "-20%" (slower for sleep)
	FFmpegBin string
	Normalize bool
}

// EdgeClient wraps the edge-tts CLI (python3 -m edge_tts).
type EdgeClient struct {
	cfg EdgeConfig
}

// NewEdgeClient creates a configured Edge TTS client.
func NewEdgeClient(cfg EdgeConfig) *EdgeClient {
	if cfg.Voice == "" {
		cfg.Voice = "en-US-AndrewNeural"
	}
	if cfg.Rate == "" {
		cfg.Rate = "-20%"
	}
	if cfg.FFmpegBin == "" {
		cfg.FFmpegBin = "ffmpeg"
	}
	return &EdgeClient{cfg: cfg}
}

// Synthesize generates speech from text using edge-tts, then converts to WAV.
func (c *EdgeClient) Synthesize(ctx context.Context, text string, outPath string) error {
	mp3Path := outPath + ".tmp.mp3"

	log.Printf("edge-tts: synthesizing with voice=%s rate=%s", c.cfg.Voice, c.cfg.Rate)

	// Write text to temp file to avoid shell escaping issues.
	txtPath := outPath + ".tmp.txt"
	if err := os.WriteFile(txtPath, []byte(text), 0644); err != nil {
		return fmt.Errorf("write text file: %w", err)
	}
	defer os.Remove(txtPath)

	// Call edge-tts. Use --rate=VALUE format to prevent argparse from
	// interpreting negative values like "-20%" as flags.
	cmd := exec.CommandContext(ctx, "python3", "-m", "edge_tts",
		"--voice="+c.cfg.Voice,
		"--rate="+c.cfg.Rate,
		"--file="+txtPath,
		"--write-media="+mp3Path,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("edge-tts: %s: %w", truncateBytes(out, 500), err)
	}
	defer os.Remove(mp3Path)

	// Convert MP3 to WAV (44100 Hz mono), optionally normalize.
	if c.cfg.Normalize {
		return convertAndNormalizeFFmpeg(ctx, c.cfg.FFmpegBin, mp3Path, outPath)
	}
	return convertToWAVFFmpeg(ctx, c.cfg.FFmpegBin, mp3Path, outPath)
}

func convertAndNormalizeFFmpeg(ctx context.Context, ffmpegBin, mp3Path, wavPath string) error {
	cmd := exec.CommandContext(ctx, ffmpegBin,
		"-i", mp3Path,
		"-af", "loudnorm=I=-16:TP=-1.5:LRA=11",
		"-ar", "44100",
		"-ac", "1",
		"-y", wavPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg loudnorm: %s: %w", truncateBytes(out, 300), err)
	}
	return nil
}

func convertToWAVFFmpeg(ctx context.Context, ffmpegBin, mp3Path, wavPath string) error {
	cmd := exec.CommandContext(ctx, ffmpegBin,
		"-i", mp3Path,
		"-ar", "44100",
		"-ac", "1",
		"-y", wavPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg convert: %s: %w", truncateBytes(out, 300), err)
	}
	return nil
}
