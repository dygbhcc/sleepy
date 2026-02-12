package storage

import "io"

// Store is the abstraction for persisting pipeline assets.
type Store interface {
	// RunDir returns the root directory for a given run.
	RunDir(runID string) string
	// Path returns the full filesystem path for a file within a run dir.
	// It also ensures the run directory exists.
	Path(runID, filename string) string
	// Write copies from r into the run directory and returns the absolute path.
	Write(runID, filename string, r io.Reader) (path string, err error)
	// WriteBytes writes raw bytes and returns the absolute path.
	WriteBytes(runID, filename string, data []byte) (path string, err error)
}
