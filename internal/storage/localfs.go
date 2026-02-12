package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// LocalFS stores assets on the local filesystem under Root/<runID>/.
type LocalFS struct {
	Root string
}

// NewLocalFS creates a new local filesystem store rooted at root.
func NewLocalFS(root string) *LocalFS {
	return &LocalFS{Root: root}
}

func (l *LocalFS) RunDir(runID string) string {
	return filepath.Join(l.Root, runID)
}

func (l *LocalFS) ensureDir(runID string) error {
	return os.MkdirAll(l.RunDir(runID), 0o755)
}

func (l *LocalFS) Path(runID, filename string) string {
	_ = l.ensureDir(runID) // best-effort; subsequent writes will surface real errors
	return filepath.Join(l.RunDir(runID), filename)
}

func (l *LocalFS) Write(runID, filename string, r io.Reader) (string, error) {
	if err := l.ensureDir(runID); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	p := filepath.Join(l.RunDir(runID), filename)
	f, err := os.Create(p)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", p, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return "", fmt.Errorf("write %s: %w", p, err)
	}
	return p, nil
}

func (l *LocalFS) WriteBytes(runID, filename string, data []byte) (string, error) {
	if err := l.ensureDir(runID); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	p := filepath.Join(l.RunDir(runID), filename)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", p, err)
	}
	return p, nil
}
