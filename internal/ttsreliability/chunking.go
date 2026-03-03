package ttsreliability

import (
	"strings"
	"unicode"
)

// ChunkText splits text into chunks of minWords–maxWords, breaking on sentence
// boundaries. It never splits mid-sentence unless a single sentence exceeds
// maxWords, in which case it splits on commas/semicolons.
func ChunkText(text string, minWords, maxWords int) []TTSChunk {
	if minWords <= 0 {
		minWords = DefaultMinWordsPerChunk
	}
	if maxWords <= 0 {
		maxWords = DefaultMaxWordsPerChunk
	}

	sentences := splitSentences(text)
	if len(sentences) == 0 {
		return nil
	}

	var chunks []TTSChunk
	var current []string
	currentWords := 0

	flush := func() {
		if len(current) == 0 {
			return
		}
		joined := strings.Join(current, " ")
		chunks = append(chunks, TTSChunk{
			Index:     len(chunks),
			Text:      joined,
			WordCount: countWords(joined),
		})
		current = nil
		currentWords = 0
	}

	for _, sent := range sentences {
		sent = strings.TrimSpace(sent)
		if sent == "" {
			continue
		}
		wc := countWords(sent)

		// Single sentence exceeds maxWords → split it further.
		if wc > maxWords {
			flush()
			parts := splitLongSentence(sent, maxWords)
			for _, p := range parts {
				pwc := countWords(p)
				chunks = append(chunks, TTSChunk{
					Index:     len(chunks),
					Text:      p,
					WordCount: pwc,
				})
			}
			continue
		}

		// Adding this sentence would exceed maxWords → flush first.
		if currentWords+wc > maxWords && currentWords >= minWords {
			flush()
		}

		current = append(current, sent)
		currentWords += wc
	}
	flush()

	return chunks
}

// splitSentences breaks text into sentences, preserving the terminating
// punctuation with each sentence. Handles ".", "!", "?" followed by whitespace.
func splitSentences(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	var sentences []string
	var current strings.Builder

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		current.WriteRune(runes[i])

		// Sentence-ending punctuation followed by whitespace or end-of-text.
		if runes[i] == '.' || runes[i] == '!' || runes[i] == '?' {
			atEnd := i+1 >= len(runes)
			followedBySpace := !atEnd && unicode.IsSpace(runes[i+1])

			if atEnd || followedBySpace {
				s := strings.TrimSpace(current.String())
				if s != "" {
					sentences = append(sentences, s)
				}
				current.Reset()
			}
		}
	}

	// Remaining text (no trailing punctuation).
	if s := strings.TrimSpace(current.String()); s != "" {
		sentences = append(sentences, s)
	}

	return sentences
}

// splitLongSentence breaks a sentence that exceeds maxWords on commas or
// semicolons. If no such delimiter exists, it splits at word boundaries.
func splitLongSentence(sent string, maxWords int) []string {
	// Try splitting on commas and semicolons first.
	delimiters := []string{"; ", ", "}
	for _, delim := range delimiters {
		parts := strings.Split(sent, delim)
		if len(parts) > 1 {
			return groupParts(parts, delim, maxWords)
		}
	}

	// Last resort: split at word boundaries.
	words := strings.Fields(sent)
	var chunks []string
	for i := 0; i < len(words); i += maxWords {
		end := i + maxWords
		if end > len(words) {
			end = len(words)
		}
		chunks = append(chunks, strings.Join(words[i:end], " "))
	}
	return chunks
}

// groupParts recombines split parts into groups that fit within maxWords.
func groupParts(parts []string, delim string, maxWords int) []string {
	var result []string
	var current strings.Builder
	currentWords := 0

	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		wc := countWords(p)

		if currentWords+wc > maxWords && currentWords > 0 {
			result = append(result, strings.TrimSpace(current.String()))
			current.Reset()
			currentWords = 0
		}

		if current.Len() > 0 {
			current.WriteString(delim)
		}
		current.WriteString(p)
		currentWords += wc

		// Re-add delimiter suffix if it was "; " and not last part.
		_ = i
	}

	if s := strings.TrimSpace(current.String()); s != "" {
		result = append(result, s)
	}
	return result
}

// countWords returns the number of whitespace-separated words in text.
func countWords(text string) int {
	return len(strings.Fields(text))
}
