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

	var llmRouter llm.ScriptGenerator
	var ttsProvider jobs.TTSSynthesizer

	// Build LLM provider list with fallback order.
	var llmProviders []llm.NamedProvider

	// Groq provider (if key is set).
	if settings.GroqAPIKey != "" {
		baseURL := "https://api.groq.com/openai/v1"
		model := "llama-3.3-70b-versatile"
		if settings.Mode == "test" {
			if settings.OpenAIBaseURL != "" {
				baseURL = settings.OpenAIBaseURL
			}
			if settings.OpenAIModel != "" {
				model = settings.OpenAIModel
			}
		}
		llmProviders = append(llmProviders, llm.NamedProvider{
			Name:   "groq",
			Client: llm.NewClient(llm.Config{BaseURL: baseURL, APIKey: settings.GroqAPIKey, Model: model}),
		})
	}

	// OpenAI provider (if key is set).
	if settings.OpenAIAPIKey != "" {
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
		llmProviders = append(llmProviders, llm.NamedProvider{
			Name:   "openai",
			Client: llm.NewClient(llm.Config{BaseURL: baseURL, APIKey: settings.OpenAIAPIKey, Model: model}),
		})
	}

	// In prod mode, prefer OpenAI first.
	if settings.Mode == "prod" && len(llmProviders) >= 2 {
		// Swap so OpenAI is first.
		for i, p := range llmProviders {
			if p.Name == "openai" && i > 0 {
				llmProviders[0], llmProviders[i] = llmProviders[i], llmProviders[0]
				break
			}
		}
	}

	if len(llmProviders) == 0 {
		return fmt.Errorf("no LLM API keys configured")
	}

	names := make([]string, len(llmProviders))
	for i, p := range llmProviders {
		names[i] = p.Name
	}
	log.Printf("worker-manager: LLM fallback order: %v", names)
	llmRouter = llm.NewRouter(llmProviders)

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
		ttsProvider = tts.NewClient(tts.Config{
			APIKey:    settings.ElevenLabsAPIKey,
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
		LLM:   llmRouter,
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
		FixEngine: jobs.NewFixEngine(outcomes),
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
