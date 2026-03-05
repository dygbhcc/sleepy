package youtube

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	yt "google.golang.org/api/youtube/v3"
)

// Scopes required for uploading videos.
var Scopes = []string{yt.YoutubeUploadScope, yt.YoutubeScope}

// TokenStore is used to persist and refresh OAuth2 tokens.
type TokenStore interface {
	GetYouTubeToken(ctx context.Context) (*oauth2.Token, error)
	SaveYouTubeToken(ctx context.Context, tok *oauth2.Token) error
}

// Client wraps the YouTube Data API v3 for video uploads.
type Client struct {
	oauthCfg *oauth2.Config
	store    TokenStore
}

// NewOAuthConfig builds the OAuth2 config from client credentials.
func NewOAuthConfig(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		RedirectURL:  redirectURL,
		Scopes:       Scopes,
	}
}

// NewClient creates a YouTube upload client.
func NewClient(oauthCfg *oauth2.Config, store TokenStore) *Client {
	return &Client{oauthCfg: oauthCfg, store: store}
}

// AuthURL returns the URL to redirect the user for OAuth2 consent.
func (c *Client) AuthURL(state string) string {
	return c.oauthCfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
}

// Exchange trades an authorization code for a token and persists it.
func (c *Client) Exchange(ctx context.Context, code string) error {
	tok, err := c.oauthCfg.Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("youtube oauth exchange: %w", err)
	}
	return c.store.SaveYouTubeToken(ctx, tok)
}

// HasToken returns true if a YouTube token is stored.
func (c *Client) HasToken(ctx context.Context) bool {
	tok, err := c.store.GetYouTubeToken(ctx)
	return err == nil && tok != nil && tok.RefreshToken != ""
}

// UploadRequest describes the video to upload.
type UploadRequest struct {
	FilePath      string
	Title         string
	Description   string
	Tags          []string
	Privacy       string // "unlisted", "public", "private"
	ThumbnailPath string // optional: path to thumbnail image
}

// Upload uploads a video to YouTube and returns the video ID.
func (c *Client) Upload(ctx context.Context, req UploadRequest) (string, error) {
	tok, err := c.store.GetYouTubeToken(ctx)
	if err != nil {
		return "", fmt.Errorf("get youtube token: %w", err)
	}
	if tok == nil || tok.RefreshToken == "" {
		return "", fmt.Errorf("youtube not connected (no token)")
	}

	// Create a token source that auto-refreshes and persist new tokens.
	ts := c.oauthCfg.TokenSource(ctx, tok)
	newTok, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("youtube token refresh: %w", err)
	}
	if newTok.AccessToken != tok.AccessToken {
		if saveErr := c.store.SaveYouTubeToken(ctx, newTok); saveErr != nil {
			log.Printf("youtube: failed to persist refreshed token: %v", saveErr)
		}
	}

	svc, err := yt.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return "", fmt.Errorf("youtube service: %w", err)
	}

	file, err := os.Open(req.FilePath)
	if err != nil {
		return "", fmt.Errorf("open video file: %w", err)
	}
	defer file.Close()

	privacy := req.Privacy
	if privacy == "" {
		privacy = "unlisted"
	}

	video := &yt.Video{
		Snippet: &yt.VideoSnippet{
			Title:       req.Title,
			Description: req.Description,
			Tags:        req.Tags,
		},
		Status: &yt.VideoStatus{
			PrivacyStatus: privacy,
		},
	}

	fi, _ := file.Stat()
	log.Printf("youtube: uploading %q (%s, %.1f MB)", req.Title, req.FilePath, float64(fi.Size())/(1024*1024))
	start := time.Now()

	call := svc.Videos.Insert([]string{"snippet", "status"}, video)
	call.Media(file, googleapi.ContentType("video/mp4"), googleapi.ChunkSize(32*1024*1024))

	resp, err := call.Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("youtube upload: %w", err)
	}

	log.Printf("youtube: uploaded %q → %s (took %s)", req.Title, resp.Id, time.Since(start).Round(time.Second))

	// Set custom thumbnail if provided.
	if req.ThumbnailPath != "" {
		if err := setThumbnail(ctx, svc, resp.Id, req.ThumbnailPath); err != nil {
			log.Printf("youtube: failed to set thumbnail for %s: %v", resp.Id, err)
		}
	}

	return resp.Id, nil
}

func setThumbnail(ctx context.Context, svc *yt.Service, videoID, thumbPath string) error {
	f, err := os.Open(thumbPath)
	if err != nil {
		return fmt.Errorf("open thumbnail: %w", err)
	}
	defer f.Close()

	call := svc.Thumbnails.Set(videoID)
	call.Media(f)
	_, err = call.Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("thumbnails.set: %w", err)
	}
	log.Printf("youtube: thumbnail set for %s from %s", videoID, thumbPath)
	return nil
}
