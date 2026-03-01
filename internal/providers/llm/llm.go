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
	// The SSML section roughly doubles the markdown, and ~1.3 tokens per word.
	// Use MaxWords if set, otherwise estimate from wordCount.
	capWords := req.MaxWords
	if capWords <= 0 {
		capWords = wordCount
	}
	maxTokens := capWords * 3 // markdown + SSML + headroom

	raw, err := c.chatCompletionWithRetry(ctx, messages, req.Temperature, maxTokens)
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

	raw, err := c.chatCompletionWithRetry(ctx, messages, 0.7, 100)
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
func (c *Client) chatCompletionWithRetry(ctx context.Context, msgs []chatMsg, temperature float64, maxTokens int) (string, error) {
	const maxTransient = 4
	for i := 0; i < maxTransient; i++ {
		raw, err := c.chatCompletion(ctx, msgs, temperature, maxTokens)
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
	Model       string    `json:"model"`
	Messages    []chatMsg `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
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

func (c *Client) chatCompletion(ctx context.Context, msgs []chatMsg, temperature float64, maxTokens int) (string, error) {
	if temperature <= 0 {
		temperature = 0.7
	}
	body, err := json.Marshal(chatReq{
		Model:       c.cfg.Model,
		Messages:    msgs,
		Temperature: temperature,
		MaxTokens:   maxTokens,
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

const systemPrompt = `You are a professional sleep narration scriptwriter. You write extremely calm, slow-paced narration for sleep videos. Your prose is gentle, hypnotic, and monotone-friendly. You never include action, tension, conflict, or anything stimulating. Every sentence should feel like a soft exhale.

STRICTLY FORBIDDEN CONTENT:

1. Any language implying danger, threat, urgency, or risk.
2. Any language implying disappearance, annihilation, or endings.
3. Any existential or philosophical questioning.
4. Any dramatic emotional escalation.
5. Any sudden transitions or intensity spikes.
6. Any wake-up instructions, alertness cues, or references to waking.
7. Any mention of violence, weapons, combat, or physical harm.
8. Any reference to blood, death, killing, or destruction.

Examples of forbidden phrases (not exhaustive):
- "the void consumes"
- "you disappear"
- "everything ends"
- "nothing remains"
- "what does it mean"
- "darkness swallows"
- "you are fading away"
- "danger" / "collapse" / "falling into nothing"
- "suddenly" / "jolt" / "alarm"
- "open your eyes" / "wake up" / "become alert"

If any forbidden language appears in your draft, silently correct it before returning the final script. Return only the final clean version.

Tone constraints:
- Calm, soft, stable.
- No tension, no contrast spikes, no conflict.
- Imagery must soothe, never startle.
- Every paragraph should feel slower and softer than the one before.`

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
{{if and .MinWords .MaxWords}}Hard range: {{.MinWords}}–{{.MaxWords}} words.
Target: {{.WordCount}} words.
HARD RULE:
- Do NOT exceed {{.MaxWords}} words.
- Do NOT go below {{.MinWords}} words.
- When within range, STOP immediately.
- Do not add extra sections after reaching range.
{{else}}Target: approximately {{.WordCount}} words ({{.DurationMin}} minutes at ~130 words per minute)
{{end}}
IMPORTANT: Write the ENTIRE script in {{.LangName}}. Every sentence, paragraph, and word must be in {{.LangName}}.

Style guide for "{{.Style}}":
{{.StyleGuide}}

Rules:
- Use present tense throughout
- Average sentence length: 10-18 words
- No exclamation marks
- No ALL CAPS words (except "SSML" in the format separator)
- No dialogue or characters in conflict
- NEVER use any of these banned words or stems: suddenly, blood, scream, terror, panic, kill, dead, gun, fight, attack, explosion, horror, nightmare, violent, murder, death, war, battle, weapon, destroy, rage, wound, shriek, jolt, alarm, collapse, danger, fear, crash
- Never include wake-up cues ("open your eyes", "wake up", "become alert")
- Descriptive, sensory imagery only: sight, gentle sounds, soft textures, warmth
- Gradual, dreamlike progression with no plot
- Begin gently and let the imagery soften further as the script continues
- End with a passage that fades into silence and stillness
- Avoid verbatim repetition of sentences or paragraphs
- Do not reuse full sentences; repetition must be subtle variation, not duplication
- Maintain variety in imagery and phrasing; avoid looping patterns

Formatting rules (STRICT):
- Do NOT include any title, heading, or episode name in the script body
- Do NOT use markdown headers (#, ##, **bold**, etc.)
- Do NOT use bullet points, numbered lists, or special formatting
- Plain prose paragraphs only, separated by blank lines
- No ellipsis (...), em-dashes (—), or decorative punctuation
- Use only basic punctuation: periods, commas, and occasional semicolons

Output format:
First, output the script as plain prose paragraphs (no titles, no markdown).
Then output a line containing exactly: ---SSML---
Then output the same script wrapped in SSML with <break time="1s"/> tags between paragraphs. Wrap the entire SSML section in <speak> tags.`))

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
	// Normalize separator variations the LLM may produce:
	// "---\nSSML---", "---\n\nSSML---", "---SSML---" etc.
	normalized := raw
	for _, sep := range []string{"---\n\nSSML---", "---\nSSML---", "--- SSML ---", "---SSML ---"} {
		if strings.Contains(normalized, sep) {
			normalized = strings.Replace(normalized, sep, "---SSML---", 1)
			break
		}
	}
	parts := strings.SplitN(normalized, "---SSML---", 2)
	md := strings.TrimSpace(parts[0])
	if md == "" {
		return nil, fmt.Errorf("empty markdown section in LLM response")
	}

	ssml := ""
	if len(parts) == 2 {
		ssml = strings.TrimSpace(parts[1])
	}
	// Fallback: wrap markdown in basic SSML if the model didn't produce one.
	if ssml == "" {
		ssml = "<speak>\n" + md + "\n</speak>"
	}
	return &ScriptResult{Markdown: md, SSML: ssml}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
