package runtime

import (
	"strings"
	"sync"

	"github.com/lazyun/devbridge/desktop/internal/logbuffer"
)

const maxPendingLogBytes = 8192

type serviceLogWriter struct {
	buffer  *logbuffer.Buffer
	service Service
	stream  logbuffer.Stream
	onLine  func(string)
	pending string
	mu      sync.Mutex
}

func (w *serviceLogWriter) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending += string(value)
	for {
		newline := strings.IndexByte(w.pending, '\n')
		if newline < 0 {
			break
		}
		line := strings.TrimSuffix(w.pending[:newline], "\r")
		w.pending = w.pending[newline+1:]
		w.buffer.AppendLine(string(w.service), w.stream, line)
		if w.onLine != nil {
			w.onLine(line)
		}
	}
	if len(w.pending) > maxPendingLogBytes {
		line := w.pending[:maxPendingLogBytes] + "…"
		w.buffer.AppendLine(string(w.service), w.stream, line)
		if w.onLine != nil {
			w.onLine(line)
		}
		w.pending = ""
	}
	return len(value), nil
}
