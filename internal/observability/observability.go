// Package observability provides bounded, local-only operational JSONL logs.
// It is deliberately best-effort: callers must never make a domain outcome
// conditional on a logging operation.
package observability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	SchemaVersion            = 1
	DefaultMaxBytes    int64 = 10 << 20
	DefaultGenerations       = 5
	DefaultMaxRecord         = 64 << 10
)

// Options configures a Logger. Writer is primarily useful for tests. When a
// directory is supplied, Filename defaults to <process>.jsonl.
type Options struct {
	Process     string
	Build       string
	Directory   string
	Filename    string
	Writer      io.Writer
	MaxBytes    int64
	Generations int
	MaxRecord   int
	Level       slog.Level
}

// Fields contains the optional stable correlation fields used by all events.
// Empty values are omitted from the JSON record.
type Fields struct {
	RequestID, ProjectID, AISessionID, FeatureAreaID, ReviewID, TaskID, ExecutionID, RunID, ApprovalID string
}

// Correlation is an alias retained to make call sites read naturally.
type Correlation = Fields

type contextKey struct{}
type loggerContextKey struct{}

// Binaries install their process logger during bootstrap. The package default
// is intentionally silent so importing a domain package does not produce I/O
// or timing side effects in tests and embedding applications.
var defaultLogger = New(Options{Process: "devbridge", Writer: io.Discard})

// SetDefault installs the process logger used by package-level Event helpers.
// Passing nil restores a safe stderr logger.
func SetDefault(logger *Logger) {
	if logger == nil {
		logger = New(Options{Process: "devbridge", Writer: io.Discard})
	}
	defaultLoggerMu.Lock()
	defaultLogger = logger
	defaultLoggerMu.Unlock()
}

var defaultLoggerMu sync.RWMutex

func Default() *Logger {
	defaultLoggerMu.RLock()
	defer defaultLoggerMu.RUnlock()
	return defaultLogger
}

// WithLogger and FromContext permit narrow propagation at existing context seams.
func WithLogger(ctx context.Context, logger *Logger) context.Context {
	return context.WithValue(ctx, loggerContextKey{}, logger)
}
func FromContext(ctx context.Context) *Logger {
	if ctx != nil {
		if logger, ok := ctx.Value(loggerContextKey{}).(*Logger); ok && logger != nil {
			return logger
		}
	}
	return Default()
}

// WithFields adds correlations without changing an existing context value.
func WithFields(ctx context.Context, fields Fields) context.Context {
	current := FieldsFromContext(ctx)
	return context.WithValue(ctx, contextKey{}, mergeFields(current, fields))
}

// WithContext is a compact spelling for adding correlation fields.
func WithContext(ctx context.Context, correlation Correlation) context.Context {
	return WithFields(ctx, Fields(correlation))
}

func FieldsFromContext(ctx context.Context) Fields {
	if ctx == nil {
		return Fields{}
	}
	fields, _ := ctx.Value(contextKey{}).(Fields)
	return fields
}

func mergeFields(a, b Fields) Fields {
	if b.RequestID != "" {
		a.RequestID = b.RequestID
	}
	if b.ProjectID != "" {
		a.ProjectID = b.ProjectID
	}
	if b.AISessionID != "" {
		a.AISessionID = b.AISessionID
	}
	if b.FeatureAreaID != "" {
		a.FeatureAreaID = b.FeatureAreaID
	}
	if b.ReviewID != "" {
		a.ReviewID = b.ReviewID
	}
	if b.TaskID != "" {
		a.TaskID = b.TaskID
	}
	if b.ExecutionID != "" {
		a.ExecutionID = b.ExecutionID
	}
	if b.RunID != "" {
		a.RunID = b.RunID
	}
	if b.ApprovalID != "" {
		a.ApprovalID = b.ApprovalID
	}
	return a
}

// Logger emits the schema-v1 events and also supplies a slog-compatible logger
// for low-level consumers. All methods are safe for concurrent use.
type Logger struct {
	process, build string
	maxRecord      int
	level          slog.Level
	sink           *sink
	slog           *slog.Logger
}

// New creates a best-effort logger. Sink setup failures are intentionally not
// returned: stderr remains available and application startup must proceed.
func New(options Options) *Logger {
	if options.Process == "" {
		options.Process = "devbridge"
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	if options.Generations <= 0 {
		options.Generations = DefaultGenerations
	}
	if options.MaxRecord <= 0 {
		options.MaxRecord = DefaultMaxRecord
	}
	stderr := options.Writer
	if stderr == nil {
		stderr = os.Stderr
	}
	s := &sink{stderr: stderr, maxBytes: options.MaxBytes, generations: options.Generations, process: options.Process, build: options.Build}
	if options.Directory != "" {
		name := options.Filename
		if name == "" {
			name = options.Process + ".jsonl"
		}
		s.path = filepath.Join(options.Directory, filepath.Base(name))
		// Eagerly try setup, but preserve best-effort semantics on failure.
		if err := s.openLocked(); err != nil {
			s.diagnosticLocked("OPEN", err)
		}
	}
	l := &Logger{process: options.Process, build: options.Build, maxRecord: options.MaxRecord, level: options.Level, sink: s}
	l.slog = slog.New(slogHandler{owner: l})
	return l
}

// Close releases the optional file sink. It never reports a failure to callers.
func (l *Logger) Close() {
	if l != nil && l.sink != nil {
		l.sink.close()
	}
}
func (l *Logger) Slog() *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l.slog
}

// Event writes one stable operational event. data is sanitized recursively and
// may contain only diagnostic metadata, never application authority.
func (l *Logger) Event(ctx context.Context, level slog.Level, component, event, message string, data map[string]any) {
	if l == nil || level < l.level {
		return
	}
	record := map[string]any{"schemaVersion": SchemaVersion, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "level": level.String(), "process": l.process, "component": bounded(component, 128), "event": bounded(event, 160)}
	if l.build != "" {
		record["build"] = bounded(l.build, 128)
	}
	if message != "" {
		record["message"] = sanitizeString(bounded(message, 2048))
	}
	putFields(record, FieldsFromContext(ctx))
	if len(data) > 0 {
		sanitized := sanitizeValue(data, 0)
		if object, ok := sanitized.(map[string]any); ok {
			if code, ok := object["errorCode"].(string); ok && code != "" {
				record["errorCode"] = bounded(code, 128)
				delete(object, "errorCode")
			}
			if len(object) != 0 {
				record["data"] = object
			}
		} else {
			record["data"] = sanitized
		}
	}
	l.write(record)
}

func (l *Logger) Info(ctx context.Context, component, event, message string, data map[string]any) {
	l.Event(ctx, slog.LevelInfo, component, event, message, data)
}
func (l *Logger) Error(ctx context.Context, component, event, message string, data map[string]any) {
	l.Event(ctx, slog.LevelError, component, event, message, data)
}

// Event emits through the logger attached to ctx, or the configured default.
func Event(ctx context.Context, level slog.Level, component, event, message string, data map[string]any) {
	FromContext(ctx).Event(ctx, level, component, event, message, data)
}
func Info(ctx context.Context, component, event, message string, data map[string]any) {
	Event(ctx, slog.LevelInfo, component, event, message, data)
}
func Error(ctx context.Context, component, event, message string, data map[string]any) {
	Event(ctx, slog.LevelError, component, event, message, data)
}

func (l *Logger) write(record map[string]any) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	if len(encoded)+1 > l.maxRecord {
		record["data"] = map[string]any{"truncated": true, "originalBytes": len(encoded)}
		record["message"] = bounded(asString(record["message"]), 512)
		encoded, err = json.Marshal(record)
		if err != nil {
			return
		}
	}
	encoded = append(encoded, '\n')
	// A pathological caller-selected max must still result in a valid JSONL line.
	if len(encoded) > l.maxRecord {
		encoded = []byte(`{"schemaVersion":1,"level":"ERROR","event":"log.record.truncated","data":{"truncated":true}}` + "\n")
	}
	l.sink.write(encoded)
}

func putFields(record map[string]any, f Fields) {
	for _, pair := range []struct{ k, v string }{{"requestId", f.RequestID}, {"projectId", f.ProjectID}, {"aiSessionId", f.AISessionID}, {"featureAreaId", f.FeatureAreaID}, {"reviewId", f.ReviewID}, {"taskId", f.TaskID}, {"executionId", f.ExecutionID}, {"runId", f.RunID}, {"approvalId", f.ApprovalID}} {
		if pair.v != "" {
			record[pair.k] = safeCorrelationValue(pair.v)
		}
	}
}

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ValidRequestID accepts only a compact opaque request handle suitable for a
// correlation field. Untrusted header values outside this grammar are replaced
// by callers rather than copied into diagnostics.
func ValidRequestID(value string) bool { return safeRequestID.MatchString(value) }

func safeCorrelationValue(value string) string {
	if safeRequestID.MatchString(value) {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return "redacted_" + hex.EncodeToString(sum[:8])
}

func asString(v any) string { s, _ := v.(string); return s }

// Fingerprint returns safe prompt/instruction metadata. It intentionally does
// not preserve the original text.
func Fingerprint(value string) map[string]any {
	sum := sha256.Sum256([]byte(value))
	return map[string]any{"bytes": len(value), "sha256": hex.EncodeToString(sum[:])}
}

func bounded(value string, limit int) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " ")
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…"
}

// Bound returns a single-line, UTF-8-safe diagnostic value.
func Bound(value string, limit int) string { return bounded(value, limit) }

const (
	CoreLogFilename = "devbridge.jsonl"
	MCPLogFilename  = "devbridge-mcp.jsonl"
)

// LogFiles returns only the two fixed filenames inside dir; callers must not
// accept a user-selected filename when serving diagnostics.
func LogFiles(dir string) (core, mcp string) {
	return filepath.Join(dir, CoreLogFilename), filepath.Join(dir, MCPLogFilename)
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "_", ""))
	// Usage counters describe quantities, not credentials.
	switch key {
	case "inputtokens", "cachedinputtokens", "cachewriteinputtokens", "outputtokens", "reasoningoutputtokens", "totaltokens":
		return false
	}
	for _, v := range []string{"authorization", "apikey", "password", "cookie", "secret", "privatekey", "credential", "bearertoken", "accesstoken", "refreshtoken", "authtoken"} {
		if strings.Contains(key, v) {
			return true
		}
	}
	return key == "token"
}

func sanitizeValue(value any, depth int) any {
	if depth > 8 {
		return "[truncated]"
	}
	switch v := value.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return v
	case string:
		return sanitizeString(v)
	case []byte:
		return map[string]any{"bytes": len(v), "sha256": Fingerprint(string(v))["sha256"]}
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			if sensitiveKey(key) {
				out[key] = "[redacted]"
			} else {
				out[bounded(key, 128)] = sanitizeValue(child, depth+1)
			}
		}
		return out
	case []any:
		out := make([]any, 0, min(len(v), 64))
		for i, child := range v {
			if i == 64 {
				out = append(out, "[truncated]")
				break
			}
			out = append(out, sanitizeValue(child, depth+1))
		}
		return out
	default:
		// Do not allow arbitrary structs to bypass key-based redaction. JSON
		// round-tripping keeps ordinary diagnostic structs useful while applying
		// the same policy to their exported fields.
		if encoded, err := json.Marshal(v); err == nil {
			var decoded any
			if json.Unmarshal(encoded, &decoded) == nil {
				return sanitizeValue(decoded, depth+1)
			}
		}
		return "[unserializable]"
	}
}

func sanitizeString(value string) string {
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && (parsed.User != nil || parsed.RawQuery != "") {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return bounded(parsed.String(), 2048)
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "-----begin") && strings.Contains(lower, "private key-----") {
		return "[redacted]"
	}
	value = embeddedURLUserinfo.ReplaceAllString(value, "$1[redacted]@")
	value = bearerValue.ReplaceAllString(value, "$1[redacted]")
	value = credentialAssignment.ReplaceAllString(value, "$1=[redacted]")
	return bounded(value, 4096)
}

var (
	bearerValue          = regexp.MustCompile(`(?i)(\bbearer\s+)[^\s,;]+`)
	credentialAssignment = regexp.MustCompile(`(?i)\b(authorization|api[_-]?(?:key|token)|access[_-]?token|refresh[_-]?token|auth[_-]?token|token|password|passwd|cookie|secret)\s*[:=]\s*(?:'[^']*'|"[^"]*"|[^\s,;]+)`)
	embeddedURLUserinfo  = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type slogHandler struct {
	owner *Logger
	attrs []slog.Attr
}

func (h slogHandler) Enabled(_ context.Context, level slog.Level) bool { return level >= h.owner.level }
func (h slogHandler) Handle(ctx context.Context, r slog.Record) error {
	data := map[string]any{}
	for _, a := range h.attrs {
		data[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool { data[a.Key] = a.Value.Any(); return true })
	component, _ := data["component"].(string)
	event, _ := data["event"].(string)
	delete(data, "component")
	delete(data, "event")
	if component == "" {
		component = "application"
	}
	if event == "" {
		event = "log.message"
	}
	h.owner.Event(ctx, r.Level, component, event, r.Message, data)
	return nil
}
func (h slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return slogHandler{owner: h.owner, attrs: append(append([]slog.Attr{}, h.attrs...), attrs...)}
}
func (h slogHandler) WithGroup(_ string) slog.Handler { return h }

type sink struct {
	mu          sync.Mutex
	stderr      io.Writer
	path        string
	file        *os.File
	maxBytes    int64
	generations int
	process     string
	build       string
}

func (s *sink) write(line []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path != "" {
		if s.file == nil {
			if err := s.openLocked(); err != nil {
				s.diagnosticLocked("OPEN", err)
			}
		}
		if s.file != nil {
			if info, err := s.file.Stat(); err == nil && info.Size()+int64(len(line)) > s.maxBytes {
				if err := s.rotateLocked(); err != nil {
					s.diagnosticLocked("ROTATE", err)
				}
			} else if err != nil {
				s.diagnosticLocked("STAT", err)
			}
			if s.file != nil {
				if _, err := s.file.Write(line); err == nil {
					_, _ = s.stderr.Write(line)
					return
				} else {
					s.diagnosticLocked("WRITE", err)
				}
				_ = s.file.Close()
				s.file = nil
			}
		}
	}
	_, _ = s.stderr.Write(line)
}

// diagnosticLocked reports only the sink operation and error type. It avoids
// recursively invoking the logger and never exposes an absolute local path.
func (s *sink) diagnosticLocked(operation string, err error) {
	record := map[string]any{
		"schemaVersion": SchemaVersion,
		"timestamp":     time.Now().UTC().Format(time.RFC3339Nano),
		"level":         "ERROR",
		"process":       s.process,
		"component":     "observability",
		"event":         "log.sink.failed",
		"errorCode":     "LOG_FILE_" + operation + "_FAILED",
		"message":       "structured log file sink failed; using stderr fallback",
		"data":          map[string]any{"operation": operation, "filename": filepath.Base(s.path), "errorType": fmt.Sprintf("%T", err)},
	}
	if s.build != "" {
		record["build"] = s.build
	}
	if encoded, marshalErr := json.Marshal(record); marshalErr == nil {
		_, _ = s.stderr.Write(append(encoded, '\n'))
	}
}
func (s *sink) openLocked() error {
	if s.path == "" || s.file != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		s.file = file
	}
	return err
}
func (s *sink) rotateLocked() error {
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			return err
		}
		s.file = nil
	}
	oldest := s.path + "." + itoa(s.generations)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return err
	}
	for i := s.generations - 1; i >= 1; i-- {
		if err := os.Rename(s.path+"."+itoa(i), s.path+"."+itoa(i+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(s.path, s.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return s.openLocked()
}
func (s *sink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 12)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
