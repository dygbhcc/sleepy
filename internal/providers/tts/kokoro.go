package tts

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
)

// KokoroConfig holds settings for the local Kokoro TTS engine.
type KokoroConfig struct {
	Voice      string  // e.g. "af_sky"; default "af_sky"
	Speed      float64 // speech speed: 0.5–1.5; default 0.85 (slightly slow for sleep)
	ScriptPath string  // path to kokoro_tts.py; default "scripts/kokoro_tts.py"
	FFmpegBin  string
	Normalize  bool
}

// KokoroClient wraps the local Kokoro TTS Python script.
type KokoroClient struct {
	cfg KokoroConfig
}

// NewKokoroClient creates a configured Kokoro TTS client.
func NewKokoroClient(cfg KokoroConfig) *KokoroClient {
	if cfg.Voice == "" {
		cfg.Voice = "af_sky"
	}
	if cfg.Speed <= 0 {
		cfg.Speed = 0.85
	}
	if cfg.ScriptPath == "" {
		cfg.ScriptPath = "scripts/kokoro_tts.py"
	}
	if cfg.FFmpegBin == "" {
		cfg.FFmpegBin = "ffmpeg"
	}
	return &KokoroClient{cfg: cfg}
}

// defaultKokoroVoices maps language codes to recommended Kokoro voices for sleep narration.
var defaultKokoroVoices = map[string]string{
	"en":    "af_sky",
	"en-gb": "bf_emma",
	"es":    "ef_dora",
	"fr":    "ff_siwis",
	"pt":    "pf_dora",
	"hi":    "hf_alpha",
	"it":    "if_sara",
	"ja":    "jf_alpha",
	"zh":    "zf_xiaobei",
	"ko":    "kf_alpha",
}

// VoiceForLang returns a Kokoro voice appropriate for the given language code.
func (c *KokoroClient) VoiceForLang(lang string) string {
	if v, ok := defaultKokoroVoices[lang]; ok {
		return v
	}
	return c.cfg.Voice
}

// Synthesize implements TTSSynthesizer.
func (c *KokoroClient) Synthesize(ctx context.Context, text string, outPath string) error {
	return c.synthesize(ctx, text, outPath, c.cfg.Voice, c.cfg.Speed)
}

// SynthesizeWithLang implements LanguageAwareTTS.
func (c *KokoroClient) SynthesizeWithLang(ctx context.Context, text string, outPath string, lang string) error {
	return c.synthesize(ctx, text, outPath, c.VoiceForLang(lang), c.cfg.Speed)
}

func (c *KokoroClient) synthesize(ctx context.Context, text, outPath, voice string, speed float64) error {
	log.Printf("kokoro-tts: voice=%s speed=%.2f chars=%d", voice, speed, len(text))

	txtPath := outPath + ".kokoro.txt"
	if err := os.WriteFile(txtPath, []byte(text), 0644); err != nil {
		return fmt.Errorf("write text file: %w", err)
	}
	defer os.Remove(txtPath)

	// Python writes a 24000 Hz WAV; we resample it with FFmpeg below.
	rawWAV := outPath + ".kokoro.raw.wav"
	defer os.Remove(rawWAV)

	cmd := exec.CommandContext(ctx, "python3", c.cfg.ScriptPath,
		txtPath,
		voice,
		rawWAV,
		strconv.FormatFloat(speed, 'f', 2, 64),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kokoro-tts: %s: %w", truncateBytes(out, 500), err)
	}
	log.Printf("kokoro-tts: %s", truncateBytes(out, 200))

	// Always resample from 24000 Hz → 44100 Hz mono to match the rest of the pipeline.
	// Optionally apply EBU R128 loudnorm in the same pass.
	af := "aresample=44100"
	if c.cfg.Normalize {
		af = "loudnorm=I=-16:TP=-1.5:LRA=11,aresample=44100"
	}
	ffCmd := exec.CommandContext(ctx, c.cfg.FFmpegBin,
		"-i", rawWAV,
		"-af", af,
		"-ac", "1",
		"-y", outPath,
	)
	if ffOut, err := ffCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg resample: %s: %w", truncateBytes(ffOut, 300), err)
	}
	return nil
}
