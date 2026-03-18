package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"sleepy/internal/db"
	"sleepy/internal/domain"
	"sleepy/internal/jobs"
	"sleepy/internal/logbuf"
	"sleepy/internal/worker"
)

var store *db.DB
var assetRoot string
var workerMgr *worker.Manager

func main() {
	log.SetOutput(logbuf.MultiWriter(os.Stderr))

	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		dsn = "postgres://sleepy:sleepy@localhost:5433/sleepy?sslmode=disable"
	}
	assetRoot = os.Getenv("ASSET_ROOT")
	if assetRoot == "" {
		assetRoot = "tmp/assets"
	}

	var err error
	store, err = db.Open(dsn)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer store.Close()

	workerMgr = worker.NewManager(store, assetRoot)

	// Seed settings from env vars if DB fields are empty.
	if s, err := store.GetWorkerSettings(context.Background()); err == nil {
		changed := false
		if s.GroqAPIKey == "" {
			if v := os.Getenv("GROQ_API_KEY"); v != "" {
				s.GroqAPIKey = v
				changed = true
			}
		}
		if s.OpenAIAPIKey == "" {
			if v := os.Getenv("OPENAI_API_KEY"); v != "" {
				s.OpenAIAPIKey = v
				changed = true
			}
		}
		if s.OpenAIBaseURL == "" {
			if v := os.Getenv("OPENAI_BASE_URL"); v != "" {
				s.OpenAIBaseURL = v
				changed = true
			}
		}
		if s.OpenAIModel == "" {
			if v := os.Getenv("OPENAI_MODEL"); v != "" {
				s.OpenAIModel = v
				changed = true
			}
		}
		if s.ElevenLabsAPIKey == "" {
			if v := os.Getenv("ELEVENLABS_API_KEY"); v != "" {
				s.ElevenLabsAPIKey = v
				changed = true
			}
		}
		if s.ElevenLabsVoiceID == "" {
			if v := os.Getenv("ELEVENLABS_VOICE_ID"); v != "" {
				s.ElevenLabsVoiceID = v
				changed = true
			}
		}
		if changed {
			if err := store.SaveWorkerSettings(context.Background(), s); err != nil {
				log.Printf("warning: could not seed settings from env: %v", err)
			} else {
				log.Println("seeded worker settings from environment variables")
			}
		}
	}

	mux := http.NewServeMux()

	// Frontend
	mux.HandleFunc("GET /", handleIndex)

	// API
	mux.HandleFunc("GET /api/runs", handleListRuns)
	mux.HandleFunc("POST /api/runs", handleCreateRun)
	mux.HandleFunc("GET /api/runs/{id}", handleGetRun)
	mux.HandleFunc("DELETE /api/runs/{id}", handleDeleteRun)
	mux.HandleFunc("POST /api/runs/{id}/retry", handleRetryRun)
	mux.HandleFunc("POST /api/runs/{id}/approve-voice", handleApproveVoice)
	mux.HandleFunc("GET /api/runs/{id}/assets", handleListAssets)
	mux.HandleFunc("GET /api/runs/{id}/script", handleGetScript)
	mux.HandleFunc("PUT /api/runs/{id}/script", handleUpdateScript)
	mux.HandleFunc("PUT /api/runs/{id}/thumbnail", handleUpdateThumbnail)
	mux.HandleFunc("GET /api/settings", handleGetSettings)
	mux.HandleFunc("PUT /api/settings", handleUpdateSettings)
	mux.HandleFunc("POST /api/worker/start", handleWorkerStart)
	mux.HandleFunc("POST /api/worker/stop", handleWorkerStop)
	mux.HandleFunc("GET /api/worker/status", handleWorkerStatus)
	mux.HandleFunc("GET /api/logs", handleGetLogs)
	mux.HandleFunc("GET /assets/{runID}/{file}", handleServeAsset)

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("api listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, cors(mux)))
}

// cors wraps a handler with permissive CORS headers for local dev.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ---------- handlers ----------

func handleListRuns(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	runs, err := store.ListRuns(r.Context(), status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runs == nil {
		runs = []domain.Run{}
	}
	writeJSON(w, http.StatusOK, runs)
}

func handleCreateRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Series      string `json:"series"`
		Episode     string `json:"episode"`
		Style       string `json:"style"`
		Language    string `json:"language"`
		DurationMin int    `json:"duration_min"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Series == "" || req.Episode == "" {
		writeErr(w, http.StatusBadRequest, "series and episode are required")
		return
	}
	if req.Style == "" {
		req.Style = "Cosmos"
	}
	if req.Language == "" {
		req.Language = "en"
	}
	if req.DurationMin <= 0 {
		req.DurationMin = 30
	}

	run, err := store.CreateRun(r.Context(), req.Series, req.Episode, req.Style, req.Language, req.DurationMin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := store.EnqueueJob(r.Context(), run.ID, jobs.JobTypeRunPipeline); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, run)
}

func handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := store.GetRun(r.Context(), id)
	if err != nil {
		if err.Error() == "get run "+id+": "+sql.ErrNoRows.Error() {
			writeErr(w, http.StatusNotFound, "run not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	assets, _ := store.ListAssets(r.Context(), id)
	if assets == nil {
		assets = []domain.Asset{}
	}
	jobsList, _ := store.ListJobs(r.Context(), id)
	if jobsList == nil {
		jobsList = []domain.Job{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"run":    run,
		"assets": assets,
		"jobs":   jobsList,
	})
}

func handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := store.DeleteRun(r.Context(), id); err != nil {
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusNotFound, "run not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleRetryRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := store.GetRun(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	if run.Status != domain.StatusFailed && run.Status != domain.StatusNeedsReview {
		writeErr(w, http.StatusBadRequest, "only FAILED or NEEDS_REVIEW runs can be retried")
		return
	}

	// Reset attempt counters so autopilot gets fresh retries.
	_ = store.ResetAttempts(r.Context(), id)

	// Find the last good status by checking which assets exist
	lastGood := domain.StatusPending
	assets, _ := store.ListAssets(r.Context(), id)
	for _, a := range assets {
		switch a.Kind {
		case domain.AssetScriptMD:
			if lastGood == domain.StatusPending {
				lastGood = domain.StatusScripted
			}
		case domain.AssetNarrationWAV:
			if lastGood == domain.StatusScripted {
				lastGood = domain.StatusVoiced
			}
		case domain.AssetThumbnailPNG:
			if lastGood == domain.StatusVoiced {
				lastGood = domain.StatusThumbnailed
			}
		case domain.AssetVideoMP4:
			if lastGood == domain.StatusThumbnailed {
				lastGood = domain.StatusRendered
			}
		case domain.AssetEpisodePack:
			if lastGood == domain.StatusRendered {
				lastGood = domain.StatusPackaged
			}
		}
	}

	if err := store.UpdateRunStatus(r.Context(), id, lastGood); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := store.EnqueueJob(r.Context(), id, jobs.JobTypeRunPipeline); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	run, _ = store.GetRun(r.Context(), id)
	writeJSON(w, http.StatusOK, run)
}

func handleApproveVoice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := store.GetRun(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	if run.Status != domain.StatusScripted {
		writeErr(w, http.StatusBadRequest, "run must be in SCRIPTED status to approve voice")
		return
	}
	if err := store.ApproveVoice(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	run, _ = store.GetRun(r.Context(), id)
	writeJSON(w, http.StatusOK, run)
}

func handleListAssets(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	assets, err := store.ListAssets(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if assets == nil {
		assets = []domain.Asset{}
	}
	writeJSON(w, http.StatusOK, assets)
}

func handleServeAsset(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	file := r.PathValue("file")

	// Prevent path traversal
	if strings.Contains(file, "..") || strings.Contains(runID, "..") {
		http.NotFound(w, r)
		return
	}

	path := filepath.Join(assetRoot, runID, file)
	http.ServeFile(w, r, path)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, "web/index.html")
}

func handleGetScript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	asset, err := store.GetAsset(r.Context(), id, domain.AssetScriptMD)
	if err != nil {
		writeErr(w, http.StatusNotFound, "script not found")
		return
	}

	content, err := os.ReadFile(asset.Path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot read script file")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(content)
}

func handleUpdateScript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	asset, err := store.GetAsset(r.Context(), id, domain.AssetScriptMD)
	if err != nil {
		writeErr(w, http.StatusNotFound, "script not found")
		return
	}

	if err := os.WriteFile(asset.Path, []byte(req.Content), 0644); err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot write script file")
		return
	}

	writeJSON(w, http.StatusOK, asset)
}

// maskKey returns the last 4 characters of a key, or empty if too short.
func maskKey(key string) string {
	if len(key) <= 4 {
		return key
	}
	return strings.Repeat("*", len(key)-4) + key[len(key)-4:]
}

func handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s, err := store.GetWorkerSettings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":               s.Mode,
		"groq_api_key":       maskKey(s.GroqAPIKey),
		"openai_api_key":     maskKey(s.OpenAIAPIKey),
		"openai_base_url":    s.OpenAIBaseURL,
		"openai_model":       s.OpenAIModel,
		"elevenlabs_api_key": maskKey(s.ElevenLabsAPIKey),
		"elevenlabs_voice_id": s.ElevenLabsVoiceID,
		"elevenlabs_model_id": s.ElevenLabsModelID,
		"elevenlabs_speed":   s.ElevenLabsSpeed,
		"edge_voice":         s.EdgeVoice,
		"edge_rate":          s.EdgeRate,
		"normalize":          s.Normalize,
		"updated_at":         s.UpdatedAt,
	})
}

func handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode              *string  `json:"mode"`
		GroqAPIKey        *string  `json:"groq_api_key"`
		OpenAIAPIKey      *string  `json:"openai_api_key"`
		OpenAIBaseURL     *string  `json:"openai_base_url"`
		OpenAIModel       *string  `json:"openai_model"`
		ElevenLabsAPIKey  *string  `json:"elevenlabs_api_key"`
		ElevenLabsVoiceID *string  `json:"elevenlabs_voice_id"`
		ElevenLabsModelID *string  `json:"elevenlabs_model_id"`
		ElevenLabsSpeed   *float64 `json:"elevenlabs_speed"`
		EdgeVoice         *string  `json:"edge_voice"`
		EdgeRate          *string  `json:"edge_rate"`
		Normalize         *bool    `json:"normalize"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Load current settings for partial update
	s, err := store.GetWorkerSettings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if req.Mode != nil {
		s.Mode = *req.Mode
	}
	if req.GroqAPIKey != nil && *req.GroqAPIKey != "" && !strings.HasPrefix(*req.GroqAPIKey, "***") {
		s.GroqAPIKey = *req.GroqAPIKey
	}
	if req.OpenAIAPIKey != nil && *req.OpenAIAPIKey != "" && !strings.HasPrefix(*req.OpenAIAPIKey, "***") {
		s.OpenAIAPIKey = *req.OpenAIAPIKey
	}
	if req.OpenAIBaseURL != nil {
		s.OpenAIBaseURL = *req.OpenAIBaseURL
	}
	if req.OpenAIModel != nil {
		s.OpenAIModel = *req.OpenAIModel
	}
	if req.ElevenLabsAPIKey != nil && *req.ElevenLabsAPIKey != "" && !strings.HasPrefix(*req.ElevenLabsAPIKey, "***") {
		s.ElevenLabsAPIKey = *req.ElevenLabsAPIKey
	}
	if req.ElevenLabsVoiceID != nil {
		s.ElevenLabsVoiceID = *req.ElevenLabsVoiceID
	}
	if req.ElevenLabsModelID != nil {
		s.ElevenLabsModelID = *req.ElevenLabsModelID
	}
	if req.ElevenLabsSpeed != nil {
		s.ElevenLabsSpeed = *req.ElevenLabsSpeed
	}
	if req.EdgeVoice != nil {
		s.EdgeVoice = *req.EdgeVoice
	}
	if req.EdgeRate != nil {
		s.EdgeRate = *req.EdgeRate
	}
	if req.Normalize != nil {
		s.Normalize = *req.Normalize
	}

	if err := store.SaveWorkerSettings(r.Context(), s); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleWorkerStart(w http.ResponseWriter, r *http.Request) {
	s, err := store.GetWorkerSettings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Validate required API keys
	if s.Mode == "test" && s.GroqAPIKey == "" {
		writeErr(w, http.StatusBadRequest, "Groq API key is required for test mode")
		return
	}
	if s.Mode == "openai" && s.OpenAIAPIKey == "" {
		writeErr(w, http.StatusBadRequest, "OpenAI API key is required for openai mode")
		return
	}
	if s.Mode == "prod" {
		if s.OpenAIAPIKey == "" {
			writeErr(w, http.StatusBadRequest, "OpenAI API key is required for prod mode")
			return
		}
		if s.ElevenLabsAPIKey == "" {
			writeErr(w, http.StatusBadRequest, "ElevenLabs API key is required for prod mode")
			return
		}
		if s.ElevenLabsVoiceID == "" {
			writeErr(w, http.StatusBadRequest, "ElevenLabs Voice ID is required for prod mode")
			return
		}
	}

	if err := workerMgr.Start(s); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func handleWorkerStop(w http.ResponseWriter, r *http.Request) {
	workerMgr.Stop()
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func handleWorkerStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"running": workerMgr.Running()})
}

func handleUpdateThumbnail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Verify the run exists
	_, err := store.GetRun(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}

	// Parse multipart form (max 10MB)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()

	// Validate file type
	ct := header.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		writeErr(w, http.StatusBadRequest, "only image files are allowed")
		return
	}

	// Determine extension from content type
	ext := ".png"
	switch ct {
	case "image/jpeg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	}

	// Ensure run asset directory exists
	runDir := filepath.Join(assetRoot, id)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot create asset directory")
		return
	}

	// Write file to disk
	destName := "thumbnail" + ext
	destPath := filepath.Join(runDir, destName)
	out, err := os.Create(destPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot create file")
		return
	}
	defer out.Close()

	if _, err := io.Copy(out, file); err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot write file")
		return
	}

	// Update or insert asset record
	existing, err := store.GetAsset(r.Context(), id, domain.AssetThumbnailPNG)
	if err == nil {
		// Update existing asset path
		store.UpdateAssetPath(r.Context(), existing.ID, destPath)
	} else {
		// Insert new asset
		store.InsertAsset(r.Context(), id, domain.AssetThumbnailPNG, destPath)
	}

	writeJSON(w, http.StatusOK, map[string]string{"path": destPath})
}

func handleGetLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"lines": logbuf.Default.Lines()})
}
