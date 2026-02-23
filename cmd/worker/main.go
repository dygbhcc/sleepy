package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sleepy/internal/db"
	"sleepy/internal/jobs"
	"sleepy/internal/providers/image"
	"sleepy/internal/providers/llm"
	"sleepy/internal/providers/tts"
	"sleepy/internal/render"
	"sleepy/internal/storage"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// MODE: "test" (free: Groq + Edge TTS) or "prod" (OpenAI + ElevenLabs)
	mode := envOr("MODE", "test")

	pgDSN := mustEnv("PG_DSN")
	assetRoot := mustEnv("ASSET_ROOT")
	ffmpegBin := envOr("FFMPEG_BIN", "ffmpeg")
	ffprobeBin := envOr("FFPROBE_BIN", "ffprobe")
	normalize := envOr("NORMALIZE", "true") != "false"

	database, err := db.Open(pgDSN)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer database.Close()

	var llmClient *llm.Client
	var ttsProvider jobs.TTSSynthesizer

	switch mode {
	case "prod":
		log.Println("=== MODE: prod (OpenAI + ElevenLabs) ===")

		llmClient = llm.NewClient(llm.Config{
			BaseURL: envOr("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			APIKey:  mustEnv("OPENAI_API_KEY"),
			Model:   envOr("OPENAI_MODEL", "gpt-4o"),
		})

		elevenSpeed := 0.80
		if s := envOr("ELEVENLABS_SPEED", ""); s != "" {
			fmt.Sscanf(s, "%f", &elevenSpeed)
		}
		ttsProvider = tts.NewClient(tts.Config{
			APIKey:    mustEnv("ELEVENLABS_API_KEY"),
			VoiceID:   mustEnv("ELEVENLABS_VOICE_ID"),
			ModelID:   envOr("ELEVENLABS_MODEL_ID", "eleven_monolingual_v1"),
			Speed:     elevenSpeed,
			Normalize: normalize,
			FFmpegBin: ffmpegBin,
		})

	default: // "test"
		log.Println("=== MODE: test (Groq + Edge TTS) ===")

		llmClient = llm.NewClient(llm.Config{
			BaseURL: envOr("OPENAI_BASE_URL", "https://api.groq.com/openai/v1"),
			APIKey:  mustEnv("GROQ_API_KEY"),
			Model:   envOr("OPENAI_MODEL", "llama-3.3-70b-versatile"),
		})

		ttsProvider = tts.NewEdgeClient(tts.EdgeConfig{
			Voice:     envOr("EDGE_VOICE", "en-US-AndrewNeural"),
			Rate:      envOr("EDGE_RATE", "-20%"),
			FFmpegBin: ffmpegBin,
			Normalize: normalize,
		})
	}

	deps := jobs.Deps{
		DB:    database,
		Store: storage.NewLocalFS(assetRoot),
		LLM:   llmClient,
		TTS:   ttsProvider,
		Image: image.NewClient(image.Config{
			FFmpegBin:      ffmpegBin,
			BackgroundsDir: envOr("BACKGROUNDS_DIR", "assets/backgrounds"),
		}),
		Render: render.RenderConfig{
			FFmpegBin:  ffmpegBin,
			FFprobeBin: ffprobeBin,
			FadeOutSec: 5.0,
			MusicPath:  envOr("MUSIC_PATH", "assets/music/breakzstudios-calm-of-the-cosmos-165862.mp3"),
			MusicVol:   0.5,
		},
	}

	log.Printf("config: normalize=%v ffmpeg=%s ffprobe=%s", normalize, ffmpegBin, ffprobeBin)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sig
		log.Printf("received signal %v, shutting down...", s)
		cancel()
	}()

	oneShot := envOr("WORKER_ONE_SHOT", "") != ""
	jobs.RunWorker(ctx, deps, 3*time.Second, oneShot, "")
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("FATAL: required env var %s is not set", key)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
