package jobs

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"

	"sleepy/internal/domain"
	"sleepy/internal/providers/llm"
)

func stepScript(ctx context.Context, deps Deps, run *domain.Run, policy Policy) error {
	log.Printf("step_script: generating for run %s (%s / %s / %s / %dmin, target_words=%d)",
		run.ID, run.Series, run.Episode, run.Style, run.DurationMin, policy.TargetWords)

	// Check if fix engine requested continuation mode.
	existingScript := loadExistingScriptForContinuation(ctx, deps, run)

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
		ExistingScript:   existingScript,
		BannedPhrases:    policy.BannedPhrases,
	})
	if err != nil {
		return err
	}

	// In continuation mode, merge existing script + new paragraphs.
	// Cap at 2x target words to prevent exponential growth across retries.
	if existingScript != "" {
		merged := existingScript + "\n\n" + result.Markdown
		maxWords := policy.TargetWords * 2
		if maxWords == 0 {
			maxWords = 10000
		}
		words := strings.Fields(merged)
		if len(words) > maxWords {
			merged = strings.Join(words[:maxWords], " ")
			log.Printf("step_script: continuation capped at %d words (limit %d)", maxWords, maxWords)
		}
		result.Markdown = merged
		result.SSML = llm.MarkdownToSSML(result.Markdown)
		log.Printf("step_script: continuation merged (%d total words)",
			len(strings.Fields(result.Markdown)))
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

	// Generate an episode name from script content if the user didn't provide one.
	if !run.TitleLocked {
		excerpt := scriptExcerpt(result.Markdown, 500)
		epName, epErr := deps.LLM.GenerateTitle(ctx, llm.TitleRequest{
			ScriptExcerpt: excerpt,
			Series:        run.Series,
			Episode:       run.Episode,
			Style:         run.Style,
			Language:      run.Language,
			DurationMin:   run.DurationMin,
		})
		if epErr != nil {
			log.Printf("step_script: episode name generation failed: %v", epErr)
		} else {
			if err := deps.DB.UpdateRunEpisode(ctx, run.ID, epName); err != nil {
				log.Printf("step_script: failed to save episode name: %v", err)
			} else {
				log.Printf("step_script: generated episode name: %q", epName)
			}
		}
	} else {
		log.Printf("step_script: episode locked by user, keeping: %q", run.Episode)
	}

	log.Printf("step_script: done (%d words)", len(strings.Fields(result.Markdown)))
	return nil
}

// loadExistingScriptForContinuation checks if the fix engine requested
// continuation mode and loads the existing script.md if available.
// Returns empty string if continuation is not active or script can't be read.
func loadExistingScriptForContinuation(ctx context.Context, deps Deps, run *domain.Run) string {
	if run.PolicyOverridesJSON == "" || run.PolicyOverridesJSON == "{}" {
		return ""
	}
	var overrides map[string]any
	if err := json.Unmarshal([]byte(run.PolicyOverridesJSON), &overrides); err != nil {
		return ""
	}
	cont, _ := overrides["continuation"].(bool)
	if !cont {
		return ""
	}

	asset, err := deps.DB.GetAsset(ctx, run.ID, domain.AssetScriptMD)
	if err != nil {
		log.Printf("step_script: continuation requested but no existing script: %v", err)
		return ""
	}
	data, err := os.ReadFile(asset.Path)
	if err != nil {
		log.Printf("step_script: continuation requested but can't read script: %v", err)
		return ""
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return ""
	}
	log.Printf("step_script: continuation mode — existing script %d words", len(strings.Fields(content)))
	return content
}

// scriptExcerpt returns the first n words of a script.
func scriptExcerpt(text string, maxWords int) string {
	words := strings.Fields(text)
	if len(words) <= maxWords {
		return text
	}
	return strings.Join(words[:maxWords], " ")
}

