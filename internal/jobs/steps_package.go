package jobs

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sleepy/internal/domain"
)

type episodeMetadata struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Series      string   `json:"series"`
	Episode     string   `json:"episode"`
	Style       string   `json:"style"`
	DurationMin int      `json:"duration_min"`
}

func stepPackage(ctx context.Context, deps Deps, run *domain.Run) error {
	log.Printf("step_package: packaging run %s", run.ID)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// 1. Build and store metadata.json (deterministic, no LLM).
	meta := buildMetadata(run)
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	metaPath, err := deps.Store.WriteBytes(run.ID, "metadata.json", metaBytes)
	if err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	if err := deps.DB.InsertAsset(ctx, run.ID, domain.AssetMetadataJSON, metaPath); err != nil {
		return fmt.Errorf("record metadata asset: %w", err)
	}

	// 2. Create episode_pack.zip with all assets.
	zipPath := deps.Store.Path(run.ID, "episode_pack.zip")
	if err := createZip(ctx, deps, run, zipPath); err != nil {
		return fmt.Errorf("create zip: %w", err)
	}
	if err := deps.DB.InsertAsset(ctx, run.ID, domain.AssetEpisodePack, zipPath); err != nil {
		return fmt.Errorf("record zip asset: %w", err)
	}

	log.Printf("step_package: done → %s", zipPath)
	return nil
}

// buildMetadata produces a deterministic metadata.json — no LLM involved.
func buildMetadata(run *domain.Run) episodeMetadata {
	theme := run.Episode
	if len(theme) > 40 {
		theme = theme[:40]
	}

	title := run.Title
	if title == "" {
		title = fmt.Sprintf("Quiet Orbit | %s — %s (Deep Sleep, %d min)",
			run.Series, theme, run.DurationMin)
	}

	description := fmt.Sprintf(`%s — %s

A calm, continuous sleep narration with no action, no tension, no sudden changes.
Designed for deep sleep, relaxation, and peaceful rest.

Style: %s
Duration: ~%d minutes

This episode features gentle, slow-paced narration over soft visuals.
Let yourself drift into deep, restful sleep.`,
		run.Series, run.Episode, run.Style, run.DurationMin)

	tags := []string{
		"sleep", "deep sleep", "sleep narration", "relaxation",
		"calm", "peaceful", "bedtime", "rest", "sleep story",
		"ambient", "no tension", "soothing", "asmr sleep",
	}
	tags = append(tags, strings.ToLower(run.Series), strings.ToLower(run.Style))

	return episodeMetadata{
		Title:       title,
		Description: description,
		Tags:        tags,
		Series:      run.Series,
		Episode:     run.Episode,
		Style:       run.Style,
		DurationMin: run.DurationMin,
	}
}

func createZip(ctx context.Context, deps Deps, run *domain.Run, zipPath string) error {
	items := []struct {
		kind string
		name string
	}{
		{domain.AssetScriptMD, "script.md"},
		{domain.AssetScriptSSML, "script.ssml"},
		{domain.AssetNarrationWAV, "narration.wav"},
		{domain.AssetThumbnailPNG, "thumbnail.png"},
		{domain.AssetVideoMP4, "video.mp4"},
		{domain.AssetMetadataJSON, "metadata.json"},
	}

	f, err := os.Create(zipPath)
	if err != nil {
		return fmt.Errorf("create zip file: %w", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)

	for _, item := range items {
		asset, err := deps.DB.GetAsset(ctx, run.ID, item.kind)
		if err != nil {
			zw.Close()
			return fmt.Errorf("get asset %s: %w", item.kind, err)
		}

		w, err := zw.Create(filepath.Join(run.ID, item.name))
		if err != nil {
			zw.Close()
			return err
		}

		src, err := os.Open(asset.Path)
		if err != nil {
			zw.Close()
			return fmt.Errorf("open %s: %w", asset.Path, err)
		}
		_, cpErr := io.Copy(w, src)
		src.Close()
		if cpErr != nil {
			zw.Close()
			return fmt.Errorf("copy %s into zip: %w", item.name, cpErr)
		}
	}

	return zw.Close()
}
