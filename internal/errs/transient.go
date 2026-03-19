package errs

import (
	"fmt"
	"time"
)

// TransientError represents a retryable error from an external service.
// Use errors.As to check if an error is transient.
type TransientError struct {
	StatusCode int           // HTTP status code (429, 502, 503, 504, 0 for non-HTTP)
	Cause      error         // underlying error
	Provider   string        // "openai", "groq", "elevenlabs", "edge_tts", "ffmpeg"
	RetryAfter time.Duration // suggested wait time from Retry-After header (0 = use default backoff)
}

func (e *TransientError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("transient error from %s (HTTP %d): %v", e.Provider, e.StatusCode, e.Cause)
	}
	return fmt.Sprintf("transient error from %s: %v", e.Provider, e.Cause)
}

func (e *TransientError) Unwrap() error { return e.Cause }

// NewTransient wraps an error as a transient error.
func NewTransient(provider string, statusCode int, cause error) *TransientError {
	return &TransientError{
		StatusCode: statusCode,
		Cause:      cause,
		Provider:   provider,
	}
}

// IsTransient returns true if the error (or any wrapped error) is a TransientError.
func IsTransient(err error) bool {
	var te *TransientError
	return As(err, &te)
}

// As is a re-export of errors.As for convenience within this package.
// Callers outside should use errors.As directly.
func As(err error, target any) bool {
	if err == nil {
		return false
	}
	// Use the standard library's errors.As via interface unwrapping.
	type asInterface interface{ As(any) bool }
	for err != nil {
		if x, ok := err.(asInterface); ok {
			if x.As(target) {
				return true
			}
		}
		// Manual type check for *TransientError target.
		if te, ok := target.(**TransientError); ok {
			if t, ok2 := err.(*TransientError); ok2 {
				*te = t
				return true
			}
		}
		// Unwrap.
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
