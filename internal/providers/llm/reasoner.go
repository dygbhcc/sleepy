package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// FixCandidateInfo is a short, human-readable description of one candidate
// fix plan, built by the caller (internal/jobs) from its FixPlan/FixCatalog.
type FixCandidateInfo struct {
	ID          string // catalog key, e.g. "script_wc_low_continue"
	Description string // one-line summary of what this fix does
}

// FixDecisionRequest describes an ambiguous QA failure and the candidate
// fixes the deterministic scorer is choosing between.
type FixDecisionRequest struct {
	Stage      string
	FailType   string
	Details    []string // human-readable QA check failure details
	Candidates []FixCandidateInfo
}

// FixChoice is the reasoner's pick among the candidates, with a short
// rationale. Advisory only — callers decide whether to act on it or just
// log it for comparison (shadow mode).
type FixChoice struct {
	ChosenID  string
	Rationale string
}

// ChooseFix asks the model to pick exactly one candidate id and explain why,
// as compact JSON. Kept intentionally cheap: low max tokens, low temperature,
// closed-set output. Any parse/validation failure is returned as an error —
// callers should treat that as "no opinion" and keep their own logic
// unchanged; this method never invents a fix plan of its own.
func (c *Client) ChooseFix(ctx context.Context, req FixDecisionRequest) (FixChoice, error) {
	if len(req.Candidates) == 0 {
		return FixChoice{}, fmt.Errorf("choose fix: no candidates")
	}

	var ids []string
	var lines []string
	for _, cand := range req.Candidates {
		ids = append(ids, cand.ID)
		lines = append(lines, fmt.Sprintf("- %s: %s", cand.ID, cand.Description))
	}

	sysPrompt := fmt.Sprintf(`You help pick a corrective strategy for a failed automated
QA check in a video production pipeline. You will be given the failure context and a
fixed list of candidate strategy ids. Pick exactly ONE id from the list — never invent
a new one — and give a one-sentence reason.

Valid ids: %s

Respond with ONLY compact JSON, no markdown, no extra text:
{"chosen_id": "<one id from the list>", "rationale": "<one short sentence>"}`,
		strings.Join(ids, ", "))

	userPrompt := fmt.Sprintf("Stage: %s\nFailure type: %s\nQA details:\n%s\n\nCandidates:\n%s",
		req.Stage, req.FailType, strings.Join(req.Details, "\n"), strings.Join(lines, "\n"))

	messages := []chatMsg{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userPrompt},
	}

	raw, err := c.chatCompletionWithRetry(ctx, messages, completionOpts{
		Temperature: 0.2,
		MaxTokens:   150,
	})
	if err != nil {
		return FixChoice{}, fmt.Errorf("choose fix: %w", err)
	}

	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	var parsed struct {
		ChosenID  string `json:"chosen_id"`
		Rationale string `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return FixChoice{}, fmt.Errorf("choose fix: parse response %q: %w", truncate(cleaned, 200), err)
	}

	valid := false
	for _, id := range ids {
		if id == parsed.ChosenID {
			valid = true
			break
		}
	}
	if !valid {
		return FixChoice{}, fmt.Errorf("choose fix: model chose unknown id %q", parsed.ChosenID)
	}

	return FixChoice{ChosenID: parsed.ChosenID, Rationale: strings.TrimSpace(parsed.Rationale)}, nil
}
