package render

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	ReelsWidth  = 1080
	ReelsHeight = 1920
	ReelsFPS    = 30
)

// ReelsConfig controls vertical video rendering for Instagram Reels.
type ReelsConfig struct {
	FFmpegBin  string
	FFprobeBin string
	MusicPath  string  // optional background music
	MusicVol   float64 // 0.0-1.0, default 0.3
}

// BRollClip represents a downloaded B-roll video segment.
type BRollClip struct {
	Path     string
	Duration int // seconds
}

// RenderReels creates a 9:16 vertical video from B-roll clips + voiceover audio.
// Clips are concatenated and scaled to fit 1080x1920, with the voiceover mixed over.
func RenderReels(ctx context.Context, cfg ReelsConfig, clips []BRollClip, audioPath string, audioDurSec float64, outPath string) error {
	if cfg.FFmpegBin == "" {
		cfg.FFmpegBin = "ffmpeg"
	}
	if cfg.MusicVol <= 0 {
		cfg.MusicVol = 0.3
	}

	if len(clips) == 0 {
		return fmt.Errorf("no B-roll clips provided")
	}

	// Step 1: Build concat file for B-roll clips.
	tmpDir := filepath.Dir(outPath)
	concatPath := filepath.Join(tmpDir, "reels_concat.txt")
	defer os.Remove(concatPath)

	var buf strings.Builder
	for _, c := range clips {
		abs, _ := filepath.Abs(c.Path)
		fmt.Fprintf(&buf, "file '%s'\n", abs)
	}
	if err := os.WriteFile(concatPath, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("write concat file: %w", err)
	}

	// Step 2: Concat B-roll into a single video, scaled to 9:16.
	rawVideo := filepath.Join(tmpDir, "reels_raw.mp4")
	defer os.Remove(rawVideo)

	duration := fmt.Sprintf("%.2f", audioDurSec)

	// Scale and crop to portrait, trim to audio duration.
	vFilter := fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,setsar=1",
		ReelsWidth, ReelsHeight, ReelsWidth, ReelsHeight,
	)

	cmdConcat := exec.CommandContext(ctx, cfg.FFmpegBin,
		"-f", "concat", "-safe", "0", "-i", concatPath,
		"-t", duration,
		"-vf", vFilter,
		"-c:v", "libx264", "-preset", "fast", "-crf", "23",
		"-pix_fmt", "yuv420p",
		"-an", // no audio from B-roll
		"-y", rawVideo,
	)
	if out, err := cmdConcat.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg concat B-roll: %s: %w", truncateStr(string(out), 500), err)
	}

	// Step 3: Merge video + voiceover (+ optional music).
	hasMusic := cfg.MusicPath != ""
	if hasMusic {
		if _, err := os.Stat(cfg.MusicPath); err != nil {
			hasMusic = false
		}
	}

	if hasMusic {
		return renderReelsWithMusic(ctx, cfg, rawVideo, audioPath, audioDurSec, outPath)
	}
	return renderReelsSimple(ctx, cfg, rawVideo, audioPath, audioDurSec, outPath)
}

func renderReelsSimple(ctx context.Context, cfg ReelsConfig, videoPath, audioPath string, audioDurSec float64, outPath string) error {
	duration := fmt.Sprintf("%.2f", audioDurSec)
	fadeStart := audioDurSec - 2.0
	if fadeStart < 0 {
		fadeStart = 0
	}

	aFilter := fmt.Sprintf("afade=t=out:st=%.2f:d=2.0", fadeStart)
	vFilter := fmt.Sprintf("fade=t=out:st=%.2f:d=2.0", fadeStart)

	cmd := exec.CommandContext(ctx, cfg.FFmpegBin,
		"-i", videoPath,
		"-t", duration, "-i", audioPath,
		"-vf", vFilter,
		"-af", aFilter,
		"-c:v", "libx264", "-preset", "medium", "-crf", "23",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "192k",
		"-movflags", "+faststart",
		"-shortest",
		"-y", outPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg reels render: %s: %w", truncateStr(string(out), 500), err)
	}
	return nil
}

func renderReelsWithMusic(ctx context.Context, cfg ReelsConfig, videoPath, audioPath string, audioDurSec float64, outPath string) error {
	duration := fmt.Sprintf("%.2f", audioDurSec)
	fadeStart := audioDurSec - 2.0
	if fadeStart < 0 {
		fadeStart = 0
	}

	fc := fmt.Sprintf(
		"[0:v]fade=t=out:st=%.2f:d=2.0[vout];"+
			"[1:a]volume=1.0[narr];"+
			"[2:a]volume=%.2f[music];"+
			"[narr][music]amix=inputs=2:duration=first:normalize=0,afade=t=out:st=%.2f:d=2.0[aout]",
		fadeStart,
		cfg.MusicVol,
		fadeStart,
	)

	cmd := exec.CommandContext(ctx, cfg.FFmpegBin,
		"-i", videoPath,
		"-t", duration, "-i", audioPath,
		"-stream_loop", "-1", "-i", cfg.MusicPath,
		"-filter_complex", fc,
		"-map", "[vout]", "-map", "[aout]",
		"-c:v", "libx264", "-preset", "medium", "-crf", "23",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "192k",
		"-movflags", "+faststart",
		"-shortest",
		"-y", outPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg reels render (music): %s: %w", truncateStr(string(out), 500), err)
	}
	return nil
}
