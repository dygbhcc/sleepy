package render

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
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

// AudioStats holds audio quality metrics from ffmpeg astats + silencedetect.
type AudioStats struct {
	PeakDB      float64 // peak level in dB (0 = digital max)
	RMSDb       float64 // RMS level in dB
	FlatFactor  float64 // >0 means clipped/flat samples detected
	SilenceSec  float64 // total seconds of silence detected (>5s segments at <-40dB)
	DurationSec float64
}

// ProbeAudioQuality analyses audio quality using ffmpeg astats + silencedetect.
func ProbeAudioQuality(ctx context.Context, ffmpegBin, filePath string) (AudioStats, error) {
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}

	cmd := exec.CommandContext(ctx, ffmpegBin,
		"-i", filePath,
		"-af", "astats=metadata=1:reset=0,silencedetect=noise=-40dB:d=5",
		"-f", "null", "-",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return AudioStats{}, fmt.Errorf("audio quality probe: %w", err)
	}

	stats := AudioStats{}
	output := string(out)
	scanner := bufio.NewScanner(strings.NewReader(output))
	var silenceStart float64

	// We want the "Overall" section values (last occurrence).
	for scanner.Scan() {
		line := scanner.Text()

		if strings.Contains(line, "Peak level dB:") {
			if v, err := parseStatValue(line, "Peak level dB:"); err == nil {
				stats.PeakDB = v
			}
		}
		if strings.Contains(line, "RMS level dB:") {
			if v, err := parseStatValue(line, "RMS level dB:"); err == nil {
				stats.RMSDb = v
			}
		}
		if strings.Contains(line, "Flat factor:") {
			if v, err := parseStatValue(line, "Flat factor:"); err == nil {
				stats.FlatFactor = v
			}
		}

		// silencedetect output: "[silencedetect ...] silence_start: 12.345"
		if strings.Contains(line, "silence_start:") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "silence_start:" && i+1 < len(parts) {
					if v, err := strconv.ParseFloat(parts[i+1], 64); err == nil {
						silenceStart = v
					}
				}
			}
		}
		if strings.Contains(line, "silence_end:") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "silence_end:" && i+1 < len(parts) {
					if v, err := strconv.ParseFloat(parts[i+1], 64); err == nil {
						stats.SilenceSec += v - silenceStart
					}
				}
			}
		}
	}

	stats.DurationSec, _ = ProbeDuration(ctx, "", filePath)
	return stats, nil
}

func parseStatValue(line, key string) (float64, error) {
	idx := strings.Index(line, key)
	if idx < 0 {
		return 0, fmt.Errorf("key not found")
	}
	s := strings.TrimSpace(line[idx+len(key):])
	return strconv.ParseFloat(s, 64)
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
