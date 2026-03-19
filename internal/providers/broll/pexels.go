package broll

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// PexelsClient wraps the Pexels video search API.
type PexelsClient struct {
	apiKey string
	http   *http.Client
}

// NewPexelsClient creates a Pexels API client.
func NewPexelsClient(apiKey string) *PexelsClient {
	return &PexelsClient{
		apiKey: apiKey,
		http:   &http.Client{Timeout: 60 * time.Second},
	}
}

// VideoResult represents a single B-roll video clip from Pexels.
type VideoResult struct {
	ID          int    `json:"id"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Duration    int    `json:"duration"` // seconds
	DownloadURL string `json:"download_url"`
	Quality     string `json:"quality"` // "hd" or "sd"
}

// SearchVideos finds portrait-oriented B-roll clips matching the query.
func (c *PexelsClient) SearchVideos(ctx context.Context, query string, count int) ([]VideoResult, error) {
	if count <= 0 {
		count = 5
	}

	u := fmt.Sprintf("https://api.pexels.com/videos/search?query=%s&orientation=portrait&per_page=%d&size=medium",
		url.QueryEscape(query), count)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pexels request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pexels HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	var result pexelsSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode pexels response: %w", err)
	}

	var videos []VideoResult
	for _, v := range result.Videos {
		// Pick the best HD file with portrait dimensions.
		best := pickBestFile(v.VideoFiles)
		if best == nil {
			continue
		}
		videos = append(videos, VideoResult{
			ID:          v.ID,
			Width:       best.Width,
			Height:      best.Height,
			Duration:    v.Duration,
			DownloadURL: best.Link,
			Quality:     best.Quality,
		})
	}
	return videos, nil
}

// DownloadClip downloads a video file to the given path.
func (c *PexelsClient) DownloadClip(ctx context.Context, downloadURL, outPath string) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download HTTP %d", resp.StatusCode)
	}

	tmp := outPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close file: %w", err)
	}
	return os.Rename(tmp, outPath)
}

// --- Pexels API response types ---

type pexelsSearchResponse struct {
	Videos []pexelsVideo `json:"videos"`
}

type pexelsVideo struct {
	ID         int           `json:"id"`
	Duration   int           `json:"duration"`
	VideoFiles []pexelsFile  `json:"video_files"`
}

type pexelsFile struct {
	ID      int    `json:"id"`
	Quality string `json:"quality"` // "hd", "sd", "uhd"
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Link    string `json:"link"`
}

// pickBestFile selects the best portrait-friendly HD file.
func pickBestFile(files []pexelsFile) *pexelsFile {
	var best *pexelsFile
	for i := range files {
		f := &files[i]
		// Prefer portrait (height > width) or square, HD quality.
		if f.Height < f.Width {
			continue // skip landscape
		}
		if best == nil || f.Height > best.Height {
			best = f
		}
	}
	// Fallback: any HD file if no portrait found.
	if best == nil {
		for i := range files {
			if files[i].Quality == "hd" {
				best = &files[i]
				break
			}
		}
	}
	// Last resort: first file.
	if best == nil && len(files) > 0 {
		best = &files[0]
	}
	return best
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
