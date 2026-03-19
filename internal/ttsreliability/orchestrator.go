package ttsreliability

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Orchestrator coordinates chunked TTS synthesis with per-chunk QA and retries.
type Orchestrator struct {
	provider  TTSProvider
	store     *ArtifactStore
	ledger    *Ledger
	ffmpegBin string
	th        Thresholds
}

// NewOrchestrator creates an orchestrator with default thresholds.
func NewOrchestrator(provider TTSProvider, db *sql.DB, ffmpegBin string) *Orchestrator {
	return &Orchestrator{
		provider:  provider,
		store:     NewArtifactStore("data/artifacts/tts"),
		ledger:    NewLedger(db),
		ffmpegBin: ffmpegBin,
		th:        DefaultThresholds(),
	}
}

// Run executes the full TTS pipeline: chunk → synthesize → QA → assemble.
func (o *Orchestrator) Run(ctx context.Context, job TTSJob) TTSResult {
	chunks := ChunkText(job.Text, DefaultMinWordsPerChunk, DefaultMaxWordsPerChunk)
	if len(chunks) == 0 {
		return TTSResult{FailType: FailProviderError, Details: "no text to synthesize"}
	}

	maxTotalAttempts := len(chunks) * BudgetMultiplier
	if maxTotalAttempts < 6 {
		maxTotalAttempts = 6
	}
	totalAttempts := 0
	totalCost := 0.0

	log.Printf("tts-reliability: run=%s chunks=%d maxAttempts=%d", job.RunID, len(chunks), maxTotalAttempts)

	results := make([]ChunkResult, len(chunks))

	for i, chunk := range chunks {
		if totalAttempts >= maxTotalAttempts {
			log.Printf("tts-reliability: budget exhausted at chunk %d (total=%d)", i, totalAttempts)
			return TTSResult{
				FailType:      FailBudgetExhausted,
				TotalAttempts: totalAttempts,
				TotalCostUSD:  totalCost,
				Chunks:        results[:i],
				Details:       fmt.Sprintf("budget exhausted at chunk %d/%d after %d attempts", i, len(chunks), totalAttempts),
			}
		}

		result, attempts, cost := o.processChunk(ctx, job, chunk, job.Settings, maxTotalAttempts-totalAttempts)
		totalAttempts += attempts
		totalCost += cost
		results[i] = result

		if !result.Pass {
			log.Printf("tts-reliability: chunk %d failed after %d attempts: %s", i, attempts, result.FailType)
			return TTSResult{
				FailType:      result.FailType,
				TotalAttempts: totalAttempts,
				TotalCostUSD:  totalCost,
				Chunks:        results[:i+1],
				Details:       fmt.Sprintf("chunk %d failed: %s", i, result.FailType),
			}
		}

		log.Printf("tts-reliability: chunk %d/%d passed (attempt=%d lufs=%.1f spectral=%.1f)",
			i+1, len(chunks), result.Attempt, result.Metrics.LUFS, result.Metrics.SpectralProxy)
	}

	// Final cross-chunk QA.
	finalQA := QAFinal(results, o.th)
	if !finalQA.Pass {
		log.Printf("tts-reliability: final QA failed: %s — %s", finalQA.FailType, finalQA.Details)
		return TTSResult{
			FailType:      finalQA.FailType,
			TotalAttempts: totalAttempts,
			TotalCostUSD:  totalCost,
			Chunks:        results,
			Details:       finalQA.Details,
		}
	}

	// Assemble chunks into final audio (retry up to 3 times for transient errors).
	finalPath := job.OutputPath
	if finalPath == "" {
		finalPath = o.store.FinalPath(job.RunID)
	}
	var assemblyErr error
	for assemblyAttempt := 1; assemblyAttempt <= 3; assemblyAttempt++ {
		assemblyErr = o.assembleChunks(ctx, results, finalPath)
		if assemblyErr == nil {
			break
		}
		log.Printf("tts-reliability: assembly attempt %d/3 failed: %v", assemblyAttempt, assemblyErr)
	}
	if assemblyErr != nil {
		return TTSResult{
			FailType:      FailProviderError,
			TotalAttempts: totalAttempts,
			TotalCostUSD:  totalCost,
			Chunks:        results,
			Details:       fmt.Sprintf("assembly (3 attempts): %v", assemblyErr),
		}
	}

	log.Printf("tts-reliability: run=%s done (chunks=%d attempts=%d cost=$%.4f) → %s",
		job.RunID, len(chunks), totalAttempts, totalCost, finalPath)

	return TTSResult{
		FinalAudioPath: finalPath,
		Success:        true,
		TotalAttempts:  totalAttempts,
		TotalCostUSD:   totalCost,
		Chunks:         results,
	}
}

// processChunk synthesizes a single chunk with retry loop.
func (o *Orchestrator) processChunk(ctx context.Context, job TTSJob, chunk TTSChunk,
	settings TTSSettings, budgetLeft int) (ChunkResult, int, float64) {

	// Check if a previous run already produced a passing artifact for this chunk.
	if result, ok := o.tryExistingArtifact(ctx, job, chunk); ok {
		log.Printf("tts-reliability: [run=%s chunk=%d] reusing existing artifact (no credit spent)",
			job.RunID, chunk.Index)
		return result, 0, 0
	}

	attempts := 0
	cost := 0.0
	currentSettings := settings
	currentChunk := chunk

	for attempt := 1; attempt <= MaxAttemptsPerChunk && attempts < budgetLeft; attempt++ {
		attempts++
		charCount := len(currentChunk.Text)
		attemptCost := float64(charCount) * CostPerCharUSD
		cost += attemptCost

		outPath := o.store.ChunkPath(job.RunID, currentChunk.Index, attempt)
		if err := EnsureDir(outPath); err != nil {
			log.Printf("tts-reliability: [run=%s chunk=%d attempt=%d] mkdir error: %v",
				job.RunID, currentChunk.Index, attempt, err)
			continue
		}

		idemKey := IdempotencyKey(job.RunID, currentChunk.Index, attempt, currentSettings)

		log.Printf("tts-reliability: [run=%s chunk=%d attempt=%d] synthesizing %d chars",
			job.RunID, currentChunk.Index, attempt, charCount)

		// Synthesize.
		if err := o.provider.SynthesizeChunk(ctx, currentChunk.Text, currentSettings, outPath); err != nil {
			log.Printf("tts-reliability: [run=%s chunk=%d attempt=%d] provider error: %v",
				job.RunID, currentChunk.Index, attempt, err)
			o.recordAttempt(ctx, job.RunID, currentChunk.Index, attempt, charCount,
				false, attemptCost, FailProviderError, QAMetrics{}, currentSettings, idemKey, outPath)
			continue
		}

		// Probe metrics.
		ffmpeg := job.FFmpegBin
		if ffmpeg == "" {
			ffmpeg = o.ffmpegBin
		}
		metrics, err := ProbeChunkMetrics(ctx, ffmpeg, outPath)
		if err != nil {
			log.Printf("tts-reliability: [run=%s chunk=%d attempt=%d] probe error: %v",
				job.RunID, currentChunk.Index, attempt, err)
			o.recordAttempt(ctx, job.RunID, currentChunk.Index, attempt, charCount,
				false, attemptCost, FailProviderError, QAMetrics{}, currentSettings, idemKey, outPath)
			continue
		}

		// QA check.
		qaResult := QAChunk(metrics, o.th)

		// Apply post-process if needed from previous fix.
		// (handled in fix action below)

		o.recordAttempt(ctx, job.RunID, currentChunk.Index, attempt, charCount,
			qaResult.Pass, attemptCost, qaResult.FailType, metrics, currentSettings, idemKey, outPath)

		if qaResult.Pass {
			return ChunkResult{
				Index:     currentChunk.Index,
				AudioPath: outPath,
				Metrics:   metrics,
				Pass:      true,
				Attempt:   attempt,
			}, attempts, cost
		}

		log.Printf("tts-reliability: [run=%s chunk=%d attempt=%d] QA fail: %s — %s",
			job.RunID, currentChunk.Index, attempt, qaResult.FailType, qaResult.Details)

		// Decide fix.
		fix := DecideFix(qaResult.FailType, attempt, currentSettings)
		if fix.Action == "give_up" {
			return ChunkResult{
				Index:    currentChunk.Index,
				Metrics:  metrics,
				FailType: qaResult.FailType,
			}, attempts, cost
		}

		// Apply loudnorm post-process if requested.
		if fix.PostProcess == "loudnorm" {
			if err := applyLoudnorm(ctx, ffmpeg, outPath); err != nil {
				log.Printf("tts-reliability: loudnorm post-process failed: %v", err)
			} else {
				// Re-probe after post-process.
				if newMetrics, err := ProbeChunkMetrics(ctx, ffmpeg, outPath); err == nil {
					reQA := QAChunk(newMetrics, o.th)
					if reQA.Pass {
						return ChunkResult{
							Index:     currentChunk.Index,
							AudioPath: outPath,
							Metrics:   newMetrics,
							Pass:      true,
							Attempt:   attempt,
						}, attempts, cost
					}
				}
			}
		}

		// Apply settings changes.
		if fix.NewSettings != (TTSSettings{}) {
			currentSettings = fix.NewSettings
		}

		// Halve chunk if requested.
		if fix.Action == "retry_smaller" && currentChunk.WordCount > 100 {
			halfWords := currentChunk.WordCount / 2
			subChunks := ChunkText(currentChunk.Text, halfWords/2, halfWords)
			if len(subChunks) > 1 {
				currentChunk = subChunks[0] // retry with just the first half
				currentChunk.Index = chunk.Index
			}
		}
	}

	return ChunkResult{
		Index:    chunk.Index,
		FailType: FailBudgetExhausted,
	}, attempts, cost
}

// tryExistingArtifact checks if a previous attempt already produced a passing audio file
// for this chunk with matching settings. Re-probes it and returns without spending TTS credits.
func (o *Orchestrator) tryExistingArtifact(ctx context.Context, job TTSJob, chunk TTSChunk) (ChunkResult, bool) {
	for attempt := 1; attempt <= MaxAttemptsPerChunk; attempt++ {
		path := o.store.ChunkPath(job.RunID, chunk.Index, attempt)
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			continue
		}

		// Verify the artifact was created with the same settings via idempotency key.
		expectedKey := IdempotencyKey(job.RunID, chunk.Index, attempt, job.Settings)
		if o.ledger != nil {
			recorded, err := o.ledger.GetAttemptKey(ctx, job.RunID, chunk.Index, attempt)
			if err == nil && recorded != "" && recorded != expectedKey {
				continue // settings changed, don't reuse
			}
		}

		ffmpeg := job.FFmpegBin
		if ffmpeg == "" {
			ffmpeg = o.ffmpegBin
		}
		metrics, err := ProbeChunkMetrics(ctx, ffmpeg, path)
		if err != nil {
			continue
		}

		qa := QAChunk(metrics, o.th)
		if qa.Pass {
			return ChunkResult{
				Index:     chunk.Index,
				AudioPath: path,
				Metrics:   metrics,
				Pass:      true,
				Attempt:   attempt,
			}, true
		}
	}
	return ChunkResult{}, false
}

func (o *Orchestrator) recordAttempt(ctx context.Context, runID string, chunkIdx, attemptNum, charCount int,
	success bool, cost float64, failType FailType, metrics QAMetrics, settings TTSSettings,
	idemKey, artifactPath string) {
	if o.ledger == nil {
		return
	}
	_ = o.ledger.RecordAttempt(ctx, runID, chunkIdx, attemptNum, charCount, success, cost, failType, metrics, settings, idemKey, artifactPath)
	_ = o.ledger.RecordCost(ctx, runID, chunkIdx, charCount, cost)
}

// assembleChunks concatenates passing chunk audio files into a single WAV with loudnorm.
func (o *Orchestrator) assembleChunks(ctx context.Context, results []ChunkResult, outPath string) error {
	if err := EnsureDir(outPath); err != nil {
		return err
	}

	if len(results) == 1 {
		// Single chunk: just copy/convert with loudnorm.
		return applyLoudnormToOutput(ctx, o.ffmpegBin, results[0].AudioPath, outPath)
	}

	// Build concat file.
	tmpDir := filepath.Dir(outPath)
	concatPath := filepath.Join(tmpDir, "tts_concat.txt")
	defer os.Remove(concatPath)

	var buf strings.Builder
	for _, r := range results {
		abs, _ := filepath.Abs(r.AudioPath)
		fmt.Fprintf(&buf, "file '%s'\n", abs)
	}
	if err := os.WriteFile(concatPath, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("write concat file: %w", err)
	}

	// Step 1: concat chunks into a raw WAV without normalization.
	rawPath := outPath + ".raw.wav"
	defer os.Remove(rawPath)
	cmdConcat := exec.CommandContext(ctx, o.ffmpegBin,
		"-f", "concat", "-safe", "0", "-i", concatPath,
		"-ar", "44100", "-ac", "1",
		"-y", rawPath,
	)
	if out, err := cmdConcat.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg concat: %s: %w", truncate(string(out), 300), err)
	}

	// Step 2: two-pass loudnorm on the full concatenated audio for consistent level.
	return applyLoudnormToOutput(ctx, o.ffmpegBin, rawPath, outPath)
}

// loudnormStats holds the measured values from a loudnorm first pass.
type loudnormStats struct {
	InputI      string `json:"input_i"`
	InputTP     string `json:"input_tp"`
	InputLRA    string `json:"input_lra"`
	InputThresh string `json:"input_thresh"`
	TargetOffset string `json:"target_offset"`
}

// measureLoudnorm runs a first-pass loudnorm analysis and returns measured stats.
func measureLoudnorm(ctx context.Context, ffmpegBin, inputPath string) (*loudnormStats, error) {
	cmd := exec.CommandContext(ctx, ffmpegBin,
		"-i", inputPath,
		"-af", "loudnorm=I=-16:TP=-1.5:LRA=11:print_format=json",
		"-f", "null", "-",
	)
	// ffmpeg writes loudnorm JSON to stderr
	out, _ := cmd.CombinedOutput()
	raw := string(out)

	// Extract JSON block from stderr output
	start := strings.LastIndex(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return nil, fmt.Errorf("loudnorm JSON not found in output")
	}
	var stats loudnormStats
	if err := json.Unmarshal([]byte(raw[start:end+1]), &stats); err != nil {
		return nil, fmt.Errorf("parse loudnorm JSON: %w", err)
	}
	return &stats, nil
}

// twoPassLoudnorm applies accurate two-pass EBU R128 normalization from inputPath to outputPath.
func twoPassLoudnorm(ctx context.Context, ffmpegBin, inputPath, outputPath string, extraArgs ...string) error {
	stats, err := measureLoudnorm(ctx, ffmpegBin, inputPath)
	if err != nil {
		// Fall back to single-pass if measurement fails
		log.Printf("loudnorm measure failed (%v), falling back to single-pass", err)
		args := []string{"-i", inputPath, "-af", "loudnorm=I=-16:TP=-1.5:LRA=11"}
		args = append(args, extraArgs...)
		args = append(args, "-y", outputPath)
		cmd := exec.CommandContext(ctx, ffmpegBin, args...)
		if out, err2 := cmd.CombinedOutput(); err2 != nil {
			return fmt.Errorf("loudnorm fallback: %s: %w", truncate(string(out), 300), err2)
		}
		return nil
	}

	filter := fmt.Sprintf(
		"loudnorm=I=-16:TP=-1.5:LRA=11:measured_I=%s:measured_TP=%s:measured_LRA=%s:measured_thresh=%s:offset=%s:linear=true",
		stats.InputI, stats.InputTP, stats.InputLRA, stats.InputThresh, stats.TargetOffset,
	)
	args := []string{"-i", inputPath, "-af", filter}
	args = append(args, extraArgs...)
	args = append(args, "-y", outputPath)
	cmd := exec.CommandContext(ctx, ffmpegBin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("loudnorm pass2: %s: %w", truncate(string(out), 300), err)
	}
	return nil
}

// applyLoudnorm runs two-pass EBU R128 loudness normalization in-place.
func applyLoudnorm(ctx context.Context, ffmpegBin, filePath string) error {
	tmpPath := filePath + ".loudnorm.tmp"
	defer os.Remove(tmpPath)
	if err := twoPassLoudnorm(ctx, ffmpegBin, filePath, tmpPath); err != nil {
		return fmt.Errorf("loudnorm: %w", err)
	}
	return os.Rename(tmpPath, filePath)
}

// applyLoudnormToOutput converts input to WAV with two-pass loudnorm.
func applyLoudnormToOutput(ctx context.Context, ffmpegBin, inputPath, outputPath string) error {
	return twoPassLoudnorm(ctx, ffmpegBin, inputPath, outputPath, "-ar", "44100", "-ac", "1")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
