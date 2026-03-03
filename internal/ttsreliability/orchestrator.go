package ttsreliability

import (
	"context"
	"database/sql"
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

	// Assemble chunks into final audio.
	finalPath := job.OutputPath
	if finalPath == "" {
		finalPath = o.store.FinalPath(job.RunID)
	}
	if err := o.assembleChunks(ctx, results, finalPath); err != nil {
		log.Printf("tts-reliability: assembly failed: %v", err)
		return TTSResult{
			FailType:      FailProviderError,
			TotalAttempts: totalAttempts,
			TotalCostUSD:  totalCost,
			Chunks:        results,
			Details:       fmt.Sprintf("assembly: %v", err),
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

	// Concat + re-encode + loudnorm in one pass to final WAV.
	cmd := exec.CommandContext(ctx, o.ffmpegBin,
		"-f", "concat", "-safe", "0", "-i", concatPath,
		"-af", "loudnorm=I=-16:TP=-1.5:LRA=11",
		"-ar", "44100", "-ac", "1",
		"-y", outPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg concat: %s: %w", truncate(string(out), 300), err)
	}

	return nil
}

// applyLoudnorm runs EBU R128 loudness normalization in-place.
func applyLoudnorm(ctx context.Context, ffmpegBin, filePath string) error {
	tmpPath := filePath + ".loudnorm.tmp"
	defer os.Remove(tmpPath)

	cmd := exec.CommandContext(ctx, ffmpegBin,
		"-i", filePath,
		"-af", "loudnorm=I=-16:TP=-1.5:LRA=11",
		"-y", tmpPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("loudnorm: %s: %w", truncate(string(out), 300), err)
	}
	return os.Rename(tmpPath, filePath)
}

// applyLoudnormToOutput converts input to WAV with loudnorm.
func applyLoudnormToOutput(ctx context.Context, ffmpegBin, inputPath, outputPath string) error {
	cmd := exec.CommandContext(ctx, ffmpegBin,
		"-i", inputPath,
		"-af", "loudnorm=I=-16:TP=-1.5:LRA=11",
		"-ar", "44100", "-ac", "1",
		"-y", outputPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("loudnorm to output: %s: %w", truncate(string(out), 300), err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
