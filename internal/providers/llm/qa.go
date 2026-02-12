package llm

import (
	"fmt"
	"log"
	"strings"
	"unicode"
)

// HighTensionWords is the extendable blacklist of words that are too stimulating
// for sleep narration content.
var HighTensionWords = []string{
	"suddenly", "blood", "scream", "terror", "panic", "kill",
	"dead", "gun", "fight", "attack", "explosion", "murder",
	"horror", "violent", "rage", "death", "danger", "crash",
	"destroy", "fear", "nightmare", "war", "weapon", "wound",
	"shriek", "jolt", "alarm",
}

// QAResult holds the outcome of the sleep-safe QA gate.
type QAResult struct {
	Pass     bool
	Failures []string
}

// RunQA runs the deterministic sleep-safe quality gate on the given script text.
// It checks for high-tension words, excessive exclamation marks, ALL CAPS words,
// long average sentence length, and high repetition ratio.
func RunQA(text string) QAResult {
	var failures []string
	lower := strings.ToLower(text)
	words := strings.Fields(lower)

	// 1. High-tension blacklist (whole-word matching to avoid false positives
	//    like "warm" matching "war", "feather" matching "fear", etc.)
	wordSet := make(map[string]struct{}, len(words))
	for _, w := range words {
		clean := strings.Trim(w, ".,;:!?\"'()-[]{}/*#")
		wordSet[clean] = struct{}{}
	}
	for _, banned := range HighTensionWords {
		if _, found := wordSet[banned]; found {
			failures = append(failures, fmt.Sprintf("contains high-tension word: %q", banned))
		}
	}

	// 2. Exclamation marks (max 2)
	if count := strings.Count(text, "!"); count > 2 {
		failures = append(failures, fmt.Sprintf("too many exclamation marks: %d (max 2)", count))
	}

	// 3. ALL CAPS words (skip words shorter than 3 chars and "SSML")
	capsCount := 0
	for _, word := range strings.Fields(text) {
		clean := strings.Trim(word, ".,;:!?\"'()-[]{}/*#")
		if len(clean) < 3 || clean == "SSML" {
			continue
		}
		allUpper := true
		hasLetter := false
		for _, r := range clean {
			if unicode.IsLetter(r) {
				hasLetter = true
				if !unicode.IsUpper(r) {
					allUpper = false
					break
				}
			}
		}
		if hasLetter && allUpper {
			capsCount++
		}
	}
	if capsCount > 3 {
		failures = append(failures, fmt.Sprintf("too many ALL CAPS words: %d (max 3)", capsCount))
	}

	// 4. Average sentence length (max 25 words)
	sentences := splitSentences(text)
	if len(sentences) > 0 {
		totalWords := 0
		for _, s := range sentences {
			totalWords += len(strings.Fields(s))
		}
		avg := float64(totalWords) / float64(len(sentences))
		if avg > 25.0 {
			failures = append(failures, fmt.Sprintf("average sentence too long: %.1f words (max 25)", avg))
		}
	}

	// 5. Repetition ratio — unique/total words (min 0.30)
	if len(words) > 50 {
		unique := make(map[string]struct{}, len(words))
		for _, w := range words {
			unique[strings.Trim(w, ".,;:!?\"'()-[]{}/*#")] = struct{}{}
		}
		ratio := float64(len(unique)) / float64(len(words))
		if ratio < 0.20 {
			failures = append(failures, fmt.Sprintf("high repetition: %.2f unique/total ratio (min 0.20)", ratio))
		}
	}

	result := QAResult{
		Pass:     len(failures) == 0,
		Failures: failures,
	}
	if !result.Pass {
		log.Printf("qa: FAIL — %s", strings.Join(failures, "; "))
	} else {
		log.Printf("qa: PASS (words=%d, sentences=%d)", len(words), len(sentences))
	}
	return result
}

// splitSentences splits text into sentences using newlines and punctuation.
// Each line is treated as a potential sentence boundary, then further split on .!?
func splitSentences(text string) []string {
	var sentences []string

	// Split on any newline first (handles both \n and \n\n).
	lines := strings.Split(text, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "---") {
			continue
		}

		// Within each line, split on sentence-ending punctuation.
		var cur strings.Builder
		found := false
		for _, r := range line {
			cur.WriteRune(r)
			if r == '.' || r == '!' || r == '?' {
				s := strings.TrimSpace(cur.String())
				if len(strings.Fields(s)) > 0 {
					sentences = append(sentences, s)
					found = true
				}
				cur.Reset()
			}
		}
		// If no punctuation in this line, treat the whole line as one sentence.
		if !found {
			if s := strings.TrimSpace(cur.String()); len(strings.Fields(s)) > 0 {
				sentences = append(sentences, s)
			}
		}
	}
	return sentences
}
