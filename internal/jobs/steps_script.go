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
		ExtraInstruction: strings.TrimSpace(policy.LLMExtraInstruction + "\n\n" + defaultSleepGuardrails()),
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

	log.Printf("step_script: done (%d words)", len(strings.Fields(result.Markdown)))
	return nil
}

func defaultSleepGuardrails() string {
	return strings.TrimSpace(`
STRICTLY FORBIDDEN CONTENT:
1) Any language implying danger, threat, urgency, risk, harm, or fear.
2) Any language implying disappearance, annihilation, endings, or being swallowed/consumed.
3) Any existential/philosophical questions (no “what does it mean”, “who are you”, etc.).
4) Any dramatic escalation, conflict, suspense, or intense surprises.
5) Any wake-up instructions or commands directed at the listener.

Examples of forbidden phrases (not exhaustive):
- "the void consumes"
- "you are disappearing"
- "everything ends"
- "nothing remains"
- "darkness swallows"
- "panic"
- "terror"
- "scream"
- "blood"
- "wake up"

Self-correction requirement:
Before returning the final script, scan your own output and rewrite any parts that violate the forbidden content rules.
Return ONLY the final clean script. No analysis, no commentary.`)
}
