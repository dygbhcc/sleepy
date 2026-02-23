package jobs

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"time"

	"sleepy/internal/domain"
	"sleepy/internal/render"
)

func stepRender(ctx context.Context, deps Deps, run *domain.Run, policy Policy) error {
	log.Printf("step_render: rendering video for run %s", run.ID)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	thumbAsset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetThumbnailPNG)
	if err != nil {
		return fmt.Errorf("load thumbnail asset: %w", err)
	}
	audioAsset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetNarrationWAV)
	if err != nil {
		return fmt.Errorf("load narration asset: %w", err)
	}

	// Check idempotency: if audio hasn't changed since last render, skip.
	currentHash, _ := computeFileHash(audioAsset.Path)
	if currentHash != "" && currentHash == run.RenderHash {
		if videoAsset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetVideoMP4); err == nil {
			if info, err := os.Stat(videoAsset.Path); err == nil && info.Size() > 0 {
				log.Printf("step_render: skipping (input hash unchanged: %s)", currentHash[:12])
				return nil
			}
		}
	}

	// Probe audio duration so we can compute fade-out and pass it to Render.
	audioDur, err := render.ProbeDuration(ctx, deps.Render.FFprobeBin, audioAsset.Path)
	if err != nil {
		return fmt.Errorf("ffprobe narration: %w", err)
	}
	fadeStart := math.Max(0, audioDur-deps.Render.FadeOutSec)
	log.Printf("step_render: audioDur=%.1fs  fadeStart=%.1fs  fadeOut=%.1fs",
		audioDur, fadeStart, deps.Render.FadeOutSec)

	outPath := deps.Store.Path(run.ID, "video.mp4")
	if err := render.Render(ctx, deps.Render, thumbAsset.Path, audioAsset.Path, audioDur, outPath); err != nil {
		return fmt.Errorf("ffmpeg render: %w", err)
	}

	if err := deps.DB.InsertAsset(ctx, run.ID, domain.AssetVideoMP4, outPath); err != nil {
		return fmt.Errorf("record video asset: %w", err)
	}

	// Store render hash (keyed on audio input) for idempotency.
	if currentHash != "" {
		_ = deps.DB.UpdateRunHash(ctx, run.ID, "render_hash", currentHash)
	}

	log.Printf("step_render: done → %s", outPath)
	return nil
}
