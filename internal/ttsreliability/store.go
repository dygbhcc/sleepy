package ttsreliability

import (
	"fmt"
	"os"
	"path/filepath"
)

// ArtifactStore manages TTS chunk artifact paths on the local filesystem.
type ArtifactStore struct {
	root string // e.g. "data/artifacts/tts"
}

// NewArtifactStore creates a store rooted at the given directory.
func NewArtifactStore(root string) *ArtifactStore {
	return &ArtifactStore{root: root}
}

// ChunkPath returns the path for a chunk attempt artifact.
// Layout: {root}/{runID}/chunk_{idx}/attempt_{n}/audio.wav
func (s *ArtifactStore) ChunkPath(runID string, chunkIdx, attempt int) string {
	return filepath.Join(s.root, runID,
		fmt.Sprintf("chunk_%d", chunkIdx),
		fmt.Sprintf("attempt_%d", attempt),
		"audio.wav")
}

// FinalPath returns the path for the assembled final audio.
func (s *ArtifactStore) FinalPath(runID string) string {
	return filepath.Join(s.root, runID, "narration.wav")
}

// EnsureDir creates all parent directories for the given path.
func EnsureDir(path string) error {
	dir := filepath.Dir(path)
	return os.MkdirAll(dir, 0755)
}
