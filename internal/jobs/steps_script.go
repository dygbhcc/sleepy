package jobs

import (
	"context"
	"log"
	"strings"

	"sleepy/internal/domain"
	"sleepy/internal/providers/llm"
)

func stepScript(ctx context.Context, deps Deps, run *domain.Run, policy Policy) error {
	log.Printf("step_script: generating for run %s (%s / %s / %s / %dmin, target_words=%d)",
		run.ID, run.Series, run.Episode, run.Style, run.DurationMin, policy.TargetWords)

	result, err := deps.LLM.GenerateScript(ctx, llm.ScriptRequest{
		Series:           run.Series,
		Episode:          run.Episode,
		Style:            run.Style,
		Language:         run.Language,
		DurationMin:      run.DurationMin,
		TargetWords:      policy.TargetWords,
		MinWords:         policy.MinWords,
		MaxWords:         policy.MaxWords,
		Temperature:      policy.LLMTemperature,
		ExtraInstruction: strings.TrimSpace(policy.LLMExtraInstruction),
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

	// Compute and store script hash for idempotency.
	hash, err := computeFileHash(mdPath)
	if err == nil {
		_ = deps.DB.UpdateRunHash(ctx, run.ID, "script_hash", hash)
	}

	// Generate a dynamic title from script content, but only if the user
	// hasn't already provided one.
	if run.Title == "" {
		excerpt := scriptExcerpt(result.Markdown, 500)
		title, titleErr := deps.LLM.GenerateTitle(ctx, llm.TitleRequest{
			ScriptExcerpt: excerpt,
			Series:        run.Series,
			Episode:       run.Episode,
			Style:         run.Style,
			Language:      run.Language,
			DurationMin:   run.DurationMin,
		})
		if titleErr != nil {
			log.Printf("step_script: title generation failed (QA will catch): %v", titleErr)
		} else {
			if err := deps.DB.UpdateRunTitle(ctx, run.ID, title); err != nil {
				log.Printf("step_script: failed to save title: %v", err)
			} else {
				log.Printf("step_script: generated title: %q", title)
			}
		}
	} else {
		log.Printf("step_script: keeping user-provided title: %q", run.Title)
	}

	log.Printf("step_script: done (%d words)", len(strings.Fields(result.Markdown)))
	return nil
}

// scriptExcerpt returns the first n words of a script.
func scriptExcerpt(text string, maxWords int) string {
	words := strings.Fields(text)
	if len(words) <= maxWords {
		return text
	}
	return strings.Join(words[:maxWords], " ")
}

