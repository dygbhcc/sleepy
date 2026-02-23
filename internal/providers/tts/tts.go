package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"sleepy/internal/errs"
	"os/exec"
	"time"
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
		cfg.Speed = 0.80
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 300 * time.Second},
	}
}

// SynthesizeWithOpts generates speech with per-call overrides from the fix engine.
func (c *Client) SynthesizeWithOpts(ctx context.Context, text string, outPath string, speedFactor, stability, similarityBoost float64) error {
	mp3Path := outPath + ".tmp.mp3"
	if err := c.callAPIWithOpts(ctx, text, mp3Path, speedFactor, stability, similarityBoost); err != nil {
		return fmt.Errorf("elevenlabs api: %w", err)
	}
	defer os.Remove(mp3Path)
	if c.cfg.Normalize {
		return c.convertAndNormalize(ctx, mp3Path, outPath)
	}
	return c.convertToWAV(ctx, mp3Path, outPath)
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
	simBoost := 0.75
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
func (c *Client) Synthesize(ctx context.Context, text string, outPath string) error {
	mp3Path := outPath + ".tmp.mp3"

	if err := c.callAPI(ctx, text, mp3Path); err != nil {
		return fmt.Errorf("elevenlabs api: %w", err)
	}
	defer os.Remove(mp3Path)

	if c.cfg.Normalize {
		if err := c.convertAndNormalize(ctx, mp3Path, outPath); err != nil {
			return fmt.Errorf("convert+normalize: %w", err)
		}
	} else {
		if err := c.convertToWAV(ctx, mp3Path, outPath); err != nil {
			return fmt.Errorf("convert to wav: %w", err)
		}
	}
	return nil
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
}

func (c *Client) callAPI(ctx context.Context, text, outPath string) error {
	body, err := json.Marshal(ttsReq{
		Text:    text,
		ModelID: c.cfg.ModelID,
		Speed:   c.cfg.Speed,
		VoiceSettings: voiceSettings{
			Stability:       0.80,
			SimilarityBoost: 0.75,
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
