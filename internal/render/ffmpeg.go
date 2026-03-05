package render

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
)

const (
	DefaultFadeOutSec = 5.0
	OutputFPS         = 25
	OutputWidth       = 1920
	OutputHeight      = 1080
)

// RenderConfig controls ffmpeg rendering behaviour.
type RenderConfig struct {
	FFmpegBin  string
	FFprobeBin string
	FadeOutSec float64
	MusicPath  string  // optional background music file
	MusicVol   float64 // music volume relative to narration (0.0–1.0); default 0.5
}

// Render creates a video from a single static background image and narration audio.
// If MusicPath is set, ambient music is mixed under the narration at MusicVol.
func Render(ctx context.Context, cfg RenderConfig, imagePath, audioPath string, audioDurSec float64, outPath string) error {
	if cfg.FFmpegBin == "" {
		cfg.FFmpegBin = "ffmpeg"
	}
	if cfg.FadeOutSec <= 0 {
		cfg.FadeOutSec = DefaultFadeOutSec
	}
	if cfg.MusicVol <= 0 {
		cfg.MusicVol = 0.5
	}

	fadeStart := math.Max(0, audioDurSec-cfg.FadeOutSec)

	// Video filter: static image scaled to output size + fade-out.
	vFilter := fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,fade=t=out:st=%.2f:d=%.2f",
		OutputWidth, OutputHeight, OutputWidth, OutputHeight,
		fadeStart, cfg.FadeOutSec,
	)

	// Check if we have background music to mix.
	hasMusic := cfg.MusicPath != ""
	if hasMusic {
		if _, err := os.Stat(cfg.MusicPath); err != nil {
			log.Printf("render: music file not found at %q, rendering without music", cfg.MusicPath)
			hasMusic = false
		}
	}

	if hasMusic {
		log.Printf("render: mixing with music %s (vol=%.0f%%)", cfg.MusicPath, cfg.MusicVol*100)
		return renderWithMusic(ctx, cfg, imagePath, audioPath, audioDurSec, fadeStart, vFilter, outPath)
	}
	log.Println("render: no music, rendering narration only")
	return renderSimple(ctx, cfg, imagePath, audioPath, audioDurSec, fadeStart, vFilter, outPath)
}

// renderWithMusic mixes narration (full volume) + ambient music (MusicVol) then applies fade-out.
// Music is looped (-stream_loop -1) so it never runs out before narration ends.
// amix uses duration=first (narration) and normalize=0 to prevent automatic volume halving.
func renderWithMusic(ctx context.Context, cfg RenderConfig, imagePath, audioPath string, audioDurSec, fadeStart float64, vFilter, outPath string) error {
	// Everything in one filter_complex: video + audio mixing.
	// normalize=0 prevents amix from dividing each input's volume by N.
	// duration=first makes the mix last as long as the narration (first amix input).
	fc := fmt.Sprintf(
		"[0:v]%s[vout];"+
			"[1:a]volume=1.0[narr];"+
			"[2:a]volume=%.2f[music];"+
			"[narr][music]amix=inputs=2:duration=first:normalize=0,afade=t=out:st=%.2f:d=%.2f[aout]",
		vFilter,
		cfg.MusicVol,
		fadeStart, cfg.FadeOutSec,
	)

	duration := fmt.Sprintf("%.2f", audioDurSec)

	cmd := exec.CommandContext(ctx, cfg.FFmpegBin,
		"-loop", "1", "-framerate", fmt.Sprintf("%d", OutputFPS), "-t", duration, "-i", imagePath,
		"-t", duration, "-i", audioPath,
		"-stream_loop", "-1", "-i", cfg.MusicPath,
		"-filter_complex", fc,
		"-map", "[vout]", "-map", "[aout]",
		"-c:v", "libx264", "-profile:v", "high", "-level", "4.0",
		"-pix_fmt", "yuv420p", "-tune", "stillimage",
		"-preset", "medium", "-crf", "23",
		"-g", "50", "-bf", "0",
		"-maxrate", "5M", "-bufsize", "10M",
		"-c:a", "aac", "-b:a", "192k",
		"-movflags", "+faststart",
		"-y", outPath,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg render (with music): %s: %w", truncateStr(string(out), 500), err)
	}
	return nil
}

// renderSimple renders with narration only (no background music).
func renderSimple(ctx context.Context, cfg RenderConfig, imagePath, audioPath string, audioDurSec, fadeStart float64, vFilter, outPath string) error {
	aFilter := fmt.Sprintf("afade=t=out:st=%.2f:d=%.2f", fadeStart, cfg.FadeOutSec)
	duration := fmt.Sprintf("%.2f", audioDurSec)

	cmd := exec.CommandContext(ctx, cfg.FFmpegBin,
		"-loop", "1", "-framerate", fmt.Sprintf("%d", OutputFPS), "-t", duration, "-i", imagePath,
		"-t", duration, "-i", audioPath,
		"-vf", vFilter,
		"-af", aFilter,
		"-c:v", "libx264", "-profile:v", "high", "-level", "4.0",
		"-pix_fmt", "yuv420p", "-tune", "stillimage",
		"-preset", "medium", "-crf", "23",
		"-g", "50", "-bf", "0",
		"-maxrate", "5M", "-bufsize", "10M",
		"-c:a", "aac", "-b:a", "192k",
		"-movflags", "+faststart",
		"-y", outPath,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg render: %s: %w", truncateStr(string(out), 500), err)
	}
	return nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
