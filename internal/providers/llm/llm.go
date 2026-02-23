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
		http: &http.Client{Timeout: 180 * time.Second},
	}
}

// GenerateScript calls the LLM, validates with the sleep-safe QA gate,
// and retries up to 2 times on QA failure before returning an error.
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

	const maxRetries = 2
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Back off between retries to avoid rate limits.
			wait := time.Duration(attempt*20) * time.Second
			log.Printf("llm: waiting %s before retry %d", wait, attempt+1)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		raw, err := c.chatCompletion(ctx, messages, req.Temperature)
		if err != nil {
			// On rate limit (typed TransientError), wait and retry.
			if errs.IsTransient(err) && attempt < maxRetries {
				log.Printf("llm: transient error on attempt %d, will retry", attempt+1)
				continue
			}
			return nil, fmt.Errorf("llm call (attempt %d): %w", attempt+1, err)
		}

		result, err := parseScriptResponse(raw)
		if err != nil {
			return nil, fmt.Errorf("parse llm response: %w", err)
		}

		qa := RunQA(result.Markdown)
		if qa.Pass {
			log.Printf("llm: QA passed on attempt %d", attempt+1)
			return result, nil
		}

		log.Printf("llm: QA failed on attempt %d: %s", attempt+1, strings.Join(qa.Failures, "; "))

		feedback := fmt.Sprintf(
			"The script did not pass the sleep-safety review. Issues found:\n%s\n\n"+
				"Rewrite the entire script, strictly avoiding every listed issue. "+
				"Keep it extremely calm and gentle throughout.",
			strings.Join(qa.Failures, "\n"),
		)
		messages = append(messages,
			chatMsg{Role: "assistant", Content: raw},
			chatMsg{Role: "user", Content: feedback},
		)
	}

	return nil, fmt.Errorf("script failed QA gate after %d retries", maxRetries)
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

func (c *Client) chatCompletion(ctx context.Context, msgs []chatMsg, temperature float64) (string, error) {
	if temperature <= 0 {
		temperature = 0.7
	}
	body, err := json.Marshal(chatReq{
		Model:       c.cfg.Model,
		Messages:    msgs,
		Temperature: temperature,
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

const systemPrompt = `You are a professional sleep narration scriptwriter. You write extremely calm, slow-paced narration for sleep videos. Your prose is gentle, hypnotic, and monotone-friendly. You never include action, tension, conflict, or anything stimulating. Every sentence should feel like a soft exhale.`

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
Target: approximately {{.WordCount}} words ({{.DurationMin}} minutes at ~130 words per minute)

IMPORTANT: Write the ENTIRE script in {{.LangName}}. Every sentence, paragraph, and word must be in {{.LangName}}.

Style guide for "{{.Style}}":
{{.StyleGuide}}

Rules:
- Use present tense throughout
- Average sentence length: 10-18 words
- No exclamation marks
- No ALL CAPS words (except "SSML" in the format separator)
- No dialogue or characters in conflict
- No high-tension words: suddenly, blood, scream, terror, panic, kill, dead, gun, fight, attack, explosion
- Descriptive, sensory imagery only: sight, gentle sounds, soft textures, warmth
- Gradual, dreamlike progression with no plot
- Begin gently and let the imagery soften further as the script continues
- End with a passage that fades into silence and stillness

Output format:
First, output the full script in plain Markdown.
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
		DurationMin, WordCount                       int
	}{req.Series, req.Episode, req.Style, guide, langName, req.DurationMin, wordCount}

	var buf bytes.Buffer
	if err := userTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func parseScriptResponse(raw string) (*ScriptResult, error) {
	parts := strings.SplitN(raw, "---SSML---", 2)
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
