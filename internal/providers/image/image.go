package image

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Config holds thumbnail generation settings.
type Config struct {
	FFmpegBin      string
	BackgroundsDir string // directory containing stock background images
}

// Client generates thumbnails via ffmpeg.
type Client struct {
	cfg Config
}

// NewClient creates a configured image client.
func NewClient(cfg Config) *Client {
	if cfg.FFmpegBin == "" {
		cfg.FFmpegBin = "ffmpeg"
	}
	return &Client{cfg: cfg}
}

// GenerateThumbnail creates a 1920x1080 thumbnail.
// If a stock background matching the style exists in BackgroundsDir, it is used.
// Otherwise falls back to a solid colour placeholder.
func (c *Client) GenerateThumbnail(ctx context.Context, title, style, outPath string) error {
	bg := c.pickBackground(style)
	if bg != "" {
		log.Printf("image: using background %s", bg)
		return c.renderWithBackground(ctx, bg, title, outPath)
	}
	log.Printf("image: no background for style %q, using solid colour", style)
	return c.solidColor(ctx, title, outPath)
}

// pickBackground finds a matching image: first tries <style>.jpg, then <style>_*.jpg,
// then any .jpg in BackgroundsDir.
func (c *Client) pickBackground(style string) string {
	if c.cfg.BackgroundsDir == "" {
		return ""
	}
	lower := strings.ToLower(style)

	// Exact match: cosmos.jpg
	exact := filepath.Join(c.cfg.BackgroundsDir, lower+".jpg")
	if _, err := os.Stat(exact); err == nil {
		return exact
	}

	// Prefix match: cosmos_1.jpg, cosmos-2.jpg, ...
	for _, sep := range []string{"_", "-"} {
		matches, _ := filepath.Glob(filepath.Join(c.cfg.BackgroundsDir, lower+sep+"*.jpg"))
		if len(matches) > 0 {
			return matches[rand.Intn(len(matches))]
		}
	}

	// Any jpg
	all, _ := filepath.Glob(filepath.Join(c.cfg.BackgroundsDir, "*.jpg"))
	if len(all) > 0 {
		return all[rand.Intn(len(all))]
	}
	return ""
}

// renderWithBackground scales the background to 1920x1080 and overlays title text.
func (c *Client) renderWithBackground(ctx context.Context, bgPath, title, outPath string) error {
	safe := sanitizeDrawtext(title)
	vf := fmt.Sprintf(
		"scale=1920:1080:force_original_aspect_ratio=increase,crop=1920:1080,"+
			"drawtext=text='%s':fontcolor=white:fontsize=52:x=(w-text_w)/2:y=(h-text_h)/2:shadowcolor=black@0.7:shadowx=3:shadowy=3",
		safe,
	)
	cmd := exec.CommandContext(ctx, c.cfg.FFmpegBin,
		"-i", bgPath,
		"-vf", vf,
		"-frames:v", "1",
		"-y", outPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Fallback: background without text
		log.Printf("image: drawtext failed, using background without text: %s", truncate(string(out), 200))
		cmd2 := exec.CommandContext(ctx, c.cfg.FFmpegBin,
			"-i", bgPath,
			"-vf", "scale=1920:1080:force_original_aspect_ratio=increase,crop=1920:1080",
			"-frames:v", "1",
			"-y", outPath,
		)
		out2, err2 := cmd2.CombinedOutput()
		if err2 != nil {
			return fmt.Errorf("background fallback: %s: %w", truncate(string(out2), 200), err2)
		}
	}
	return nil
}

// solidColor creates a plain dark frame with optional text.
func (c *Client) solidColor(ctx context.Context, title, outPath string) error {
	safe := sanitizeDrawtext(title)
	cmd := exec.CommandContext(ctx, c.cfg.FFmpegBin,
		"-f", "lavfi",
		"-i", fmt.Sprintf(
			"color=c=0x0a0a2e:s=1920x1080:d=1,drawtext=text='%s':fontcolor=white:fontsize=48:x=(w-text_w)/2:y=(h-text_h)/2",
			safe,
		),
		"-frames:v", "1",
		"-y", outPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		cmd2 := exec.CommandContext(ctx, c.cfg.FFmpegBin,
			"-f", "lavfi",
			"-i", "color=c=0x0a0a2e:s=1920x1080:d=1",
			"-frames:v", "1",
			"-y", outPath,
		)
		out2, err2 := cmd2.CombinedOutput()
		if err2 != nil {
			return fmt.Errorf("thumbnail fallback: %s: %w (original: %s)", string(out2), err2, string(out))
		}
	}
	return nil
}

func sanitizeDrawtext(s string) string {
	r := strings.NewReplacer(
		"'", "",
		":", "\\:",
		"\\", "",
		"%", "%%",
	)
	return r.Replace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
