package llm

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"sleepy/internal/errs"
)

// stubProvider and stubRouter test the fallback logic without real LLM calls.
type stubProvider struct {
	name string
	err  error
}

type stubRouter struct {
	providers []stubProvider
}

func (sr *stubRouter) GenerateScript(ctx context.Context, req ScriptRequest) (*ScriptResult, error) {
	var lastErr error
	for i, p := range sr.providers {
		if p.err == nil {
			return &ScriptResult{Markdown: "ok from " + p.name, SSML: "<speak>ok</speak>"}, nil
		}
		lastErr = p.err
		if !isFallbackEligible(p.err) {
			return nil, p.err
		}
		_ = i
	}
	return nil, fmt.Errorf("all providers failed: %w", lastErr)
}

func TestFallback_429ThenSuccess(t *testing.T) {
	sr := &stubRouter{
		providers: []stubProvider{
			{name: "groq", err: errs.NewTransient("groq", 429, fmt.Errorf("rate limited"))},
			{name: "openai", err: nil},
		},
	}
	result, err := sr.GenerateScript(context.Background(), ScriptRequest{})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if !strings.Contains(result.Markdown, "openai") {
		t.Fatalf("expected openai result, got: %s", result.Markdown)
	}
}

func TestFallback_400NoFallback(t *testing.T) {
	sr := &stubRouter{
		providers: []stubProvider{
			{name: "groq", err: fmt.Errorf("api error (HTTP 400): bad request")},
			{name: "openai", err: nil},
		},
	}
	_, err := sr.GenerateScript(context.Background(), ScriptRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected 400 error, got: %v", err)
	}
}

func TestFallback_All429(t *testing.T) {
	sr := &stubRouter{
		providers: []stubProvider{
			{name: "groq", err: errs.NewTransient("groq", 429, fmt.Errorf("rate limited"))},
			{name: "openai", err: errs.NewTransient("openai", 429, fmt.Errorf("rate limited"))},
		},
	}
	_, err := sr.GenerateScript(context.Background(), ScriptRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "all providers failed") {
		t.Fatalf("expected 'all providers failed', got: %v", err)
	}
}

func TestFallback_Permanent429(t *testing.T) {
	// Permanent 429 (quota exhausted) — not a TransientError but contains "HTTP 429".
	// Should still trigger fallback to next provider.
	sr := &stubRouter{
		providers: []stubProvider{
			{name: "groq", err: fmt.Errorf("api error (HTTP 429): insufficient_quota")},
			{name: "openai", err: nil},
		},
	}
	result, err := sr.GenerateScript(context.Background(), ScriptRequest{})
	if err != nil {
		t.Fatalf("expected success after fallback, got: %v", err)
	}
	if !strings.Contains(result.Markdown, "openai") {
		t.Fatalf("expected openai result, got: %s", result.Markdown)
	}
}

func TestIsFallbackEligible(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		eligible bool
	}{
		{"transient 429", errs.NewTransient("groq", 429, fmt.Errorf("rate limited")), true},
		{"transient 502", errs.NewTransient("groq", 502, fmt.Errorf("bad gateway")), true},
		{"permanent 429 quota", fmt.Errorf("api error (HTTP 429): insufficient_quota"), true},
		{"permanent 429 tokens per day", fmt.Errorf("llm call (attempt 1): api error (HTTP 429): tokens per day"), true},
		{"400 bad request", fmt.Errorf("api error (HTTP 400): bad request"), false},
		{"401 unauthorized", fmt.Errorf("api error (HTTP 401): unauthorized"), false},
		{"parse error", fmt.Errorf("parse error: unexpected token"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFallbackEligible(tt.err); got != tt.eligible {
				t.Errorf("isFallbackEligible() = %v, want %v", got, tt.eligible)
			}
		})
	}
}
