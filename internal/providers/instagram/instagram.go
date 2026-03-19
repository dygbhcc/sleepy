package instagram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TokenStore persists Instagram tokens.
type TokenStore interface {
	GetWorkerSettingsInstagram(ctx context.Context) (accessToken, userID string, err error)
	SaveInstagramToken(ctx context.Context, accessToken, userID string) error
}

// Client wraps the Instagram Graph API for publishing Reels.
type Client struct {
	store TokenStore
	http  *http.Client
}

// NewClient creates an Instagram API client.
func NewClient(store TokenStore) *Client {
	return &Client{
		store: store,
		http:  &http.Client{Timeout: 120 * time.Second},
	}
}

// PublishRequest describes a Reel to publish.
type PublishRequest struct {
	VideoURL  string // publicly accessible URL of the video file
	Caption   string
	CoverURL  string // optional cover image URL
}

// PublishResult contains the published media info.
type PublishResult struct {
	MediaID   string
	Permalink string
}

// Publish uploads a Reel to Instagram using the 2-step container flow.
// Note: The video must be accessible via a public URL. For local files,
// you'll need to serve them temporarily or use a pre-signed URL.
func (c *Client) Publish(ctx context.Context, req PublishRequest) (*PublishResult, error) {
	accessToken, userID, err := c.store.GetWorkerSettingsInstagram(ctx)
	if err != nil {
		return nil, fmt.Errorf("get instagram credentials: %w", err)
	}
	if accessToken == "" || userID == "" {
		return nil, fmt.Errorf("instagram not connected")
	}

	// Step 1: Create media container.
	containerID, err := c.createContainer(ctx, userID, accessToken, req)
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}
	log.Printf("instagram: container created: %s", containerID)

	// Step 2: Wait for container to finish processing.
	if err := c.waitForContainer(ctx, containerID, accessToken); err != nil {
		return nil, fmt.Errorf("container processing: %w", err)
	}

	// Step 3: Publish the container.
	mediaID, err := c.publishContainer(ctx, userID, accessToken, containerID)
	if err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}

	log.Printf("instagram: published media %s", mediaID)
	return &PublishResult{MediaID: mediaID}, nil
}

// HasToken checks if Instagram credentials are configured.
func (c *Client) HasToken(ctx context.Context) bool {
	token, userID, err := c.store.GetWorkerSettingsInstagram(ctx)
	return err == nil && token != "" && userID != ""
}

// ExchangeShortLivedToken exchanges a short-lived token for a long-lived one (60 days).
func (c *Client) ExchangeShortLivedToken(ctx context.Context, appID, appSecret, shortToken string) (string, error) {
	u := fmt.Sprintf(
		"https://graph.facebook.com/v19.0/oauth/access_token?grant_type=fb_exchange_token&client_id=%s&client_secret=%s&fb_exchange_token=%s",
		url.QueryEscape(appID), url.QueryEscape(appSecret), url.QueryEscape(shortToken),
	)

	resp, err := c.http.Get(u)
	if err != nil {
		return "", fmt.Errorf("exchange token: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if result.AccessToken == "" {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("empty access token: %s", string(body))
	}
	return result.AccessToken, nil
}

// GetUserID fetches the Instagram user ID for the given access token.
func (c *Client) GetUserID(ctx context.Context, accessToken string) (string, error) {
	u := fmt.Sprintf("https://graph.facebook.com/v19.0/me/accounts?access_token=%s", url.QueryEscape(accessToken))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID              string `json:"id"`
			InstagramBusinessAccount struct {
				ID string `json:"id"`
			} `json:"instagram_business_account"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode accounts: %w", err)
	}
	for _, page := range result.Data {
		if page.InstagramBusinessAccount.ID != "" {
			return page.InstagramBusinessAccount.ID, nil
		}
	}
	return "", fmt.Errorf("no Instagram business account found")
}

// --- Internal API calls ---

func (c *Client) createContainer(ctx context.Context, userID, token string, req PublishRequest) (string, error) {
	params := url.Values{
		"media_type":   {"REELS"},
		"video_url":    {req.VideoURL},
		"caption":      {req.Caption},
		"access_token": {token},
	}
	if req.CoverURL != "" {
		params.Set("cover_url", req.CoverURL)
	}

	u := fmt.Sprintf("https://graph.facebook.com/v19.0/%s/media", userID)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(params.Encode()))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		ID    string `json:"id"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if result.Error != nil {
		return "", fmt.Errorf("instagram API: %s", result.Error.Message)
	}
	return result.ID, nil
}

func (c *Client) waitForContainer(ctx context.Context, containerID, token string) error {
	u := fmt.Sprintf("https://graph.facebook.com/v19.0/%s?fields=status_code&access_token=%s",
		containerID, url.QueryEscape(token))

	for i := 0; i < 60; i++ { // max 5 minutes (5s * 60)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}

		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		resp, err := c.http.Do(req)
		if err != nil {
			continue
		}
		var result struct {
			StatusCode string `json:"status_code"`
		}
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()

		switch result.StatusCode {
		case "FINISHED":
			return nil
		case "ERROR":
			return fmt.Errorf("container processing failed")
		case "IN_PROGRESS", "PUBLISHED":
			// keep waiting
		}
	}
	return fmt.Errorf("container processing timed out")
}

func (c *Client) publishContainer(ctx context.Context, userID, token, containerID string) (string, error) {
	params := url.Values{
		"creation_id":  {containerID},
		"access_token": {token},
	}

	u := fmt.Sprintf("https://graph.facebook.com/v19.0/%s/media_publish", userID)
	req, err := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(params.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		ID    string `json:"id"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if result.Error != nil {
		return "", fmt.Errorf("instagram publish: %s", result.Error.Message)
	}
	return result.ID, nil
}
