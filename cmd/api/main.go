package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"sleepy/internal/db"
	"sleepy/internal/domain"
	"sleepy/internal/jobs"
	"sleepy/internal/providers/youtube"
	"sleepy/internal/worker"
)

var store *db.DB
var assetRoot string
var workerMgr *worker.Manager

func main() {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		dsn = "postgres://localhost:5432/sleepy?sslmode=disable"
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

	go startScheduler(context.Background(), store)

	mux := http.NewServeMux()

	// Frontend
	mux.HandleFunc("GET /", handleIndex)

	// API
	mux.HandleFunc("GET /api/runs", handleListRuns)
	mux.HandleFunc("POST /api/runs", handleCreateRun)
	mux.HandleFunc("POST /api/batch", handleBatchCreate)
	mux.HandleFunc("GET /api/runs/{id}", handleGetRun)
	mux.HandleFunc("DELETE /api/runs/{id}", handleDeleteRun)
	mux.HandleFunc("POST /api/runs/{id}/retry", handleRetryRun)
	mux.HandleFunc("POST /api/runs/{id}/approve-voice", handleApproveVoice)
	mux.HandleFunc("GET /api/runs/{id}/assets", handleListAssets)
	mux.HandleFunc("GET /api/runs/{id}/script", handleGetScript)
	mux.HandleFunc("PUT /api/runs/{id}/script", handleUpdateScript)
	mux.HandleFunc("PUT /api/runs/{id}/thumbnail", handleUpdateThumbnail)
	mux.HandleFunc("GET /api/elevenlabs/voices", handleElevenLabsVoices)
	mux.HandleFunc("GET /api/elevenlabs/models", handleElevenLabsModels)
	mux.HandleFunc("GET /api/edge/voices", handleEdgeVoices)
	mux.HandleFunc("GET /api/music", handleListMusic)
	mux.HandleFunc("GET /api/settings", handleGetSettings)
	mux.HandleFunc("PUT /api/settings", handleUpdateSettings)
	mux.HandleFunc("POST /api/worker/start", handleWorkerStart)
	mux.HandleFunc("POST /api/worker/stop", handleWorkerStop)
	mux.HandleFunc("GET /api/worker/status", handleWorkerStatus)
	mux.HandleFunc("GET /api/youtube/auth", handleYouTubeAuth)
	mux.HandleFunc("GET /api/youtube/callback", handleYouTubeCallback)
	mux.HandleFunc("GET /api/youtube/status", handleYouTubeStatus)
	mux.HandleFunc("POST /api/youtube/disconnect", handleYouTubeDisconnect)
	mux.HandleFunc("POST /api/runs/{id}/upload-youtube", handleUploadYouTube)
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
		Title       string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Series == "" {
		req.Series = "Cosmos"
	}
	if req.Episode == "" {
		req.Episode = "Untitled"
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

	// If the user provided a custom episode name via "title" field, use it as
	// the episode and lock it so AI won't overwrite.
	if t := strings.TrimSpace(req.Title); t != "" {
		if err := store.UpdateRunEpisode(r.Context(), run.ID, t); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := store.UpdateRunTitle(r.Context(), run.ID, "", true); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		run.Episode = t
		run.TitleLocked = true
	}

	if err := store.EnqueueJob(r.Context(), run.ID, jobs.JobTypeRunPipeline); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, run)
}

func handleBatchCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Count       int    `json:"count"`
		Language    string `json:"language"`
		DurationMin int    `json:"duration_min"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Count <= 0 {
		req.Count = 3
	}
	if req.Count > 10 {
		req.Count = 10
	}
	if req.Language == "" {
		req.Language = "en"
	}
	if req.DurationMin <= 0 {
		req.DurationMin = 3
	}

	// Find existing episode names to avoid duplicates.
	existing, err := store.ListRuns(r.Context(), "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	used := make(map[string]bool, len(existing))
	for _, run := range existing {
		used[run.Episode] = true
	}

	picks := pickEpisodes(req.Count, used)
	if len(picks) == 0 {
		writeErr(w, http.StatusConflict, "no unused episodes available in pool")
		return
	}

	var created []domain.Run
	for _, ep := range picks {
		run, err := store.CreateRun(r.Context(), ep.Series, ep.Episode, ep.Style, req.Language, req.DurationMin)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := store.EnqueueJob(r.Context(), run.ID, jobs.JobTypeRunPipeline); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		created = append(created, *run)
	}

	writeJSON(w, http.StatusCreated, created)
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
		// Asset doesn't exist yet — create it (manual script for a PENDING/NEEDS_REVIEW run).
		runDir := filepath.Join(assetRoot, id)
		if err := os.MkdirAll(runDir, 0755); err != nil {
			writeErr(w, http.StatusInternalServerError, "cannot create run directory")
			return
		}
		mdPath := filepath.Join(runDir, "script.md")
		if err := os.WriteFile(mdPath, []byte(req.Content), 0644); err != nil {
			writeErr(w, http.StatusInternalServerError, "cannot write script file")
			return
		}
		if err := store.InsertAsset(r.Context(), id, domain.AssetScriptMD, mdPath); err != nil {
			writeErr(w, http.StatusInternalServerError, "cannot record script asset")
			return
		}

		// Create basic SSML wrapper for TTS.
		ssmlContent := "<speak>\n" + req.Content + "\n</speak>"
		ssmlPath := filepath.Join(runDir, "script.ssml")
		if err := os.WriteFile(ssmlPath, []byte(ssmlContent), 0644); err != nil {
			log.Printf("handleUpdateScript: failed to write SSML: %v", err)
		} else {
			_ = store.InsertAsset(r.Context(), id, domain.AssetScriptSSML, ssmlPath)
		}

		// Update script hash.
		h := sha256.Sum256([]byte(req.Content))
		_ = store.UpdateRunHash(r.Context(), id, "script_hash", hex.EncodeToString(h[:6]))

		asset, _ = store.GetAsset(r.Context(), id, domain.AssetScriptMD)
		writeJSON(w, http.StatusOK, asset)
		return
	}

	// Asset exists — update in place.
	if err := os.WriteFile(asset.Path, []byte(req.Content), 0644); err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot write script file")
		return
	}

	// Also update SSML.
	ssmlAsset, ssmlErr := store.GetAsset(r.Context(), id, domain.AssetScriptSSML)
	if ssmlErr == nil {
		ssmlContent := "<speak>\n" + req.Content + "\n</speak>"
		_ = os.WriteFile(ssmlAsset.Path, []byte(ssmlContent), 0644)
	}

	// Update script hash so pipeline recognizes the change.
	h := sha256.Sum256([]byte(req.Content))
	_ = store.UpdateRunHash(r.Context(), id, "script_hash", hex.EncodeToString(h[:6]))

	writeJSON(w, http.StatusOK, asset)
}

// maskKey returns the last 4 characters of a key, or empty if too short.
func maskKey(key string) string {
	if len(key) <= 4 {
		return key
	}
	return strings.Repeat("*", len(key)-4) + key[len(key)-4:]
}

// handleElevenLabsVoices proxies the ElevenLabs voices API to list available voices.
func handleElevenLabsVoices(w http.ResponseWriter, r *http.Request) {
	// Use API key from environment variable.
	apiKey := os.Getenv("ELEVENLABS_API_KEY")
	if apiKey == "" {
		writeErr(w, http.StatusBadRequest, "ElevenLabs API key required")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), "GET",
		"https://api.elevenlabs.io/v2/voices?page_size=100", nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("xi-api-key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "ElevenLabs API error: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		writeErr(w, resp.StatusCode, "ElevenLabs: "+string(body))
		return
	}

	var result struct {
		Voices []struct {
			VoiceID    string `json:"voice_id"`
			Name       string `json:"name"`
			Category   string `json:"category"`
			PreviewURL string `json:"preview_url"`
		} `json:"voices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		writeErr(w, http.StatusInternalServerError, "decode voices: "+err.Error())
		return
	}

	type voice struct {
		VoiceID    string `json:"voice_id"`
		Name       string `json:"name"`
		Category   string `json:"category"`
		PreviewURL string `json:"preview_url"`
	}
	voices := make([]voice, len(result.Voices))
	for i, v := range result.Voices {
		voices[i] = voice{v.VoiceID, v.Name, v.Category, v.PreviewURL}
	}
	writeJSON(w, http.StatusOK, voices)
}

// handleElevenLabsModels returns the known ElevenLabs TTS models.
func handleElevenLabsModels(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ModelID string `json:"model_id"`
		Name    string `json:"name"`
	}
	result := []model{
		{"eleven_multilingual_v2", "Eleven Multilingual v2"},
		{"eleven_turbo_v2_5", "Eleven Turbo v2.5"},
		{"eleven_turbo_v2", "Eleven Turbo v2"},
		{"eleven_monolingual_v1", "Eleven English v1"},
		{"eleven_multilingual_v1", "Eleven Multilingual v1"},
	}
	if result == nil {
		result = []model{}
	}
	writeJSON(w, http.StatusOK, result)
}

// handleEdgeVoices runs `edge-tts --list-voices` and returns the results as JSON.
func handleEdgeVoices(w http.ResponseWriter, r *http.Request) {
	type edgeVoice struct {
		Name   string `json:"name"`
		Gender string `json:"gender"`
		Locale string `json:"locale"`
	}

	cmd := exec.CommandContext(r.Context(), "python3", "-m", "edge_tts", "--list-voices")
	out, err := cmd.Output()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "edge-tts list-voices failed")
		return
	}

	var voices []edgeVoice
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if i < 2 || strings.TrimSpace(line) == "" {
			continue // skip header and separator
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		gender := fields[1]
		// Extract locale from voice name (e.g. "en-US-AndrewNeural" → "en-US")
		locale := ""
		parts := strings.SplitN(name, "-", 3)
		if len(parts) >= 2 {
			locale = parts[0] + "-" + parts[1]
		}
		voices = append(voices, edgeVoice{Name: name, Gender: gender, Locale: locale})
	}
	if voices == nil {
		voices = []edgeVoice{}
	}
	writeJSON(w, http.StatusOK, voices)
}

// handleListMusic lists audio files in assets/music/.
func handleListMusic(w http.ResponseWriter, r *http.Request) {
	const musicDir = "assets/music"
	entries, err := os.ReadDir(musicDir)
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	type musicFile struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	var files []musicFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".mp3") || strings.HasSuffix(lower, ".wav") || strings.HasSuffix(lower, ".ogg") {
			files = append(files, musicFile{Name: name, Path: musicDir + "/" + name})
		}
	}
	if files == nil {
		files = []musicFile{}
	}
	writeJSON(w, http.StatusOK, files)
}

func handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s, err := store.GetWorkerSettings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":               s.Mode,
		"openai_key_set":     os.Getenv("OPENAI_API_KEY") != "",
		"openai_base_url":    s.OpenAIBaseURL,
		"openai_model":       s.OpenAIModel,
		"elevenlabs_key_set": os.Getenv("ELEVENLABS_API_KEY") != "",
		"elevenlabs_voice_id": s.ElevenLabsVoiceID,
		"elevenlabs_model_id": s.ElevenLabsModelID,
		"elevenlabs_speed":   s.ElevenLabsSpeed,
		"edge_voice":         s.EdgeVoice,
		"edge_rate":          s.EdgeRate,
		"normalize":              s.Normalize,
		"music_path":             s.MusicPath,
		"youtube_enabled":         s.YouTubeEnabled,
		"youtube_privacy":         s.YouTubePrivacy,
		"youtube_client_id":       s.YouTubeClientID,
		"youtube_client_secret":   maskKey(s.YouTubeClientSecret),
		"youtube_connected":       s.YouTubeRefreshToken != "",
		"updated_at":              s.UpdatedAt,
	})
}

func handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode              *string  `json:"mode"`
		OpenAIBaseURL     *string  `json:"openai_base_url"`
		OpenAIModel       *string  `json:"openai_model"`
		ElevenLabsVoiceID *string  `json:"elevenlabs_voice_id"`
		ElevenLabsModelID *string  `json:"elevenlabs_model_id"`
		ElevenLabsSpeed   *float64 `json:"elevenlabs_speed"`
		EdgeVoice            *string  `json:"edge_voice"`
		EdgeRate             *string  `json:"edge_rate"`
		Normalize            *bool    `json:"normalize"`
		MusicPath            *string  `json:"music_path"`
		YouTubeEnabled       *bool    `json:"youtube_enabled"`
		YouTubePrivacy       *string  `json:"youtube_privacy"`
		YouTubeClientID      *string  `json:"youtube_client_id"`
		YouTubeClientSecret  *string  `json:"youtube_client_secret"`
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
	if req.OpenAIBaseURL != nil {
		s.OpenAIBaseURL = *req.OpenAIBaseURL
	}
	if req.OpenAIModel != nil {
		s.OpenAIModel = *req.OpenAIModel
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
	if req.MusicPath != nil {
		s.MusicPath = *req.MusicPath
	}
	if req.YouTubeEnabled != nil {
		s.YouTubeEnabled = *req.YouTubeEnabled
	}
	if req.YouTubePrivacy != nil {
		s.YouTubePrivacy = *req.YouTubePrivacy
	}
	if req.YouTubeClientID != nil {
		s.YouTubeClientID = *req.YouTubeClientID
	}
	if req.YouTubeClientSecret != nil && *req.YouTubeClientSecret != "" && !strings.HasPrefix(*req.YouTubeClientSecret, "***") {
		s.YouTubeClientSecret = *req.YouTubeClientSecret
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

	// Validate required API keys (from env vars).
	if os.Getenv("OPENAI_API_KEY") == "" {
		writeErr(w, http.StatusBadRequest, "OPENAI_API_KEY env var is required")
		return
	}
	if s.Mode == "prod" {
		if os.Getenv("ELEVENLABS_API_KEY") == "" {
			writeErr(w, http.StatusBadRequest, "ELEVENLABS_API_KEY env var is required for prod mode")
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

// ---------- YouTube ----------

func findStyleThumbnail(style string) string {
	prefix := strings.ToLower(style)
	entries, err := os.ReadDir("assets/thumbnails")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(strings.ToLower(e.Name()), prefix) {
			return filepath.Join("assets/thumbnails", e.Name())
		}
	}
	return ""
}

func getYouTubeClient(ctx context.Context) (*youtube.Client, error) {
	s, err := store.GetWorkerSettings(ctx)
	if err != nil {
		return nil, err
	}
	if s.YouTubeClientID == "" || s.YouTubeClientSecret == "" {
		return nil, fmt.Errorf("YouTube client ID and secret not configured")
	}
	oauthCfg := youtube.NewOAuthConfig(s.YouTubeClientID, s.YouTubeClientSecret, baseURL()+"/api/youtube/callback")
	return youtube.NewClient(oauthCfg, store), nil
}

func baseURL() string {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}

func handleYouTubeAuth(w http.ResponseWriter, r *http.Request) {
	yt, err := getYouTubeClient(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	url := yt.AuthURL("sleepy-youtube")
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

func handleYouTubeCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		writeErr(w, http.StatusBadRequest, "missing code parameter")
		return
	}

	yt, err := getYouTubeClient(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := yt.Exchange(r.Context(), code); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Redirect back to the app settings page.
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

func handleYouTubeStatus(w http.ResponseWriter, r *http.Request) {
	yt, err := getYouTubeClient(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]bool{"connected": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"connected": yt.HasToken(r.Context())})
}

func handleYouTubeDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := store.ClearYouTubeToken(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

func handleUploadYouTube(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := store.GetRun(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}

	videoAsset, err := store.GetAsset(r.Context(), id, domain.AssetVideoMP4)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no video asset found for this run")
		return
	}

	yt, err := getYouTubeClient(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	s, _ := store.GetWorkerSettings(r.Context())
	privacy := "unlisted"
	if s != nil && s.YouTubePrivacy != "" {
		privacy = s.YouTubePrivacy
	}

	title := fmt.Sprintf("Deep Sleep Story | %s - %s", run.Style, run.Episode)
	desc := fmt.Sprintf("A gentle sleep narration.\n\nSeries: %s\nEpisode: %s\nStyle: %s",
		run.Series, run.Episode, run.Style)

	videoID, err := yt.Upload(r.Context(), youtube.UploadRequest{
		FilePath:      videoAsset.Path,
		Title:         title,
		Description:   desc,
		Tags:          []string{"sleep", "narration", "relaxation", run.Style, run.Series},
		Privacy:       privacy,
		ThumbnailPath: findStyleThumbnail(run.Style),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = store.SetYouTubeVideoID(r.Context(), id, videoID)

	writeJSON(w, http.StatusOK, map[string]string{
		"video_id": videoID,
		"url":      "https://youtube.com/watch?v=" + videoID,
	})
}
