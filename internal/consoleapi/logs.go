package consoleapi

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/observability"
)

const (
	DefaultLogLimit = 100
	MaxLogLimit     = 500
)

// LogListFilter limits a local diagnostic projection. It intentionally has no
// filename or path field: only the two Bridge-owned current JSONL files can be
// read.
type LogListFilter struct {
	Limit         int
	Level         string
	Process       string
	Component     string
	Event         string
	FeatureAreaID string
	TaskID        string
	ExecutionID   string
	RunID         string
}

// LogEntry is the safe structured projection of one JSONL event. Data remains
// structured because it was already sanitized by the shared logging boundary.
type LogEntry struct {
	SchemaVersion int            `json:"schemaVersion"`
	Timestamp     time.Time      `json:"timestamp"`
	Level         string         `json:"level"`
	Process       string         `json:"process"`
	Component     string         `json:"component"`
	Event         string         `json:"event"`
	Build         string         `json:"build,omitempty"`
	RequestID     string         `json:"requestId,omitempty"`
	ProjectID     string         `json:"projectId,omitempty"`
	AISessionID   string         `json:"aiSessionId,omitempty"`
	FeatureAreaID string         `json:"featureAreaId,omitempty"`
	TaskID        string         `json:"taskId,omitempty"`
	ExecutionID   string         `json:"executionId,omitempty"`
	RunID         string         `json:"runId,omitempty"`
	ApprovalID    string         `json:"approvalId,omitempty"`
	ErrorCode     string         `json:"errorCode,omitempty"`
	Message       string         `json:"message,omitempty"`
	Data          map[string]any `json:"data,omitempty"`
	source        string
	line          int
}

type LogPage struct {
	Items []LogEntry `json:"items"`
	Limit int        `json:"limit"`
}

func (r *Reader) ListLogs(ctx context.Context, filter LogListFilter) (LogPage, error) {
	if err := ctx.Err(); err != nil {
		return LogPage{}, err
	}
	limit, err := normalizeLogLimit(filter.Limit)
	if err != nil {
		return LogPage{}, err
	}
	if r.logDirectory == "" {
		return LogPage{Items: []LogEntry{}, Limit: limit}, nil
	}
	entries := make([]LogEntry, 0)
	// These fixed paths come from the shared logger and are deliberately not
	// derived from user input.
	coreLog, mcpLog := observability.LogFiles(r.logDirectory)
	paths := []string{coreLog, mcpLog}
	for generation := 1; generation <= observability.DefaultGenerations; generation++ {
		suffix := "." + strconv.Itoa(generation)
		paths = append(paths, coreLog+suffix, mcpLog+suffix)
	}
	for _, path := range paths {
		values, err := readLogFile(path, ctx)
		if err != nil {
			return LogPage{}, err
		}
		entries = append(entries, values...)
	}
	slices.SortFunc(entries, func(a, b LogEntry) int {
		if order := b.Timestamp.Compare(a.Timestamp); order != 0 {
			return order
		}
		if order := cmp.Compare(a.Process, b.Process); order != 0 {
			return order
		}
		if order := cmp.Compare(a.source, b.source); order != 0 {
			return order
		}
		return cmp.Compare(a.line, b.line)
	})
	result := make([]LogEntry, 0, min(limit, len(entries)))
	for _, entry := range entries {
		if matchesLog(entry, filter) {
			entry.source = ""
			entry.line = 0
			result = append(result, entry)
			if len(result) == limit {
				break
			}
		}
	}
	return LogPage{Items: result, Limit: limit}, nil
}

func normalizeLogLimit(value int) (int, error) {
	if value == 0 {
		return DefaultLogLimit, nil
	}
	if value < 0 || value > MaxLogLimit {
		return 0, errors.New("limit must be between 1 and 500")
	}
	return value, nil
}

func readLogFile(path string, ctx context.Context) ([]LogEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	entries := make([]LogEntry, 0)
	for lineNumber := 1; ; lineNumber++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, readErr := reader.ReadBytes('\n')
		// The writer always terminates committed records with a newline. Ignore
		// an unterminated tail that may be visible during a concurrent write.
		if len(line) > 0 && !(errors.Is(readErr, io.EOF) && line[len(line)-1] != '\n') {
			var entry LogEntry
			if json.Unmarshal(line, &entry) == nil && !entry.Timestamp.IsZero() {
				entry.source, entry.line = path, lineNumber
				entries = append(entries, entry)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return entries, nil
}

func matchesLog(entry LogEntry, filter LogListFilter) bool {
	return matchLogField(entry.Level, filter.Level) &&
		matchLogField(entry.Process, filter.Process) &&
		matchLogField(entry.Component, filter.Component) &&
		matchLogField(entry.Event, filter.Event) &&
		matchLogField(entry.FeatureAreaID, filter.FeatureAreaID) &&
		matchLogField(entry.TaskID, filter.TaskID) &&
		matchLogField(entry.ExecutionID, filter.ExecutionID) &&
		matchLogField(entry.RunID, filter.RunID)
}

func matchLogField(value, filter string) bool {
	return strings.TrimSpace(filter) == "" || value == strings.TrimSpace(filter)
}
