package ttsreliability

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ProbeChunkMetrics runs ffmpeg analysis on an audio file and returns QA metrics.
// It extracts: peak, RMS, flat factor, silence, LUFS, and a spectral proxy.
func ProbeChunkMetrics(ctx context.Context, ffmpegBin, filePath string) (QAMetrics, error) {
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}

	var m QAMetrics

	// Pass 1: astats + silencedetect for peak, RMS, flat factor, silence.
	if err := probeAstats(ctx, ffmpegBin, filePath, &m); err != nil {
		return m, fmt.Errorf("astats probe: %w", err)
	}

	// Pass 2: loudnorm for integrated LUFS.
	if err := probeLUFS(ctx, ffmpegBin, filePath, &m); err != nil {
		// LUFS probe failure is non-fatal; log and continue.
		m.LUFS = -99
	}

	// Pass 3: duration via ffprobe.
	if err := probeDuration(ctx, ffmpegBin, filePath, &m); err != nil {
		return m, fmt.Errorf("duration probe: %w", err)
	}

	// Compute spectral proxy: wider gap = richer frequency content.
	m.SpectralProxy = m.PeakDB - m.RMSDb

	return m, nil
}

func probeAstats(ctx context.Context, ffmpegBin, filePath string, m *QAMetrics) error {
	cmd := exec.CommandContext(ctx, ffmpegBin,
		"-i", filePath,
		"-af", "astats=metadata=1:reset=0,silencedetect=noise=-40dB:d=3",
		"-f", "null", "-",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg astats: %w", err)
	}

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	var silenceStart float64

	for scanner.Scan() {
		line := scanner.Text()

		if strings.Contains(line, "Peak level dB:") {
			if v, err := parseValue(line, "Peak level dB:"); err == nil {
				m.PeakDB = v
			}
		}
		if strings.Contains(line, "RMS level dB:") {
			if v, err := parseValue(line, "RMS level dB:"); err == nil {
				m.RMSDb = v
			}
		}
		if strings.Contains(line, "Flat factor:") {
			if v, err := parseValue(line, "Flat factor:"); err == nil {
				m.FlatFactor = v
			}
		}

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
						m.SilenceSec += v - silenceStart
					}
				}
			}
		}
	}

	return nil
}

func probeLUFS(ctx context.Context, ffmpegBin, filePath string, m *QAMetrics) error {
	cmd := exec.CommandContext(ctx, ffmpegBin,
		"-i", filePath,
		"-af", "loudnorm=I=-16:TP=-1.5:LRA=11:print_format=summary",
		"-f", "null", "-",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg loudnorm: %w", err)
	}

	// Parse "Input Integrated:" line from loudnorm summary.
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "Input Integrated:") {
			if v, err := parseLUFSValue(line); err == nil {
				m.LUFS = v
				return nil
			}
		}
	}

	return fmt.Errorf("LUFS not found in loudnorm output")
}

func probeDuration(ctx context.Context, ffmpegBin, filePath string, m *QAMetrics) error {
	// Use ffprobe (typically same dir as ffmpeg).
	ffprobeBin := strings.TrimSuffix(ffmpegBin, "ffmpeg") + "ffprobe"
	if ffmpegBin == "ffmpeg" {
		ffprobeBin = "ffprobe"
	}

	cmd := exec.CommandContext(ctx, ffprobeBin,
		"-v", "quiet",
		"-print_format", "default=noprint_wrappers=1:nokey=1",
		"-show_entries", "format=duration",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("ffprobe duration: %w", err)
	}
	dur, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return fmt.Errorf("parse duration: %w", err)
	}
	m.DurationSec = dur
	return nil
}

func parseValue(line, key string) (float64, error) {
	idx := strings.Index(line, key)
	if idx < 0 {
		return 0, fmt.Errorf("key not found")
	}
	s := strings.TrimSpace(line[idx+len(key):])
	return strconv.ParseFloat(s, 64)
}

func parseLUFSValue(line string) (float64, error) {
	// Line format: "Input Integrated:    -14.2 LUFS"
	idx := strings.Index(line, "Input Integrated:")
	if idx < 0 {
		return 0, fmt.Errorf("key not found")
	}
	rest := strings.TrimSpace(line[idx+len("Input Integrated:"):])
	rest = strings.TrimSuffix(rest, "LUFS")
	rest = strings.TrimSpace(rest)
	return strconv.ParseFloat(rest, 64)
}
