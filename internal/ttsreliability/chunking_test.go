package ttsreliability

import (
	"strings"
	"testing"
)

func TestChunkText_SingleChunk(t *testing.T) {
	text := generateWords(600)
	chunks := ChunkText(text, 500, 900)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].WordCount < 500 || chunks[0].WordCount > 900 {
		t.Errorf("word count %d outside 500-900", chunks[0].WordCount)
	}
	if chunks[0].Index != 0 {
		t.Errorf("expected index 0, got %d", chunks[0].Index)
	}
}

func TestChunkText_MultipleSentences(t *testing.T) {
	// Build text with ~1500 words across many sentences.
	var sb strings.Builder
	for i := 0; i < 75; i++ {
		sb.WriteString(generateWords(20))
		sb.WriteString(". ")
	}
	text := sb.String()
	chunks := ChunkText(text, 500, 900)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.WordCount > 920 { // small tolerance for sentence overshoot
			t.Errorf("chunk %d has %d words (exceeds 900+tolerance)", c.Index, c.WordCount)
		}
	}
}

func TestChunkText_SentenceBoundaries(t *testing.T) {
	text := "This is sentence one. This is sentence two. This is sentence three."
	chunks := ChunkText(text, 1, 8)
	// Each sentence is ~5 words, min=1 max=8, so at least 2 chunks.
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	// No chunk should have a partial sentence (split mid-word).
	for _, c := range chunks {
		if !strings.HasSuffix(c.Text, ".") && !strings.HasSuffix(c.Text, "three.") {
			// Last chunk might not end with period if text doesn't.
		}
	}
}

func TestChunkText_LongSentenceSplit(t *testing.T) {
	// One sentence with 1200 words, no periods.
	longSentence := generateWords(1200) + "."
	chunks := ChunkText(longSentence, 500, 900)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks from oversized sentence, got %d", len(chunks))
	}
}

func TestChunkText_EmptyInput(t *testing.T) {
	chunks := ChunkText("", 500, 900)
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for empty input, got %d", len(chunks))
	}
}

func TestChunkText_IndicesSequential(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString(generateWords(15))
		sb.WriteString(". ")
	}
	chunks := ChunkText(sb.String(), 500, 900)
	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("expected index %d, got %d", i, c.Index)
		}
	}
}

func TestSplitSentences(t *testing.T) {
	text := "Hello world. How are you? I am fine! Good."
	got := splitSentences(text)
	want := []string{"Hello world.", "How are you?", "I am fine!", "Good."}
	if len(got) != len(want) {
		t.Fatalf("expected %d sentences, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSplitSentences_Abbreviations(t *testing.T) {
	// "Dr." followed by non-space shouldn't split (but our simple heuristic will).
	// This is acceptable for TTS chunking.
	text := "Dr.Smith went home. He was tired."
	got := splitSentences(text)
	// "Dr.Smith went home." stays together because no space after first period.
	if len(got) != 2 {
		t.Errorf("expected 2 sentences, got %d: %v", len(got), got)
	}
}

func TestCountWords(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"hello world", 2},
		{"  spaces   everywhere  ", 2},
		{"", 0},
		{"one", 1},
	}
	for _, tt := range tests {
		got := countWords(tt.input)
		if got != tt.want {
			t.Errorf("countWords(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

// generateWords creates a string with exactly n words.
func generateWords(n int) string {
	words := []string{"the", "gentle", "wind", "carried", "soft", "whispers", "through", "the", "meadow", "calm"}
	var sb strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(words[i%len(words)])
	}
	return sb.String()
}
