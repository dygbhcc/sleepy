package jobs

import (
	"context"
	"log"
	"strings"

	"sleepy/internal/domain"
	"sleepy/internal/providers/llm"
)

func stepScript(ctx context.Context, deps Deps, run *domain.Run) error {
	log.Printf("step_script: generating for run %s (%s / %s / %s / %dmin)",
		run.ID, run.Series, run.Episode, run.Style, run.DurationMin)

	result, err := deps.LLM.GenerateScript(ctx, llm.ScriptRequest{
		Series:      run.Series,
		Episode:     run.Episode,
		Style:       run.Style,
		Language:    run.Language,
		DurationMin: run.DurationMin,
	})
	if err != nil {
		return err
	}

	mdPath, err := deps.Store.WriteBytes(run.ID, "script.md", []byte(result.Markdown))
	if err != nil {
		return err
	}
	if err := deps.DB.InsertAsset(ctx, run.ID, domain.AssetScriptMD, mdPath); err != nil {
		return err
	}

	ssmlPath, err := deps.Store.WriteBytes(run.ID, "script.ssml", []byte(result.SSML))
	if err != nil {
		return err
	}
	if err := deps.DB.InsertAsset(ctx, run.ID, domain.AssetScriptSSML, ssmlPath); err != nil {
		return err
	}

	log.Printf("step_script: done (%d words)", len(strings.Fields(result.Markdown)))
	return nil
}
