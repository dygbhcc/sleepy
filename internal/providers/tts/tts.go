package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"sleepy/internal/errs"
)

// Config holds ElevenLabs API settings.
type Config struct {
	APIKey    string
	VoiceID   string
	ModelID   string  // e.g. "eleven_multilingual_v2"; defaults to "eleven_monolingual_v1"
	Speed     float64 // speech speed: 0.25–4.0; default 0.80 (calm, slow for sleep)
	Normalize bool    // when true, apply EBU R128 loudnorm via ffmpeg; default ON for MVP
	FFmpegBin string  // path to ffmpeg binary for MP3→WAV conversion + optional normalization
}

// Client wraps the ElevenLabs TTS REST API.
type Client struct {
	cfg  Config
	http *http.Client
}

// NewClient creates a configured TTS client.
func NewClient(cfg Config) *Client {
	if cfg.FFmpegBin == "" {
		cfg.FFmpegBin = "ffmpeg"
	}
	if cfg.ModelID == "" {
		cfg.ModelID = "eleven_monolingual_v1"
	}
	if cfg.Speed <= 0 {
		cfg.Speed = 0.50
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 300 * time.Second},
	}
}

// SynthesizeWithOpts generates speech with per-call overrides from the fix engine.
func (c *Client) SynthesizeWithOpts(ctx context.Context, text string, outPath string, speedFactor, stability, similarityBoost float64) error {
	chunks := splitTextForTTS(text, maxCharsPerRequest)
	if len(chunks) == 1 {
		mp3Path := outPath + ".tmp.mp3"
		if err := c.callAPIWithOpts(ctx, chunks[0], mp3Path, speedFactor, stability, similarityBoost); err != nil {
			return fmt.Errorf("elevenlabs api: %w", err)
		}
		defer os.Remove(mp3Path)
		if c.cfg.Normalize {
			return c.convertAndNormalize(ctx, mp3Path, outPath)
		}
		return c.convertToWAV(ctx, mp3Path, outPath)
	}

	log.Printf("tts: text too long (%d chars), splitting into %d chunks", len(text), len(chunks))
	return c.synthesizeChunked(ctx, chunks, outPath, func(ctx context.Context, chunk, path string) error {
		return c.callAPIWithOpts(ctx, chunk, path, speedFactor, stability, similarityBoost)
	})
}

func (c *Client) callAPIWithOpts(ctx context.Context, text, outPath string, speedFactor, stability, similarityBoost float64) error {
	speed := c.cfg.Speed
	if speedFactor > 0 {
		speed *= speedFactor
	}
	stab := 0.80
	if stability > 0 {
		stab = stability
	}
	simBoost := 0.85
	if similarityBoost > 0 {
		simBoost = similarityBoost
	}

	body, err := json.Marshal(ttsReq{
		Text:    text,
		ModelID: c.cfg.ModelID,
		Speed:   speed,
		VoiceSettings: voiceSettings{
			Stability:       stab,
			SimilarityBoost: simBoost,
			SpeakerBoost:    true,
		},
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf(
		"https://api.elevenlabs.io/v1/text-to-speech/%s?output_format=mp3_44100_128",
		c.cfg.VoiceID,
	)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("xi-api-key", c.cfg.APIKey)
	req.Header.Set("Accept", "audio/mpeg")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		baseErr := fmt.Errorf("elevenlabs HTTP %d: %s", resp.StatusCode, truncateBytes(b, 500))
		if resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
			return errs.NewTransient("elevenlabs", resp.StatusCode, baseErr)
		}
		return baseErr
	}

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("download audio: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("elevenlabs returned empty audio body")
	}
	return nil
}

// Synthesize sends text (plain or SSML) to ElevenLabs, downloads MP3,
// post-processes to 44100 Hz mono WAV. When Config.Normalize is true,
// applies EBU R128 loudnorm (loudnorm=I=-16:TP=-1.5:LRA=11).
// Long texts are automatically split into chunks under 10,000 chars.
func (c *Client) Synthesize(ctx context.Context, text string, outPath string) error {
	chunks := splitTextForTTS(text, maxCharsPerRequest)
	if len(chunks) == 1 {
		mp3Path := outPath + ".tmp.mp3"
		if err := c.callAPI(ctx, chunks[0], mp3Path); err != nil {
			return fmt.Errorf("elevenlabs api: %w", err)
		}
		defer os.Remove(mp3Path)
		if c.cfg.Normalize {
			return c.convertAndNormalize(ctx, mp3Path, outPath)
		}
		return c.convertToWAV(ctx, mp3Path, outPath)
	}

	log.Printf("tts: text too long (%d chars), splitting into %d chunks", len(text), len(chunks))
	return c.synthesizeChunked(ctx, chunks, outPath, func(ctx context.Context, chunk, path string) error {
		return c.callAPI(ctx, chunk, path)
	})
}

// ---- ElevenLabs wire types ----

type ttsReq struct {
	Text          string        `json:"text"`
	ModelID       string        `json:"model_id"`
	Speed         float64       `json:"speed,omitempty"`
	VoiceSettings voiceSettings `json:"voice_settings"`
}

type voiceSettings struct {
	Stability       float64 `json:"stability"`
	SimilarityBoost float64 `json:"similarity_boost"`
	SpeakerBoost    bool    `json:"use_speaker_boost"`
}

func (c *Client) callAPI(ctx context.Context, text, outPath string) error {
	body, err := json.Marshal(ttsReq{
		Text:    text,
		ModelID: c.cfg.ModelID,
		Speed:   c.cfg.Speed,
		VoiceSettings: voiceSettings{
			Stability:       0.80,
			SimilarityBoost: 0.85,
			SpeakerBoost:    true,
		},
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf(
		"https://api.elevenlabs.io/v1/text-to-speech/%s?output_format=mp3_44100_128",
		c.cfg.VoiceID,
	)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("xi-api-key", c.cfg.APIKey)
	req.Header.Set("Accept", "audio/mpeg")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		baseErr := fmt.Errorf("elevenlabs HTTP %d: %s", resp.StatusCode, truncateBytes(b, 500))
		if resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
			return errs.NewTransient("elevenlabs", resp.StatusCode, baseErr)
		}
		return baseErr
	}

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("download audio: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("elevenlabs returned empty audio body")
	}
	return nil
}

// convertAndNormalize converts MP3 to WAV with EBU R128 loudness normalization.
func (c *Client) convertAndNormalize(ctx context.Context, mp3Path, wavPath string) error {
	cmd := exec.CommandContext(ctx, c.cfg.FFmpegBin,
		"-i", mp3Path,
		"-af", "loudnorm=I=-16:TP=-1.5:LRA=11",
		"-ar", "44100",
		"-ac", "1",
		"-y", wavPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg loudnorm: %s: %w", truncateBytes(out, 300), err)
	}
	return nil
}

// convertToWAV converts MP3 to WAV without normalization.
func (c *Client) convertToWAV(ctx context.Context, mp3Path, wavPath string) error {
	cmd := exec.CommandContext(ctx, c.cfg.FFmpegBin,
		"-i", mp3Path,
		"-ar", "44100",
		"-ac", "1",
		"-y", wavPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg convert: %s: %w", truncateBytes(out, 300), err)
	}
	return nil
}

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// maxCharsPerRequest is the ElevenLabs text limit (10,000 chars).
const maxCharsPerRequest = 9500 // leave some headroom

// splitTextForTTS splits text into chunks that fit within the char limit,
// breaking on paragraph boundaries (double newline), then sentence boundaries.
func splitTextForTTS(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}

	paragraphs := strings.Split(text, "\n\n")
	var chunks []string
	var current strings.Builder

	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// If adding this paragraph would exceed limit, flush current chunk.
		if current.Len() > 0 && current.Len()+2+len(p) > limit {
			chunks = append(chunks, strings.TrimSpace(current.String()))
			current.Reset()
		}
		// If a single paragraph exceeds the limit, split by sentences.
		if len(p) > limit {
			if current.Len() > 0 {
				chunks = append(chunks, strings.TrimSpace(current.String()))
				current.Reset()
			}
			chunks = append(chunks, splitBySentences(p, limit)...)
			continue
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(p)
	}
	if current.Len() > 0 {
		chunks = append(chunks, strings.TrimSpace(current.String()))
	}
	return chunks
}

func splitBySentences(text string, limit int) []string {
	sentences := strings.SplitAfter(text, ". ")
	var chunks []string
	var current strings.Builder

	for _, s := range sentences {
		if current.Len()+len(s) > limit && current.Len() > 0 {
			chunks = append(chunks, strings.TrimSpace(current.String()))
			current.Reset()
		}
		current.WriteString(s)
	}
	if current.Len() > 0 {
		chunks = append(chunks, strings.TrimSpace(current.String()))
	}
	return chunks
}

// synthesizeChunked calls the TTS API for each chunk, saves individual MP3s,
// then concatenates them into a single WAV output.
func (c *Client) synthesizeChunked(ctx context.Context, chunks []string, outPath string, callFn func(context.Context, string, string) error) error {
	tmpDir := filepath.Dir(outPath)
	var mp3Parts []string
	defer func() {
		for _, p := range mp3Parts {
			os.Remove(p)
		}
	}()

	for i, chunk := range chunks {
		mp3Path := filepath.Join(tmpDir, fmt.Sprintf("tts_chunk_%d.mp3", i))
		log.Printf("tts: synthesizing chunk %d/%d (%d chars)", i+1, len(chunks), len(chunk))
		if err := callFn(ctx, chunk, mp3Path); err != nil {
			return fmt.Errorf("elevenlabs api chunk %d: %w", i+1, err)
		}
		mp3Parts = append(mp3Parts, mp3Path)
	}

	// Build ffmpeg concat file.
	concatPath := filepath.Join(tmpDir, "tts_concat.txt")
	defer os.Remove(concatPath)
	var concatBuf strings.Builder
	for _, p := range mp3Parts {
		abs, _ := filepath.Abs(p)
		fmt.Fprintf(&concatBuf, "file '%s'\n", abs)
	}
	if err := os.WriteFile(concatPath, []byte(concatBuf.String()), 0644); err != nil {
		return fmt.Errorf("write concat file: %w", err)
	}

	// Concatenate all MP3s into single MP3, then convert to WAV.
	mergedMP3 := outPath + ".merged.mp3"
	defer os.Remove(mergedMP3)
	cmd := exec.CommandContext(ctx, c.cfg.FFmpegBin,
		"-f", "concat", "-safe", "0", "-i", concatPath,
		"-c", "copy", "-y", mergedMP3,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg concat: %s: %w", truncateBytes(out, 300), err)
	}

	if c.cfg.Normalize {
		return c.convertAndNormalize(ctx, mergedMP3, outPath)
	}
	return c.convertToWAV(ctx, mergedMP3, outPath)
}
