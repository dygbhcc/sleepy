package jobs

import (
	"context"
	"log"
	"strings"
	"time"

	"sleepy/internal/db"
	"sleepy/internal/domain"
	"sleepy/internal/providers/llm"
)

// MaybeShadowReason asynchronously asks the attached LLM reasoner (if any)
// which candidate fix it would have picked for this QA failure, and logs it
// next to the deterministic scorer's actual choice (scorerChoice) for later
// comparison. It is fire-and-forget: it never blocks the caller, never
// retries the run, and never changes what DecideFix already returned. A nil
// reasoner (the default — see SetReasoner) makes this a no-op, so it is
// always safe to call unconditionally right after DecideFix.
func (fe *FixEngine) MaybeShadowReason(dbConn *db.DB, runID string, stage domain.RunStatus, report QAReport, scorerChoice string) {
	if fe.reasoner == nil || dbConn == nil {
		return
	}
	candidates := fe.catalog.CandidatesFor(stage, report.FailType)
	if len(candidates) < 2 {
		return // nothing to reason about — only one (or no) option existed
	}

	var details []string
	for _, chk := range report.Checks {
		if !chk.Pass {
			details = append(details, chk.Name+": "+chk.Details)
		}
	}

	var candInfo []llm.FixCandidateInfo
	for _, c := range candidates {
		candInfo = append(candInfo, llm.FixCandidateInfo{
			ID:          c.ID,
			Description: describeCandidate(c),
		})
	}

	go fe.shadowReason(dbConn, runID, string(stage), string(report.FailType), details, candInfo, scorerChoice)
}

func (fe *FixEngine) shadowReason(dbConn *db.DB, runID, stage, failType string, details []string, candidates []llm.FixCandidateInfo, scorerChoice string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("fix_reasoner: recovered panic: %v", r)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	choice, err := fe.reasoner.ChooseFix(ctx, llm.FixDecisionRequest{
		Stage:      stage,
		FailType:   failType,
		Details:    details,
		Candidates: candidates,
	})

	row := db.FixReasonerLogRow{
		RunID:        runID,
		Stage:        stage,
		FailType:     failType,
		ScorerChoice: scorerChoice,
	}
	if err != nil {
		row.Error = err.Error()
		log.Printf("fix_reasoner: run=%s stage=%s scorer=%s reasoner_error=%v", runID, stage, scorerChoice, err)
	} else {
		row.ReasonerChoice = choice.ChosenID
		row.ReasonerRationale = choice.Rationale
		row.Agreed = choice.ChosenID == scorerChoice
		log.Printf("fix_reasoner: run=%s stage=%s scorer=%s reasoner=%s agreed=%v rationale=%q",
			runID, stage, scorerChoice, choice.ChosenID, row.Agreed, choice.Rationale)
	}

	if insertErr := dbConn.InsertFixReasonerLog(ctx, row); insertErr != nil {
		log.Printf("fix_reasoner: failed to persist shadow log: %v", insertErr)
	}
}

// describeCandidate builds a short human-readable summary of a fix plan for
// the reasoner prompt, from its id and its most informative override.
func describeCandidate(c FixPlan) string {
	if instr, ok := c.Overrides["llm_extra_instruction"].(string); ok && instr != "" {
		return instr
	}
	if cont, ok := c.Overrides["continuation"].(bool); ok && cont {
		return "continue the existing content instead of regenerating"
	}
	return strings.ReplaceAll(c.ID, "_", " ")
}
