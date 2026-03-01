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

// Config holds OpenAI-compatible API settings.
type Config struct {
	BaseURL string // e.g. https://api.openai.com/v1
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
}

// ScriptResult contains the generated script in two formats.
type ScriptResult struct {
	Markdown string
	SSML     string
}

// Client wraps the OpenAI-compatible chat completions API.
type Client struct {
	cfg  Config
	http *http.Client
}

// NewClient creates a configured LLM client.
func NewClient(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-4o"
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 600 * time.Second},
	}
}

// GenerateScript calls the LLM and returns the result.
// QA validation is handled externally by the pipeline's qaScript stage
// and fix engine, which provide smarter retries with parameter adjustments.
func (c *Client) GenerateScript(ctx context.Context, req ScriptRequest) (*ScriptResult, error) {
	wordCount := req.DurationMin * 130
	if req.TargetWords > 0 {
		wordCount = req.TargetWords
	}

	userPrompt, err := buildPrompt(req, wordCount)
	if err != nil {
		return nil, fmt.Errorf("build prompt: %w", err)
	}

	sysPrompt := systemPrompt
	if req.ExtraInstruction != "" {
		sysPrompt += "\n\n" + req.ExtraInstruction
	}

	messages := []chatMsg{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userPrompt},
	}

	// Compute a hard max_tokens ceiling to prevent runaway generation.
	// LLM now produces markdown only (no SSML), so budget is ~1.5 tokens/word + headroom.
	capWords := req.MaxWords
	if capWords <= 0 {
		capWords = wordCount
	}
	// Token budget: ~1.5 tokens/word for English, ~2 for Turkish/other.
	// Use 2x to avoid truncating non-English scripts while still capping runaway.
	maxTokens := capWords * 2

	raw, err := c.chatCompletionWithRetry(ctx, messages, completionOpts{
		Temperature:      req.Temperature,
		MaxTokens:        maxTokens,
		FrequencyPenalty: 0.2,
		PresencePenalty:  0.3,
	})
	if err != nil {
		return nil, err
	}

	return parseScriptResponse(raw)
}

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

	sysPrompt := fmt.Sprintf(`You are a YouTube title writer for sleep/relaxation videos.
Write exactly ONE short video title. Rules:
- Maximum 60 characters
- Language: %s
- Calm, gentle, inviting tone
- SEO-friendly (include "sleep" or equivalent in the language)
- No clickbait, no ALL CAPS, no exclamation marks
- No quotes around the title
- Return ONLY the title text, nothing else`, langName)

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
	Model            string    `json:"model"`
	Messages         []chatMsg `json:"messages"`
	Temperature      float64   `json:"temperature"`
	MaxCompletionTokens int    `json:"max_completion_tokens,omitempty"`
	FrequencyPenalty float64   `json:"frequency_penalty,omitempty"`
	PresencePenalty  float64   `json:"presence_penalty,omitempty"`
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
		Model:            c.cfg.Model,
		Messages:         msgs,
		Temperature:      temperature,
		MaxCompletionTokens: opts.MaxTokens,
		FrequencyPenalty: opts.FrequencyPenalty,
		PresencePenalty:  opts.PresencePenalty,
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

// -------- Prompt template --------

const systemPrompt = `You are a sleep narration scriptwriter. You produce calm, slow-paced plain text for sleep videos.

=== ABSOLUTE RULES (violating any = failure) ===

1. BANNED WORDS — never use: suddenly, blood, scream, terror, panic, kill, dead, gun, fight, attack, explosion, horror, nightmare, violent, murder, death, war, battle, weapon, destroy, rage, wound, shriek, jolt, alarm, collapse, danger, fear, crash, void, disappear, fade away, swallow, consume, annihilate, wake up, open your eyes, become alert
2. ZERO exclamation marks. Only use periods, commas, and occasional semicolons.
3. Every sentence must be 8-20 words. No exceptions. Break long sentences with periods.
4. No ALL CAPS words.
5. No titles, headings, markdown, bullet points, numbered lists, SSML, or XML.
6. No ellipsis (...), em-dashes (—), or decorative punctuation.
7. No existential questions, philosophical musings, tension, conflict, or suspense.
8. No wake-up cues or references to waking, alertness, or opening eyes.
9. Plain prose paragraphs separated by blank lines. Nothing else.

=== TONE ===
- Calm, soft, stable, hypnotic, monotone-friendly.
- Imagery must soothe, never startle.
- Every paragraph should feel slower and softer than the previous one.
- Descriptive, sensory imagery only: sight, gentle sounds, soft textures, warmth.
- Gradual, dreamlike progression with no plot.

=== SELF-CHECK ===
Before returning, scan your output for banned words and sentences over 20 words. Fix any violations silently. Return ONLY the clean script.`

var langNames = map[string]string{
	"en": "English",
	"tr": "Turkish",
	"pt": "Portuguese",
	"es": "Spanish",
	"it": "Italian",
}

var userTmpl = template.Must(template.New("user").Parse(`Write a sleep narration script.

Series: {{.Series}}
Episode: {{.Episode}}
Style: {{.Style}}
Language: {{.LangName}}
Style guide: {{.StyleGuide}}

{{if and .MinWords .MaxWords}}=== WORD COUNT (MANDATORY) ===
Target: {{.WordCount}} words.
Minimum: {{.MinWords}} words. Maximum: {{.MaxWords}} words.
You MUST write at least {{.MinWords}} words. Keep writing new paragraphs until you reach {{.WordCount}}.
STOP before exceeding {{.MaxWords}}. Do not stop early. Do not overshoot.
{{else}}Word count: approximately {{.WordCount}} words ({{.DurationMin}} minutes at ~130 words per minute).
Write at least {{.WordCount}} words. Do not stop early.
{{end}}
Write the ENTIRE script in {{.LangName}}.

Additional rules:
- Use present tense throughout.
- Begin gently, let imagery soften further as the script continues.
- End with a passage that fades into silence and stillness.
- Each paragraph must introduce fresh imagery. No verbatim repetition.
- Vary sentence length and vocabulary. Avoid looping patterns.

Output: plain text paragraphs only, separated by blank lines. Nothing else.`))

var styleGuides = map[string]string{
	"Cosmos": "Imagery of deep space, drifting nebulae, distant stars, gentle cosmic winds, " +
		"the silence between galaxies, soft light from faraway suns, slowly turning planets.",
	"Earthside": "Imagery of quiet forests, still lakes, gentle rainfall, moss-covered stones, " +
		"slow rivers, meadows at dusk, soft moonlight on rolling hills.",
	"Myth": "Imagery of enchanted gardens, ancient sleeping libraries, gentle mythical creatures at rest, " +
		"soft lantern light, timeless quiet villages, dreaming mountains.",
}

func buildPrompt(req ScriptRequest, wordCount int) (string, error) {
	guide, ok := styleGuides[req.Style]
	if !ok {
		guide = styleGuides["Cosmos"]
	}
	lang := req.Language
	if lang == "" {
		lang = "en"
	}
	langName, ok := langNames[lang]
	if !ok {
		langName = "English"
	}
	data := struct {
		Series, Episode, Style, StyleGuide, LangName string
		DurationMin, WordCount, MinWords, MaxWords   int
	}{req.Series, req.Episode, req.Style, guide, langName, req.DurationMin, wordCount, req.MinWords, req.MaxWords}

	var buf bytes.Buffer
	if err := userTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func parseScriptResponse(raw string) (*ScriptResult, error) {
	// Strip any SSML section the model may have included despite instructions.
	md := raw
	for _, sep := range []string{"---SSML---", "---\nSSML---", "---\n\nSSML---", "--- SSML ---", "---SSML ---"} {
		if idx := strings.Index(md, sep); idx >= 0 {
			md = md[:idx]
			break
		}
	}
	md = strings.TrimSpace(md)
	if md == "" {
		return nil, fmt.Errorf("empty script in LLM response")
	}

	// Generate SSML from markdown: wrap paragraphs with <break> tags.
	ssml := markdownToSSML(md)
	return &ScriptResult{Markdown: md, SSML: ssml}, nil
}

// markdownToSSML converts plain-text paragraphs into SSML with pauses between them.
func markdownToSSML(md string) string {
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
