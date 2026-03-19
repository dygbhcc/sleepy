package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"sleepy/internal/db"
	"sleepy/internal/domain"
	"sleepy/internal/jobs"
	"sleepy/internal/providers/image"
	"sleepy/internal/providers/llm"
	"sleepy/internal/providers/tts"
	"sleepy/internal/providers/youtube"
	"sleepy/internal/render"
	"sleepy/internal/storage"
)

// Manager controls the lifecycle of an in-process worker goroutine.
type Manager struct {
	db          *db.DB
	assetRoot   string
	Concurrency int // number of parallel workers; 0 or 1 = single worker
	mu          sync.Mutex
	cancel      context.CancelFunc
	running     bool
	wg          sync.WaitGroup
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

	// Verify required binaries exist on PATH.
	if _, err := exec.LookPath(ffmpegBin); err != nil {
		return fmt.Errorf("ffmpeg not found on PATH: %w", err)
	}
	if _, err := exec.LookPath(ffprobeBin); err != nil {
		return fmt.Errorf("ffprobe not found on PATH: %w", err)
	}

	var ttsProvider jobs.TTSSynthesizer

	openaiKey := os.Getenv("OPENAI_API_KEY")
	if openaiKey == "" {
		openaiKey = settings.OpenAIAPIKey
	}
	if openaiKey == "" {
		return fmt.Errorf("OPENAI_API_KEY env var is required")
	}

	baseURL := settings.OpenAIBaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	model := settings.OpenAIModel
	if model == "" {
		if settings.Mode == "prod" {
			model = "gpt-4o"
		} else {
			model = "gpt-4o-mini"
		}
	}
	llmClient := llm.NewClient(llm.Config{BaseURL: baseURL, APIKey: openaiKey, Model: model})
	log.Printf("worker-manager: LLM provider: openai (model=%s)", model)

	switch settings.Mode {
	case "prod":
		log.Println("worker-manager: starting in prod mode (ElevenLabs TTS)")

		speed := settings.ElevenLabsSpeed
		if speed <= 0 {
			speed = 0.80
		}
		modelID := settings.ElevenLabsModelID
		if modelID == "" {
			modelID = "eleven_monolingual_v1"
		}
		elevenKey := os.Getenv("ELEVENLABS_API_KEY")
		if elevenKey == "" {
			elevenKey = settings.ElevenLabsAPIKey
		}
		if elevenKey == "" {
			return fmt.Errorf("ELEVENLABS_API_KEY env var is required for prod mode")
		}
		ttsProvider = tts.NewClient(tts.Config{
			APIKey:    elevenKey,
			VoiceID:   settings.ElevenLabsVoiceID,
			ModelID:   modelID,
			Speed:     speed,
			Normalize: settings.Normalize,
			FFmpegBin: ffmpegBin,
		})

	default: // "test"
		log.Println("worker-manager: starting in test mode (Edge TTS)")

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

	// Clean up stale locks from previous crashed workers.
	if n, err := m.db.CleanStaleLocks(context.Background()); err != nil {
		log.Printf("worker-manager: failed to clean stale locks: %v", err)
	} else if n > 0 {
		log.Printf("worker-manager: cleaned %d stale locks from previous run", n)
	}

	// Bootstrap fix engine from historical outcomes.
	outcomes, err := jobs.LoadFixOutcomes(context.Background(), m.db)
	if err != nil {
		log.Printf("worker-manager: failed to load fix outcomes (starting with empty scorer): %v", err)
		outcomes = nil
	} else {
		log.Printf("worker-manager: loaded %d historical fix outcomes", len(outcomes))
	}

	deps := jobs.Deps{
		InflightLimits: jobs.InflightLimits{
			Script: settings.MaxInflightScript,
			TTS:    settings.MaxInflightTTS,
			Render: settings.MaxInflightRender,
		},
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
			MusicPath:  settings.MusicPath,
			MusicVol:   0.5,
		},
		FixEngine: jobs.NewFixEngine(outcomes),
	}

	// Wire up YouTube client if configured.
	if settings.YouTubeEnabled && settings.YouTubeClientID != "" && settings.YouTubeClientSecret != "" {
		addr := "http://localhost:8080"
		oauthCfg := youtube.NewOAuthConfig(settings.YouTubeClientID, settings.YouTubeClientSecret, addr+"/api/youtube/callback")
		deps.YouTube = youtube.NewClient(oauthCfg, m.db)
		deps.YouTubePrivacy = settings.YouTubePrivacy
		if deps.YouTubePrivacy == "" {
			deps.YouTubePrivacy = "unlisted"
		}
		log.Printf("worker-manager: YouTube upload enabled (privacy=%s)", deps.YouTubePrivacy)
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.running = true

	n := max(m.Concurrency, 1)
	log.Printf("worker-manager: launching %d worker goroutine(s)", n)

	for i := 0; i < n; i++ {
		workerID := fmt.Sprintf("worker-%d", i)
		m.wg.Add(1)
		go func(id string) {
			defer m.wg.Done()
			jobs.RunWorker(ctx, deps, 3*time.Second, false, id)
			log.Printf("worker-manager: %s exited", id)
		}(workerID)
	}

	// Background goroutine to update state when all workers finish.
	go func() {
		m.wg.Wait()
		m.mu.Lock()
		m.running = false
		m.cancel = nil
		m.mu.Unlock()
		log.Println("worker-manager: all worker goroutines exited")
	}()

	return nil
}

// Stop cancels the worker context and waits for goroutines to finish (up to 30s).
func (m *Manager) Stop() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()

	// Wait for workers to drain with a timeout.
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("worker-manager: all workers stopped cleanly")
	case <-time.After(30 * time.Second):
		log.Println("worker-manager: stop timed out after 30s, some workers may still be running")
	}

	m.mu.Lock()
	m.running = false
	m.cancel = nil
	m.mu.Unlock()
}

// Running returns whether the worker goroutine is currently active.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}
