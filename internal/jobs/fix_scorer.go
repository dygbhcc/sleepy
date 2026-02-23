package jobs

import (
	"math"
	"sync"
)

// Scoring weights — success rate and speed first; cost only when measured.
const (
	weightSuccessRate = 0.70
	weightAvgAttempts = 0.30
)

// FixScorer maintains in-memory statistics per fix plan ID.
type FixScorer struct {
	mu    sync.RWMutex
	stats map[string]*fixStats
}

type fixStats struct {
	total       int
	successes   int
	sumAttempts int
	sumCost     float64
}

// NewFixScorer creates a scorer pre-loaded with historical outcomes.
func NewFixScorer(outcomes []FixOutcome) *FixScorer {
	fs := &FixScorer{stats: make(map[string]*fixStats)}
	for _, o := range outcomes {
		fs.ingest(o)
	}
	return fs
}

// Record adds a new outcome to the scorer's memory.
func (fs *FixScorer) Record(o FixOutcome) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.ingest(o)
}

func (fs *FixScorer) ingest(o FixOutcome) {
	s, ok := fs.stats[o.FixPlanID]
	if !ok {
		s = &fixStats{}
		fs.stats[o.FixPlanID] = s
	}
	s.total++
	if o.Success {
		s.successes++
	}
	s.sumAttempts += o.AttemptsToPass
	s.sumCost += o.CostEstimate
}

// Best returns the highest-scoring candidate from the list.
// No history → returns first candidate (catalog order = default priority).
// Tie-break: lowest avgAttempts.
func (fs *FixScorer) Best(candidates []FixPlan) FixPlan {
	if len(candidates) == 0 {
		return FixPlan{Action: "needs_review"}
	}
	if len(candidates) == 1 {
		return candidates[0]
	}

	fs.mu.RLock()
	defer fs.mu.RUnlock()

	// Check if we have any history at all.
	hasHistory := false
	for _, c := range candidates {
		if _, ok := fs.stats[c.ID]; ok {
			hasHistory = true
			break
		}
	}
	if !hasHistory {
		return candidates[0]
	}

	bestIdx := 0
	bestScore := math.Inf(-1)
	bestAttempts := math.Inf(1)

	for i, c := range candidates {
		s, ok := fs.stats[c.ID]
		if !ok {
			// No data: assign neutral score (slight penalty vs proven fixes).
			if 0.0 > bestScore {
				bestIdx = i
				bestScore = 0.0
				bestAttempts = 5.0
			}
			continue
		}

		score := fs.score(s)
		avgAttempts := float64(s.sumAttempts) / float64(s.total)

		if score > bestScore || (score == bestScore && avgAttempts < bestAttempts) {
			bestIdx = i
			bestScore = score
			bestAttempts = avgAttempts
		}
	}

	return candidates[bestIdx]
}

func (fs *FixScorer) score(s *fixStats) float64 {
	if s.total == 0 {
		return 0
	}

	successRate := float64(s.successes) / float64(s.total)
	avgAttempts := float64(s.sumAttempts) / float64(s.total)

	attemptScore := 1.0 - math.Min(avgAttempts/5.0, 1.0)

	sc := weightSuccessRate*successRate + weightAvgAttempts*attemptScore

	// Optional cost penalty: only apply when we have cost data.
	if s.sumCost > 0 {
		avgCost := s.sumCost / float64(s.total)
		costPenalty := math.Min(avgCost/0.10, 0.10)
		sc -= costPenalty
	}

	return sc
}
