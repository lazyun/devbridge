// Package logbuffer keeps a bounded, presentation-safe tail of child process output.
package logbuffer

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// Stream identifies the child-process output stream.
type Stream string

const (
	Stdout Stream = "stdout"
	Stderr Stream = "stderr"
)

// Entry is one sanitized log line. Source normally identifies core or mcp.
type Entry struct {
	Time   time.Time `json:"time"`
	Source string    `json:"source"`
	Stream Stream    `json:"stream"`
	Text   string    `json:"text"`
}

// Buffer is a concurrency-safe ring of recent child process output.
type Buffer struct {
	mu       sync.RWMutex
	entries  []Entry
	start    int
	count    int
	capacity int
}

const maxEntryBytes = 8192

// New creates a buffer retaining at most capacity log lines. A non-positive
// capacity creates a one-line buffer so appending remains safe and bounded.
func New(capacity int) *Buffer {
	if capacity < 1 {
		capacity = 1
	}
	return &Buffer{entries: make([]Entry, capacity), capacity: capacity}
}

// Append splits output into lines, sanitizes each line, and adds it to the tail.
// Empty trailing fragments from newline-terminated process output are ignored.
func (b *Buffer) Append(source string, stream Stream, output []byte) {
	for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		b.AppendLine(source, stream, line)
	}
}

// AppendLine sanitizes and adds one line to the tail.
func (b *Buffer) AppendLine(source string, stream Stream, text string) {
	if len(text) > maxEntryBytes {
		text = text[:maxEntryBytes] + "…"
	}
	entry := Entry{Time: time.Now().UTC(), Source: source, Stream: stream, Text: Sanitize(text)}
	b.mu.Lock()
	defer b.mu.Unlock()
	idx := (b.start + b.count) % b.capacity
	if b.count == b.capacity {
		idx = b.start
		b.start = (b.start + 1) % b.capacity
	} else {
		b.count++
	}
	b.entries[idx] = entry
}

// Snapshot returns entries ordered from oldest to newest.
func (b *Buffer) Snapshot() []Entry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Entry, b.count)
	for i := range out {
		out[i] = b.entries[(b.start+i)%b.capacity]
	}
	return out
}

// Clear removes all retained output.
func (b *Buffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = make([]Entry, b.capacity)
	b.start, b.count = 0, 0
}

// Len reports retained entries.
func (b *Buffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.count
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)[^\s,;]+`),
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*basic\s+)[^\s,;]+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|password|passwd|secret|token)\s*[:=]\s*["']?)[^\s,"';]+`),
}

// Sanitize redacts common credential-shaped key/value and Authorization values.
// It intentionally does not attempt to parse arbitrary child output.
func Sanitize(text string) string {
	for _, pattern := range secretPatterns {
		text = pattern.ReplaceAllString(text, "$1[REDACTED]")
	}
	return text
}
