package jobs

import (
	"context"
	"fmt"
	"log"
	"time"

	"sleepy/internal/domain"
	"sleepy/internal/providers/youtube"
)

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

	videoID, err := deps.YouTube.Upload(ctx, youtube.UploadRequest{
		FilePath:    videoAsset.Path,
		Title:       title,
		Description: desc,
		Tags:        tags,
		Privacy:     deps.YouTubePrivacy,
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
