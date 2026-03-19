package tts

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
)

// EdgeConfig holds settings for the free Microsoft Edge TTS.
type EdgeConfig struct {
	Voice     string // e.g. "en-US-AndrewNeural"; default "en-US-AndrewNeural"
	Rate      string // e.g. "-20%"; default "-20%" (slower for sleep)
	FFmpegBin string
	Normalize bool
}

// EdgeClient wraps the edge-tts CLI (python3 -m edge_tts).
type EdgeClient struct {
	cfg EdgeConfig
}

// NewEdgeClient creates a configured Edge TTS client.
func NewEdgeClient(cfg EdgeConfig) *EdgeClient {
	if cfg.Voice == "" {
		cfg.Voice = "en-US-AndrewNeural"
	}
	if cfg.Rate == "" {
		cfg.Rate = "-20%"
	}
	if cfg.FFmpegBin == "" {
		cfg.FFmpegBin = "ffmpeg"
	}
	return &EdgeClient{cfg: cfg}
}

// defaultVoices maps language codes to recommended Edge TTS voices for sleep narration.
var defaultVoices = map[string]string{
	"en": "en-US-AndrewNeural",
	"tr": "tr-TR-AhmetNeural",
	"pt": "pt-BR-AntonioNeural",
	"es": "es-ES-AlvaroNeural",
	"it": "it-IT-DiegoNeural",
}

// VoiceForLang returns the configured voice for the given language,
// falling back to the default configured voice.
func (c *EdgeClient) VoiceForLang(lang string) string {
	if v, ok := defaultVoices[lang]; ok {
		return v
	}
	return c.cfg.Voice
}

// Synthesize generates speech from text using edge-tts, then converts to WAV.
func (c *EdgeClient) Synthesize(ctx context.Context, text string, outPath string) error {
	return c.synthesizeWithVoice(ctx, text, outPath, c.cfg.Voice)
}

// SynthesizeWithLang generates speech using a voice appropriate for the given language.
func (c *EdgeClient) SynthesizeWithLang(ctx context.Context, text string, outPath string, lang string) error {
	voice := c.VoiceForLang(lang)
	return c.synthesizeWithVoice(ctx, text, outPath, voice)
}

// SynthesizeWithOpts generates speech with per-call overrides (fix engine integration).
func (c *EdgeClient) SynthesizeWithOpts(ctx context.Context, text string, outPath string, rateDelta int, lang string) error {
	voice := c.cfg.Voice
	if lang != "" {
		voice = c.VoiceForLang(lang)
	}
	rate := c.cfg.Rate
	if rateDelta != 0 {
		// Parse current rate (e.g. "-20%") and apply delta.
		rate = applyRateDelta(rate, rateDelta)
	}
	return c.synthesizeWithVoiceAndRate(ctx, text, outPath, voice, rate)
}

func applyRateDelta(currentRate string, delta int) string {
	// Parse "-20%" → -20, apply delta, format back.
	var current int
	fmt.Sscanf(currentRate, "%d%%", &current)
	adjusted := current + delta
	if adjusted >= 0 {
		return fmt.Sprintf("+%d%%", adjusted)
	}
	return fmt.Sprintf("%d%%", adjusted)
}

func (c *EdgeClient) synthesizeWithVoice(ctx context.Context, text string, outPath string, voice string) error {
	return c.synthesizeWithVoiceAndRate(ctx, text, outPath, voice, c.cfg.Rate)
}

func (c *EdgeClient) synthesizeWithVoiceAndRate(ctx context.Context, text string, outPath string, voice string, rate string) error {
	mp3Path := outPath + ".tmp.mp3"

	log.Printf("edge-tts: synthesizing with voice=%s rate=%s", voice, rate)

	// Write text to temp file to avoid shell escaping issues.
	txtPath := outPath + ".tmp.txt"
	if err := os.WriteFile(txtPath, []byte(text), 0644); err != nil {
		return fmt.Errorf("write text file: %w", err)
	}
	defer os.Remove(txtPath)

	// Call edge-tts. Use --rate=VALUE format to prevent argparse from
	// interpreting negative values like "-20%" as flags.
	cmd := exec.CommandContext(ctx, "python3", "-m", "edge_tts",
		"--voice="+voice,
		"--rate="+rate,
		"--file="+txtPath,
		"--write-media="+mp3Path,
	)
	// Use process group so context cancellation kills all child processes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("edge-tts: %s: %w", truncateBytes(out, 500), err)
	}
	defer os.Remove(mp3Path)

	// Convert MP3 to WAV (44100 Hz mono), optionally normalize.
	if c.cfg.Normalize {
		return convertAndNormalizeFFmpeg(ctx, c.cfg.FFmpegBin, mp3Path, outPath)
	}
	return convertToWAVFFmpeg(ctx, c.cfg.FFmpegBin, mp3Path, outPath)
}

func convertAndNormalizeFFmpeg(ctx context.Context, ffmpegBin, mp3Path, wavPath string) error {
	cmd := exec.CommandContext(ctx, ffmpegBin,
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

func convertToWAVFFmpeg(ctx context.Context, ffmpegBin, mp3Path, wavPath string) error {
	cmd := exec.CommandContext(ctx, ffmpegBin,
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
