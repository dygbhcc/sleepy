package main

import (
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
)

var store *db.DB
var assetRoot string

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

	mux := http.NewServeMux()

	// Frontend
	mux.HandleFunc("GET /", handleIndex)

	// API
	mux.HandleFunc("GET /api/runs", handleListRuns)
	mux.HandleFunc("POST /api/runs", handleCreateRun)
	mux.HandleFunc("GET /api/runs/{id}", handleGetRun)
	mux.HandleFunc("DELETE /api/runs/{id}", handleDeleteRun)
	mux.HandleFunc("POST /api/runs/{id}/retry", handleRetryRun)
	mux.HandleFunc("GET /api/runs/{id}/assets", handleListAssets)
	mux.HandleFunc("GET /api/runs/{id}/script", handleGetScript)
	mux.HandleFunc("PUT /api/runs/{id}/script", handleUpdateScript)
	mux.HandleFunc("PUT /api/runs/{id}/thumbnail", handleUpdateThumbnail)
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
	if run.Status != domain.StatusFailed {
		writeErr(w, http.StatusBadRequest, "only FAILED runs can be retried")
		return
	}

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
