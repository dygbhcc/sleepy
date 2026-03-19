package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"sleepy/internal/domain"
	"sleepy/internal/providers/broll"
	"sleepy/internal/providers/llm"
	"sleepy/internal/render"
)

// stepReelsScript generates a short-form voiceover script for a lifestyle Reel.
func stepReelsScript(ctx context.Context, deps Deps, run *domain.Run) error {
	log.Printf("step_reels_script: generating for run %s (topic=%s, %ds)",
		run.ID, run.Episode, run.DurationSec)

	durSec := run.DurationSec
	if durSec <= 0 {
		durSec = 60
	}

	result, err := deps.LLM.GenerateReelsScript(ctx, llm.ReelsScriptRequest{
		Topic:       run.Episode,
		Category:    run.Style,
		DurationSec: durSec,
		Language:    run.Language,
	})
	if err != nil {
		return fmt.Errorf("generate reels script: %w", err)
	}

	// Save voiceover as script.md (reuses existing asset pipeline).
	mdPath, err := deps.Store.WriteBytes(run.ID, "script.md", []byte(result.Voiceover))
	if err != nil {
		return err
	}
	if err := deps.DB.InsertAsset(ctx, run.ID, domain.AssetScriptMD, mdPath); err != nil {
		return err
	}

	// Save SSML version for TTS.
	ssml := llm.MarkdownToSSML(result.Voiceover)
	ssmlPath, err := deps.Store.WriteBytes(run.ID, "script.ssml", []byte(ssml))
	if err != nil {
		return err
	}
	if err := deps.DB.InsertAsset(ctx, run.ID, domain.AssetScriptSSML, ssmlPath); err != nil {
		return err
	}

	// Save caption + hashtags as metadata.
	meta := map[string]any{
		"caption":  result.Caption,
		"hashtags": result.Hashtags,
		"topic":    run.Episode,
		"category": run.Style,
	}
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	metaPath, err := deps.Store.WriteBytes(run.ID, "reels_meta.json", metaJSON)
	if err != nil {
		return err
	}
	if err := deps.DB.InsertAsset(ctx, run.ID, domain.AssetMetadataJSON, metaPath); err != nil {
		return err
	}

	hash, err := computeFileHash(mdPath)
	if err == nil {
		_ = deps.DB.UpdateRunHash(ctx, run.ID, "script_hash", hash)
	}

	log.Printf("step_reels_script: done (%d words, %d hashtags)",
		len(strings.Fields(result.Voiceover)), len(result.Hashtags))
	return nil
}

// stepReelsBRoll downloads B-roll clips from Pexels based on the topic.
func stepReelsBRoll(ctx context.Context, deps Deps, run *domain.Run) error {
	log.Printf("step_reels_broll: downloading B-roll for run %s", run.ID)

	if deps.BRoll == nil {
		return fmt.Errorf("Pexels API key not configured")
	}

	// Use topic/category as search query.
	query := run.Episode + " " + run.Style
	videos, err := deps.BRoll.SearchVideos(ctx, query, 5)
	if err != nil {
		return fmt.Errorf("search B-roll: %w", err)
	}
	if len(videos) == 0 {
		// Fallback to just the category.
		videos, err = deps.BRoll.SearchVideos(ctx, run.Style, 5)
		if err != nil || len(videos) == 0 {
			return fmt.Errorf("no B-roll clips found for %q", query)
		}
	}

	// Download clips.
	var downloaded []broll.VideoResult
	for i, v := range videos {
		outPath := deps.Store.Path(run.ID, fmt.Sprintf("broll_%d.mp4", i))
		log.Printf("step_reels_broll: downloading clip %d/%d (%ds)", i+1, len(videos), v.Duration)
		if err := deps.BRoll.DownloadClip(ctx, v.DownloadURL, outPath); err != nil {
			log.Printf("step_reels_broll: clip %d download failed: %v", i, err)
			continue
		}
		v.DownloadURL = outPath // replace URL with local path
		downloaded = append(downloaded, v)
	}

	if len(downloaded) == 0 {
		return fmt.Errorf("all B-roll downloads failed")
	}

	// Save B-roll manifest as JSON asset.
	manifest, _ := json.MarshalIndent(downloaded, "", "  ")
	manifestPath, err := deps.Store.WriteBytes(run.ID, "broll.json", manifest)
	if err != nil {
		return err
	}
	if err := deps.DB.InsertAsset(ctx, run.ID, domain.AssetBRollJSON, manifestPath); err != nil {
		return err
	}

	// Use first frame of first clip as thumbnail.
	// (The render step will handle the actual thumbnail extraction)

	log.Printf("step_reels_broll: downloaded %d clips", len(downloaded))
	return nil
}

// stepReelsRender composes the final 9:16 Reel video from B-roll + voiceover.
func stepReelsRender(ctx context.Context, deps Deps, run *domain.Run) error {
	log.Printf("step_reels_render: rendering Reel for run %s", run.ID)

	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	// Load B-roll manifest.
	brollAsset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetBRollJSON)
	if err != nil {
		return fmt.Errorf("load B-roll manifest: %w", err)
	}
	brollData, err := os.ReadFile(brollAsset.Path)
	if err != nil {
		return fmt.Errorf("read B-roll manifest: %w", err)
	}
	var brollVideos []broll.VideoResult
	if err := json.Unmarshal(brollData, &brollVideos); err != nil {
		return fmt.Errorf("parse B-roll manifest: %w", err)
	}

	// Convert to render clips.
	var clips []render.BRollClip
	for _, v := range brollVideos {
		clips = append(clips, render.BRollClip{
			Path:     v.DownloadURL, // local path (was replaced during download)
			Duration: v.Duration,
		})
	}

	// Load voiceover audio.
	audioAsset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetNarrationWAV)
	if err != nil {
		return fmt.Errorf("load narration asset: %w", err)
	}

	// Probe audio duration.
	audioDur, err := render.ProbeDuration(ctx, deps.Render.FFprobeBin, audioAsset.Path)
	if err != nil {
		return fmt.Errorf("probe narration: %w", err)
	}

	outPath := deps.Store.Path(run.ID, "reels.mp4")
	tmpPath := outPath + ".tmp"

	cfg := render.ReelsConfig{
		FFmpegBin:  deps.Render.FFmpegBin,
		FFprobeBin: deps.Render.FFprobeBin,
		MusicPath:  deps.Render.MusicPath,
		MusicVol:   0.3,
	}

	if err := render.RenderReels(ctx, cfg, clips, audioAsset.Path, audioDur, tmpPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("render reels: %w", err)
	}

	// Validate output.
	info, err := os.Stat(tmpPath)
	if err != nil || info.Size() < 1024 {
		os.Remove(tmpPath)
		return fmt.Errorf("rendered Reel missing or too small")
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("commit rendered Reel: %w", err)
	}

	if err := deps.DB.InsertAsset(ctx, run.ID, domain.AssetReelsMP4, outPath); err != nil {
		return fmt.Errorf("record reels asset: %w", err)
	}

	log.Printf("step_reels_render: done → %s (%.0fs)", outPath, audioDur)
	return nil
}
