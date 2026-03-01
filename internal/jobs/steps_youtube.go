package jobs

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sleepy/internal/domain"
	"sleepy/internal/providers/youtube"
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

	title := fmt.Sprintf("%s - %s", run.Series, run.Episode)
	desc := fmt.Sprintf("A gentle sleep narration.\n\nSeries: %s\nEpisode: %s\nStyle: %s",
		run.Series, run.Episode, run.Style)
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
