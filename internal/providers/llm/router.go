package llm

import (
	"context"
	"fmt"
	"log"
	"strings"

	"sleepy/internal/errs"
)

// ScriptGenerator is the interface consumed by stepScript.
// Both *Client and *Router implement it.
type ScriptGenerator interface {
	GenerateScript(ctx context.Context, req ScriptRequest) (*ScriptResult, error)
}

// NamedProvider pairs a human-readable name with an LLM client.
type NamedProvider struct {
	Name   string  // e.g. "groq", "openai"
	Client *Client
}

// Router tries providers in order, falling back on 429 / transient errors.
type Router struct {
	providers []NamedProvider
}

// NewRouter creates a fallback router. At least one provider is required.
func NewRouter(providers []NamedProvider) *Router {
	return &Router{providers: providers}
}

// GenerateScript tries each provider in order. On a fallback-eligible error
// (HTTP 429 or transient), the next provider is attempted with the same request.
// Non-fallback errors are returned immediately.
func (r *Router) GenerateScript(ctx context.Context, req ScriptRequest) (*ScriptResult, error) {
	if len(r.providers) == 0 {
		return nil, fmt.Errorf("llm router: no providers configured")
	}

	var lastErr error
	for i, p := range r.providers {
		if i > 0 {
			log.Printf("llm-router: falling back to provider %q after error: %v", p.Name, lastErr)
		}
		result, err := p.Client.GenerateScript(ctx, req)
		if err == nil {
			if i > 0 {
				log.Printf("llm-router: provider %q succeeded (fallback from %q)", p.Name, r.providers[0].Name)
			}
			return result, nil
		}
		lastErr = err

		if !isFallbackEligible(err) {
			log.Printf("llm-router: provider %q returned non-fallback error: %v", p.Name, err)
			return nil, err
		}
		log.Printf("llm-router: provider %q failed (fallback-eligible): %v", p.Name, err)
	}

	return nil, fmt.Errorf("llm router: all %d providers failed, last error: %w", len(r.providers), lastErr)
}

// isFallbackEligible returns true for errors that warrant trying another provider:
//   - Any TransientError (HTTP 429, 502, 503, 504, network errors)
//   - "Permanent" 429s (quota exhausted, rate_limit_exceeded) — permanent for THIS
//     provider but another provider may work.
func isFallbackEligible(err error) bool {
	if errs.IsTransient(err) {
		return true
	}
	// Permanent 429s are not wrapped as TransientError but contain "HTTP 429".
	return strings.Contains(err.Error(), "HTTP 429")
}
