package logbuf

import (
	"io"
	"strings"
	"sync"
)

const maxLines = 300

// Buffer is a thread-safe ring buffer that captures log lines.
type Buffer struct {
	mu    sync.RWMutex
	lines []string
	pos   int
	full  bool
}

var Default = &Buffer{}

const maxLineLen = 1000

func (b *Buffer) Write(p []byte) (n int, err error) {
	s := strings.TrimRight(string(p), "\n")
	if s == "" {
		return len(p), nil
	}
	if len(s) > maxLineLen {
		s = s[:maxLineLen] + "..."
	}
	b.mu.Lock()
	if len(b.lines) < maxLines {
		b.lines = append(b.lines, s)
	} else {
		b.lines[b.pos] = s
		b.pos = (b.pos + 1) % maxLines
		b.full = true
	}
	b.mu.Unlock()
	return len(p), nil
}

func (b *Buffer) Lines() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if !b.full {
		out := make([]string, len(b.lines))
		copy(out, b.lines)
		return out
	}
	out := make([]string, maxLines)
	copy(out, b.lines[b.pos:])
	copy(out[maxLines-b.pos:], b.lines[:b.pos])
	return out
}

func MultiWriter(dst io.Writer) io.Writer {
	return io.MultiWriter(dst, Default)
}
