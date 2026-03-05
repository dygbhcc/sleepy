package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"text/template"
	"time"

	"sleepy/internal/errs"
)

// ScriptGenerator is the interface consumed by the pipeline.
type ScriptGenerator interface {
	GenerateScript(ctx context.Context, req ScriptRequest) (*ScriptResult, error)
	GenerateTitle(ctx context.Context, req TitleRequest) (string, error)
}

// TitleRequest describes what title to generate.
type TitleRequest struct {
	ScriptExcerpt string // first ~500 words of the script
	Series        string
	Episode       string
	Style         string
	Language      string
	DurationMin   int
}

// Config holds LLM API settings (Gemini or any OpenAI-compatible endpoint).
type Config struct {
	BaseURL string // e.g. https://generativelanguage.googleapis.com/v1beta/openai
	APIKey  string
	Model   string // e.g. gpt-4o
}

// ScriptRequest describes what script to generate.
type ScriptRequest struct {
	Series      string
	Episode     string
	Style       string // Cosmos | Earthside | Myth
	Language    string // en, tr, pt, es, it
	DurationMin int

	// Override fields (set by fix engine; zero values = use defaults).
	TargetWords      int     // explicit word target; 0 = derive from DurationMin
	MinWords         int     // hard minimum word count; 0 = no constraint
	MaxWords         int     // hard maximum word count; 0 = no constraint
	Temperature      float64 // 0 = use default (0.7)
	ExtraInstruction string  // appended to system prompt

	// Continuation mode: if non-empty, the LLM continues from this script
	// instead of generating from scratch. Only the new paragraphs are returned.
	ExistingScript string

	// BannedPhrases is included in the prompt for the model to avoid.
	BannedPhrases []string
}

// ScriptResult contains the generated script in two formats.
type ScriptResult struct {
	Markdown string
	SSML     string
}

// Client wraps an OpenAI-compatible chat completions API (Gemini, OpenAI, etc.).
type Client struct {
	cfg  Config
	http *http.Client
}

// NewClient creates a configured LLM client.
func NewClient(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
	}
	if cfg.Model == "" {
		cfg.Model = "gemini-2.5-flash"
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 600 * time.Second},
	}
}

// -------- Chunked iterative script generation --------

const paragraphsPerChunk = 12

// GenerateScript produces a sleep narration script using chunked iterative generation.
// The total script is split into chunks of 12 paragraphs each. Each chunk is generated
// with a separate LLM call, using the previous chunk's last 2 paragraphs as context.
// In continuation mode (ExistingScript non-empty), already-completed chunks are skipped.
func (c *Client) GenerateScript(ctx context.Context, req ScriptRequest) (*ScriptResult, error) {
	wordCount := req.DurationMin * 130
	if req.TargetWords > 0 {
		wordCount = req.TargetWords
	}

	totalParagraphs := wordCount / 56
	if totalParagraphs < 12 {
		totalParagraphs = 12
	}

	numChunks := (totalParagraphs + paragraphsPerChunk - 1) / paragraphsPerChunk

	log.Printf("llm: plan — %d target words, %d total paragraphs, %d chunks of %d paragraphs",
		wordCount, totalParagraphs, numChunks, paragraphsPerChunk)

	// Resolve language name.
	lang := req.Language
	if lang == "" {
		lang = "en"
	}
	langName, ok := langNames[lang]
	if !ok {
		langName = "English"
	}

	// Resolve style description and imagery sets.
	styleDesc, ok := styleGuides[req.Style]
	if !ok {
		styleDesc = styleGuides["Cosmos"]
	}
	imagery, ok := imagerySets[req.Style]
	if !ok {
		imagery = imagerySets["Cosmos"]
	}

	// Build system prompt.
	sysPrompt := systemPrompt
	if req.ExtraInstruction != "" {
		sysPrompt += "\n\n" + req.ExtraInstruction
	}

	// In continuation mode, count existing paragraphs to skip completed chunks.
	skipChunks := 0
	var previousContext string
	if req.ExistingScript != "" {
		existingParas := splitParagraphs(req.ExistingScript)
		skipChunks = len(existingParas) / paragraphsPerChunk
		if skipChunks >= numChunks {
			// Already have enough paragraphs — nothing to generate.
			md := req.ExistingScript
			return &ScriptResult{Markdown: md, SSML: MarkdownToSSML(md)}, nil
		}
		// Use last 2 paragraphs as context for the next chunk.
		if len(existingParas) >= 2 {
			previousContext = strings.Join(existingParas[len(existingParas)-2:], "\n\n")
		} else if len(existingParas) > 0 {
			previousContext = existingParas[len(existingParas)-1]
		}
		log.Printf("llm: continuation mode — %d existing paragraphs, skipping %d chunks, generating from chunk %d",
			len(existingParas), skipChunks, skipChunks+1)
	}

	// Token budget per chunk.
	maxTokens := paragraphsPerChunk * 120
	if maxTokens < 1500 {
		maxTokens = 1500
	}
	if maxTokens > 4000 {
		maxTokens = 4000
	}

	var accumulated strings.Builder
	for i := skipChunks; i < numChunks; i++ {
		remaining := totalParagraphs - (i * paragraphsPerChunk)
		thisChunkParas := paragraphsPerChunk
		if remaining < thisChunkParas {
			thisChunkParas = remaining
		}
		if thisChunkParas <= 0 {
			break
		}

		// Pick imagery sets for this chunk by rotating through the 10 sets.
		chunkImagery := rotateImagery(imagery, i)

		chunkMinWords := thisChunkParas * 50
		chunkMaxWords := thisChunkParas * 65

		data := chunkData{
			ParagraphCount:   thisChunkParas,
			IsFirstChunk:     i == 0 && req.ExistingScript == "",
			IsLastChunk:      i == numChunks-1,
			Series:           req.Series,
			Episode:          req.Episode,
			LangName:         langName,
			StyleDescription: styleDesc,
			ImagerySets:      chunkImagery,
			PreviousContext:  previousContext,
			ChunkMinWords:    chunkMinWords,
			ChunkMaxWords:    chunkMaxWords,
		}

		userPrompt, err := buildChunkPrompt(data)
		if err != nil {
			return nil, fmt.Errorf("build chunk prompt %d: %w", i+1, err)
		}

		messages := []chatMsg{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: userPrompt},
		}

		log.Printf("llm: chunk %d/%d — generating %d paragraphs (%d-%d words), maxTokens=%d",
			i+1, numChunks, thisChunkParas, chunkMinWords, chunkMaxWords, maxTokens)
		log.Printf("llm: chunk %d/%d prompt:\n[SYSTEM]\n%s\n\n[USER]\n%s",
			i+1, numChunks, truncate(sysPrompt, 500), truncate(userPrompt, 1500))

		raw, err := c.chatCompletionWithRetry(ctx, messages, completionOpts{
			Temperature:      req.Temperature,
			MaxTokens:        maxTokens,
			FrequencyPenalty: 0.3,
			PresencePenalty:  0.4,
		})
		if err != nil {
			return nil, fmt.Errorf("chunk %d/%d: %w", i+1, numChunks, err)
		}

		chunkText := cleanChunkOutput(raw)
		if chunkText == "" {
			return nil, fmt.Errorf("chunk %d/%d: empty response from LLM (raw %d bytes: %q)",
				i+1, numChunks, len(raw), truncate(raw, 200))
		}

		// Update previous context for next chunk.
		chunkParas := splitParagraphs(chunkText)
		if len(chunkParas) >= 2 {
			previousContext = strings.Join(chunkParas[len(chunkParas)-2:], "\n\n")
		} else if len(chunkParas) > 0 {
			previousContext = chunkParas[len(chunkParas)-1]
		}

		if accumulated.Len() > 0 {
			accumulated.WriteString("\n\n")
		}
		accumulated.WriteString(chunkText)

		totalWords := len(strings.Fields(accumulated.String()))
		log.Printf("llm: chunk %d/%d done (%d paragraphs, %d total words so far)",
			i+1, numChunks, len(chunkParas), totalWords)
	}

	md := strings.TrimSpace(accumulated.String())
	if md == "" {
		return nil, fmt.Errorf("empty script after all chunks")
	}

	// Post-process: replace banned words with safe synonyms.
	md = replaceBannedWords(md, req.BannedPhrases)

	ssml := MarkdownToSSML(md)
	return &ScriptResult{Markdown: md, SSML: ssml}, nil
}

// splitParagraphs splits text on blank lines and returns non-empty paragraphs.
func splitParagraphs(text string) []string {
	parts := strings.Split(text, "\n\n")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// rotateImagery returns the imagery sets rotated for the given chunk index.
// Each chunk gets a different starting position in the imagery list.
func rotateImagery(imagery []string, chunkIndex int) []string {
	n := len(imagery)
	if n == 0 {
		return imagery
	}
	start := (chunkIndex * paragraphsPerChunk) % n
	rotated := make([]string, n)
	for i := 0; i < n; i++ {
		rotated[i] = imagery[(start+i)%n]
	}
	return rotated
}

// bannedReplacements maps banned words to safe, calm synonyms.
// Used for post-processing: the model sometimes ignores prompt rules,
// so we replace banned words to guarantee compliance without deleting sentences.
var bannedReplacements = map[string]string{
	"suddenly":    "gently",
	"blood":       "warmth",
	"scream":      "whisper",
	"terror":      "stillness",
	"panic":       "calm",
	"kill":        "rest",
	"dead":        "still",
	"gun":         "breeze",
	"fight":       "peace",
	"attack":      "embrace",
	"explosion":   "shimmer",
	"horror":      "comfort",
	"nightmare":   "dream",
	"violent":     "gentle",
	"murder":      "silence",
	"death":       "rest",
	"war":         "peace",
	"battle":      "harmony",
	"weapon":      "feather",
	"destroy":     "soften",
	"rage":        "calm",
	"wound":       "touch",
	"shriek":      "hush",
	"jolt":        "drift",
	"alarm":       "chime",
	"collapse":    "settle",
	"danger":      "comfort",
	"fear":        "ease",
	"crash":       "hush",
	"void":        "space",
	"disappear":   "linger",
	"swallow":     "cradle",
	"consume":     "embrace",
	"annihilate":  "dissolve",
}

// replaceBannedWords replaces banned words with safe synonyms (case-insensitive,
// whole-word matching). Preserves sentence structure and word count.
func replaceBannedWords(text string, banned []string) string {
	if len(banned) == 0 {
		return text
	}
	replaced := 0
	words := strings.Fields(text)
	for i, w := range words {
		// Strip punctuation for matching but preserve it in output.
		clean := strings.TrimRight(w, ".,;:!?\"')")
		suffix := w[len(clean):]
		prefix := ""
		clean = strings.TrimLeft(clean, "\"'(")
		if len(clean) < len(w)-len(suffix) {
			prefix = w[:len(w)-len(suffix)-len(clean)]
		}

		lower := strings.ToLower(clean)
		for _, b := range banned {
			if lower == strings.ToLower(b) {
				if repl, ok := bannedReplacements[lower]; ok {
					// Preserve original capitalization of first letter.
					if len(clean) > 0 && clean[0] >= 'A' && clean[0] <= 'Z' {
						repl = strings.ToUpper(repl[:1]) + repl[1:]
					}
					words[i] = prefix + repl + suffix
					replaced++
				}
				break
			}
		}
	}
	if replaced > 0 {
		log.Printf("llm: post-process replaced %d banned word(s) with safe synonyms", replaced)
	}
	// Rebuild text preserving paragraph structure.
	return rebuildWithParagraphs(text, words)
}

// rebuildWithParagraphs reconstructs text with replaced words while preserving
// the original paragraph (double-newline) structure.
func rebuildWithParagraphs(original string, words []string) string {
	paragraphs := strings.Split(original, "\n\n")
	wi := 0
	var result []string
	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		n := len(strings.Fields(para))
		if wi+n > len(words) {
			n = len(words) - wi
		}
		result = append(result, strings.Join(words[wi:wi+n], " "))
		wi += n
	}
	return strings.Join(result, "\n\n")
}

// cleanChunkOutput strips any unwanted LLM artifacts from chunk output.
func cleanChunkOutput(raw string) string {
	md := raw
	// Strip any SSML section the model may have included.
	for _, sep := range []string{"---SSML---", "---\nSSML---", "---\n\nSSML---", "--- SSML ---", "---SSML ---"} {
		if idx := strings.Index(md, sep); idx >= 0 {
			md = md[:idx]
			break
		}
	}
	return strings.TrimSpace(md)
}

// -------- Chunk prompt template --------

type chunkData struct {
	ParagraphCount   int
	IsFirstChunk     bool
	IsLastChunk      bool
	Series           string
	Episode          string
	LangName         string
	StyleDescription string
	ImagerySets      []string
	PreviousContext  string
	ChunkMinWords    int
	ChunkMaxWords    int
}

var chunkTmpl = template.Must(template.New("chunk").Funcs(template.FuncMap{
	"add": func(a, b int) int { return a + b },
}).Parse(`CONTENT GOAL:
Write exactly {{.ParagraphCount}} paragraphs.
Each paragraph must contain exactly 4 sentences.
Every sentence must be 8-20 words and end with a period.
This structure is mandatory.
{{if .IsFirstChunk}}
WORD COUNT GOAL:
The total word count MUST be {{.ChunkMinWords}}-{{.ChunkMaxWords}} words. If below {{.ChunkMinWords}}, continue writing. If above {{.ChunkMaxWords}}, stop immediately before exceeding {{.ChunkMaxWords}}
{{else}}
WORD COUNT GOAL:
Continue the script. Write {{.ChunkMinWords}}-{{.ChunkMaxWords}} new words.
Do NOT repeat any existing text.
{{end}}
STYLE:
Series: {{.Series}}
Episode: {{.Episode}}
Language: {{.LangName}}
Present tense only.
{{.StyleDescription}}

VARIETY WITHOUT REPETITION:
Use these imagery sets and rotate through them, never repeating a full sentence:
{{range $i, $img := .ImagerySets}}{{add $i 1}}. {{$img}}
{{end}}
PACING:
{{if .IsFirstChunk}}Begin extremely gentle.{{else}}Continue from where the previous section ended.{{end}}
Each paragraph becomes slightly softer and quieter.
{{if .IsLastChunk}}End with the softest imagery and stillness.{{end}}
{{if .PreviousContext}}
=== PREVIOUS CONTEXT (last 2 paragraphs, DO NOT REPEAT) ===
{{.PreviousContext}}
=== END CONTEXT ===
{{end}}
Output only the {{.ParagraphCount}} new paragraphs. Nothing else.`))

func buildChunkPrompt(data chunkData) (string, error) {
	var buf bytes.Buffer
	if err := chunkTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// -------- System prompt --------

const systemPrompt = `You are a sleep narration scriptwriter. Produce calm, slow-paced plain text for sleep videos.

ABSOLUTE RULES:
1. Never use any banned words: suddenly, blood, scream, terror, panic, kill, dead, gun, fight, attack, explosion, horror, nightmare, violent, murder, death, war, battle, weapon, destroy, rage, wound, shriek, jolt, alarm, collapse, danger, fear, crash, void, disappear, fade away, swallow, consume, annihilate, wake up, open your eyes, become alert.
2. Zero exclamation marks. Use periods, commas, and occasional semicolons only.
3. Every sentence must be 8-20 words. Every sentence ends with a period.
4. No ALL CAPS words.
5. No titles, headings, markdown, bullet points, numbered lists, SSML, or XML.
6. No ellipsis, em-dashes, or decorative punctuation.
7. No existential questions, philosophical musings, tension, conflict, or suspense.
8. No wake-up cues or references to waking, alertness, or opening eyes.
9. Plain prose paragraphs separated by blank lines. Nothing else.
10. No inspirational tone. No motivational language. No transformation narrative. No destiny or purpose language

SELF-CHECK:
Before you answer, scan for banned words and sentence length violations.
Fix silently. Return only the script.`

// -------- Language & style maps --------

var langNames = map[string]string{
	"en": "English",
	"tr": "Turkish",
	"pt": "Portuguese",
	"es": "Spanish",
	"it": "Italian",
}

var styleGuides = map[string]string{
	"Cosmos": "Imagery of deep space, drifting nebulae, distant stars, gentle cosmic winds, " +
		"the silence between galaxies, soft light from faraway suns, slowly turning planets.",
	"Earthside": "Imagery of quiet forests, still lakes, gentle rainfall, moss-covered stones, " +
		"slow rivers, meadows at dusk, soft moonlight on rolling hills.",
	"Myth": "Imagery of enchanted gardens, ancient sleeping libraries, gentle mythical creatures at rest, " +
		"soft lantern light, timeless quiet villages, dreaming mountains.",
}

var imagerySets = map[string][]string{
	"Cosmos": {
		"Soft starlight on distant dust.",
		"Slow moons in wide orbits.",
		"Nebula haze with muted colors.",
		"Quiet planet nightsides and thin atmospheres.",
		"Gentle rings and faint ice grains.",
		"Far suns as warm, steady glow.",
		"Constellation shapes and slow drifting perspective.",
		"Calm cosmic winds as soft movement of light.",
		"Deep space textures: velvet dark, soft gradients, distant shimmer.",
		"Very quiet closing stillness with dim, steady light.",
	},
	"Earthside": {
		"Dew forming on cool morning grass.",
		"Still lake reflecting pale sky.",
		"Moss growing on ancient stones.",
		"Slow river bending through quiet meadow.",
		"Soft rain on broad green leaves.",
		"Fireflies drifting in warm dusk air.",
		"Fog settling into low valleys.",
		"Moonlight pooling on forest floor.",
		"Birch bark peeling in gentle wind.",
		"Snow falling softly on sleeping fields.",
	},
	"Myth": {
		"Lantern glow in an old stone garden.",
		"Sleeping dragon curled near warm embers.",
		"Ancient library with dusty, dreaming books.",
		"Quiet village under a canopy of stars.",
		"Silver thread of a forgotten lullaby.",
		"Gentle giant resting in a flower meadow.",
		"Enchanted well reflecting a calm moon.",
		"Soft chimes in a temple courtyard.",
		"Woven tapestry showing peaceful legends.",
		"Mist over a bridge to quiet lands.",
	},
}

// -------- Title generation --------

// GenerateTitle calls the LLM to produce a short, calm video title based on script content.
func (c *Client) GenerateTitle(ctx context.Context, req TitleRequest) (string, error) {
	lang := req.Language
	if lang == "" {
		lang = "en"
	}
	langName, ok := langNames[lang]
	if !ok {
		langName = "English"
	}

	sysPrompt := fmt.Sprintf(`Generate a short episode name for a sleep narration video.
Rules:
- 2-5 words maximum. Short and evocative.
- Language: %s
- Poetic, calm, imagery-based. Like a place or scene name.
- No generic words like "sleep", "relaxation", "journey", "peaceful".
- No clickbait, no ALL CAPS, no exclamation marks, no quotes.
- Examples: "Rings of Saturn", "The Slow Moons", "Velvet Meadow", "Amber Lanterns"
- Return ONLY the episode name, nothing else.`, langName)

	userPrompt := fmt.Sprintf("Series: %s\nEpisode: %s\nStyle: %s\nDuration: %d minutes\n\nScript excerpt:\n%s",
		req.Series, req.Episode, req.Style, req.DurationMin, req.ScriptExcerpt)

	messages := []chatMsg{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userPrompt},
	}

	raw, err := c.chatCompletionWithRetry(ctx, messages, completionOpts{
		Temperature: 0.7,
		MaxTokens:   100,
	})
	if err != nil {
		return "", fmt.Errorf("generate title: %w", err)
	}

	// Clean up: remove quotes, trim whitespace.
	title := strings.TrimSpace(raw)
	title = strings.Trim(title, "\"'\u201c\u201d\u2018\u2019")
	title = strings.TrimSpace(title)

	return title, nil
}

// -------- OpenAI chat completions --------

// chatCompletionWithRetry wraps chatCompletion with transient-error retries
// (429, 502, 503, 504) so the caller doesn't have to mix transient retries
// with QA retries.
func (c *Client) chatCompletionWithRetry(ctx context.Context, msgs []chatMsg, opts completionOpts) (string, error) {
	const maxTransient = 4
	for i := 0; i < maxTransient; i++ {
		raw, err := c.chatCompletion(ctx, msgs, opts)
		if err == nil {
			return raw, nil
		}
		if !errs.IsTransient(err) || i == maxTransient-1 {
			return "", err
		}
		wait := time.Duration((i+1)*15) * time.Second
		log.Printf("llm: transient error (attempt %d/%d), retrying in %s: %v", i+1, maxTransient, wait, err)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("unreachable")
}

// -------- OpenAI chat completions wire types --------

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Model               string    `json:"model"`
	Messages            []chatMsg `json:"messages"`
	Temperature         float64   `json:"temperature"`
	MaxCompletionTokens int       `json:"max_completion_tokens,omitempty"`
	FrequencyPenalty    float64   `json:"frequency_penalty,omitempty"`
	PresencePenalty     float64   `json:"presence_penalty,omitempty"`
}

type chatResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type completionOpts struct {
	Temperature      float64
	MaxTokens        int
	FrequencyPenalty float64
	PresencePenalty  float64
}

func (c *Client) chatCompletion(ctx context.Context, msgs []chatMsg, opts completionOpts) (string, error) {
	temperature := opts.Temperature
	if temperature <= 0 {
		temperature = 0.7
	}
	body, err := json.Marshal(chatReq{
		Model:               c.cfg.Model,
		Messages:            msgs,
		Temperature:         temperature,
		MaxCompletionTokens: opts.MaxTokens,
		FrequencyPenalty:    opts.FrequencyPenalty,
		PresencePenalty:     opts.PresencePenalty,
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(c.cfg.BaseURL, "/")+"/chat/completions",
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		baseErr := fmt.Errorf("api error (HTTP %d): %s", resp.StatusCode, truncate(string(respBody), 500))
		// 429 can mean short rate-limit (transient) or quota/billing exhausted (permanent).
		// Only treat true quota/billing exhaustion as permanent.
		if resp.StatusCode == 429 {
			body := string(respBody)
			if strings.Contains(body, "insufficient_quota") {
				return "", baseErr // permanent — don't retry
			}
		}
		if resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
			return "", errs.NewTransient("openai", resp.StatusCode, baseErr)
		}
		return "", baseErr
	}

	var cr chatResp
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if cr.Error != nil {
		return "", fmt.Errorf("api error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("empty choices in response")
	}
	return cr.Choices[0].Message.Content, nil
}

// -------- Utilities --------

// MarkdownToSSML converts plain-text paragraphs into SSML with pauses between them.
func MarkdownToSSML(md string) string {
	paragraphs := strings.Split(md, "\n\n")
	var b strings.Builder
	b.WriteString("<speak>\n")
	for i, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if i > 0 {
			b.WriteString("\n<break time=\"1s\"/>\n")
		}
		b.WriteString(p)
	}
	b.WriteString("\n</speak>")
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
