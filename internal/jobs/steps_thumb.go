package jobs

import (
	"context"
	"fmt"
	"log"

	"sleepy/internal/domain"
)

func stepThumbnail(ctx context.Context, deps Deps, run *domain.Run) error {
	log.Printf("step_thumb: generating thumbnail for run %s", run.ID)

	title := fmt.Sprintf("%s — %s", run.Series, run.Episode)
	outPath := deps.Store.Path(run.ID, "thumbnail.png")

	if err := deps.Image.GenerateThumbnail(ctx, title, run.Style, outPath); err != nil {
		return err
	}

	if err := deps.DB.InsertAsset(ctx, run.ID, domain.AssetThumbnailPNG, outPath); err != nil {
		return err
	}

	log.Printf("step_thumb: done → %s", outPath)
	return nil
}
