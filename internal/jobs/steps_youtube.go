package jobs

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sleepy/internal/domain"
	"sleepy/internal/providers/youtube"
	"sleepy/internal/render"
)

const thumbnailsDir = "assets/thumbnails"

func stepYouTube(ctx context.Context, deps Deps, run *domain.Run) error {
	if deps.YouTube == nil {
		log.Printf("step_youtube: skipping (YouTube not configured)")
		return nil
	}

	// Idempotency: already uploaded.
	if run.YouTubeVideoID != "" {
		log.Printf("step_youtube: skipping (already uploaded: %s)", run.YouTubeVideoID)
		return nil
	}

	if !deps.YouTube.HasToken(ctx) {
		log.Printf("step_youtube: skipping (no YouTube token)")
		return nil
	}

	videoAsset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetVideoMP4)
	if err != nil {
		return fmt.Errorf("load video asset: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	// Get actual video duration from the rendered file.
	actualDurSec, err := render.ProbeDuration(ctx, deps.Render.FFprobeBin, videoAsset.Path)
	if err != nil {
		log.Printf("step_youtube: ffprobe duration failed, falling back to DurationMin: %v", err)
		actualDurSec = float64(run.DurationMin) * 60
	}
	actualDurMin := int(math.Round(actualDurSec / 60))

	title := formatYouTubeTitle(run, actualDurMin)
	desc := fmt.Sprintf(`This long-form cosmic narration is designed for deep sleep and quiet nights.
A slow, calm story set in a peaceful universe, with no action, no tension, and no sudden changes.
Simply let the voice and the steady rhythm accompany you as you rest.

Series: %s
Episode: %s
Style: %s`, run.Series, run.Episode, run.Style)
	tags := []string{"sleep", "narration", "relaxation", run.Style, run.Series}

	thumbPath := findThumbnail(run.Style)

	videoID, err := deps.YouTube.Upload(ctx, youtube.UploadRequest{
		FilePath:      videoAsset.Path,
		Title:         title,
		Description:   desc,
		Tags:          tags,
		Privacy:       deps.YouTubePrivacy,
		ThumbnailPath: thumbPath,
	})
	if err != nil {
		return fmt.Errorf("youtube upload: %w", err)
	}

	if err := deps.DB.SetYouTubeVideoID(ctx, run.ID, videoID); err != nil {
		return fmt.Errorf("record youtube video id: %w", err)
	}

	log.Printf("step_youtube: uploaded run %s → youtube.com/watch?v=%s", run.ID, videoID)
	return nil
}

// formatYouTubeTitle builds the YouTube title in the format:
// "Deep Sleep Story | {Style} - {Title} ({duration})"
// e.g. "Deep Sleep Story | Cosmos - Drifting Through the Dark (2.5 Hours)"
// actualMin is the real video duration in minutes (from ffprobe).
func formatYouTubeTitle(run *domain.Run, actualMin int) string {
	dur := formatDuration(actualMin)
	return fmt.Sprintf("Deep Sleep Story | %s - %s (%s)", run.Style, run.Episode, dur)
}

// formatDuration converts minutes to a human-readable duration string.
// e.g. 150 → "2.5 Hours", 60 → "1 Hour", 30 → "30 Minutes"
func formatDuration(minutes int) string {
	if minutes < 60 {
		return fmt.Sprintf("%d Minutes", minutes)
	}
	hours := float64(minutes) / 60.0
	// Whole hours: "2 Hours", "1 Hour"
	if minutes%60 == 0 {
		h := minutes / 60
		if h == 1 {
			return "1 Hour"
		}
		return fmt.Sprintf("%d Hours", h)
	}
	// Fractional: "2.5 Hours", "1.5 Hours"
	return fmt.Sprintf("%.1f Hours", hours)
}

// findThumbnail looks for a thumbnail matching the style name in assets/thumbnails/.
// Matches: cosmos_thumbnail.jpg for style "Cosmos", earthside_thumbnail.jpg for "Earthside", etc.
func findThumbnail(style string) string {
	prefix := strings.ToLower(style)
	entries, err := os.ReadDir(thumbnailsDir)
	if err != nil {
		log.Printf("step_youtube: cannot read thumbnails dir: %v", err)
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasPrefix(name, prefix) {
			path := filepath.Join(thumbnailsDir, e.Name())
			log.Printf("step_youtube: matched thumbnail %s for style %q", path, style)
			return path
		}
	}
	log.Printf("step_youtube: no thumbnail found for style %q", style)
	return ""
}
