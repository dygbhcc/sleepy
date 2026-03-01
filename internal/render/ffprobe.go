package render

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
)

type probeOutput struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// ProbeDuration returns the duration of an audio or video file in seconds.
func ProbeDuration(ctx context.Context, ffprobeBin, filePath string) (float64, error) {
	if ffprobeBin == "" {
		ffprobeBin = "ffprobe"
	}

	cmd := exec.CommandContext(ctx, ffprobeBin,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return 0, fmt.Errorf("ffprobe %s: %s: %w", filePath, string(ee.Stderr), err)
		}
		return 0, fmt.Errorf("ffprobe %s: %w", filePath, err)
	}

	var p probeOutput
	if err := json.Unmarshal(out, &p); err != nil {
		return 0, fmt.Errorf("parse ffprobe json: %w", err)
	}
	if p.Format.Duration == "" {
		return 0, fmt.Errorf("ffprobe returned empty duration for %s", filePath)
	}

	dur, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", p.Format.Duration, err)
	}
	return dur, nil
}

// ProbeVideoIntegrity decodes the first 10 seconds of the video stream
// and returns an error if ffmpeg reports decode errors (e.g. invalid NAL units).
// This catches corrupt files that ffprobe -show_format passes as valid.
func ProbeVideoIntegrity(ctx context.Context, ffmpegBin, filePath string) error {
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}

	// Decode the first 10s of video to /dev/null; -xerror makes ffmpeg exit
	// non-zero on any decode error instead of silently continuing.
	cmd := exec.CommandContext(ctx, ffmpegBin,
		"-v", "error",
		"-xerror",
		"-t", "10",
		"-i", filePath,
		"-f", "null",
		"-",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := string(out)
		if len(detail) > 500 {
			detail = detail[:500]
		}
		return fmt.Errorf("video integrity check failed: %s: %w", detail, err)
	}
	return nil
}
