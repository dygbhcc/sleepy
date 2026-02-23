package worker

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"sleepy/internal/db"
	"sleepy/internal/domain"
	"sleepy/internal/jobs"
	"sleepy/internal/providers/image"
	"sleepy/internal/providers/llm"
	"sleepy/internal/providers/tts"
	"sleepy/internal/render"
	"sleepy/internal/storage"
)

// Manager controls the lifecycle of an in-process worker goroutine.
type Manager struct {
	db        *db.DB
	assetRoot string
	mu        sync.Mutex
	cancel    context.CancelFunc
	running   bool
}

// NewManager creates a new worker manager.
func NewManager(database *db.DB, assetRoot string) *Manager {
	return &Manager{db: database, assetRoot: assetRoot}
}

// Start builds dependencies from the given settings and launches the worker goroutine.
func (m *Manager) Start(settings *domain.WorkerSettings) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return fmt.Errorf("worker is already running")
	}

	ffmpegBin := "ffmpeg"
	ffprobeBin := "ffprobe"

	var llmClient *llm.Client
	var ttsProvider jobs.TTSSynthesizer

	switch settings.Mode {
	case "prod":
		log.Println("worker-manager: starting in prod mode (OpenAI + ElevenLabs)")

		baseURL := settings.OpenAIBaseURL
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		model := settings.OpenAIModel
		if model == "" {
			model = "gpt-4o"
		}
		llmClient = llm.NewClient(llm.Config{
			BaseURL: baseURL,
			APIKey:  settings.OpenAIAPIKey,
			Model:   model,
		})

		speed := settings.ElevenLabsSpeed
		if speed <= 0 {
			speed = 0.80
		}
		modelID := settings.ElevenLabsModelID
		if modelID == "" {
			modelID = "eleven_monolingual_v1"
		}
		ttsProvider = tts.NewClient(tts.Config{
			APIKey:    settings.ElevenLabsAPIKey,
			VoiceID:   settings.ElevenLabsVoiceID,
			ModelID:   modelID,
			Speed:     speed,
			Normalize: settings.Normalize,
			FFmpegBin: ffmpegBin,
		})

	default: // "test"
		log.Println("worker-manager: starting in test mode (Groq + Edge TTS)")

		baseURL := settings.OpenAIBaseURL
		if baseURL == "" {
			baseURL = "https://api.groq.com/openai/v1"
		}
		model := settings.OpenAIModel
		if model == "" {
			model = "llama-3.3-70b-versatile"
		}
		llmClient = llm.NewClient(llm.Config{
			BaseURL: baseURL,
			APIKey:  settings.GroqAPIKey,
			Model:   model,
		})

		voice := settings.EdgeVoice
		if voice == "" {
			voice = "en-US-AndrewNeural"
		}
		rate := settings.EdgeRate
		if rate == "" {
			rate = "-20%"
		}
		ttsProvider = tts.NewEdgeClient(tts.EdgeConfig{
			Voice:     voice,
			Rate:      rate,
			FFmpegBin: ffmpegBin,
			Normalize: settings.Normalize,
		})
	}

	deps := jobs.Deps{
		DB:    m.db,
		Store: storage.NewLocalFS(m.assetRoot),
		LLM:   llmClient,
		TTS:   ttsProvider,
		Image: image.NewClient(image.Config{
			FFmpegBin:      ffmpegBin,
			BackgroundsDir: "assets/backgrounds",
		}),
		Render: render.RenderConfig{
			FFmpegBin:  ffmpegBin,
			FFprobeBin: ffprobeBin,
			FadeOutSec: 5.0,
			MusicPath:  "assets/music/breakzstudios-calm-of-the-cosmos-165862.mp3",
			MusicVol:   0.5,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.running = true

	go func() {
		jobs.RunWorker(ctx, deps, 3*time.Second, false, "")
		m.mu.Lock()
		m.running = false
		m.cancel = nil
		m.mu.Unlock()
		log.Println("worker-manager: worker goroutine exited")
	}()

	return nil
}

// Stop cancels the worker context and waits briefly for it to wind down.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cancel != nil {
		m.cancel()
	}
	m.running = false
	m.cancel = nil
	log.Println("worker-manager: stop requested")
}

// Running returns whether the worker goroutine is currently active.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}
