package run

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/observability"
)

func TestOperationalLifecycleAndUsageEventsRemainDistinct(t *testing.T) {
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), observability.New(observability.Options{Writer: &output}))
	service := NewService(NewMemoryStore(), func() (string, error) { return "run-logged", nil }, nil)
	value, err := service.CreateForExecution(ctx, "task-a", PurposeImplementation, "execution-a")
	if err != nil {
		t.Fatal(err)
	}
	value, _ = service.Transition(ctx, value.ID, StatusPreparing)
	value, _ = service.Transition(ctx, value.ID, StatusRunning)
	value, _ = service.Transition(ctx, value.ID, StatusCollecting)
	usage := &TokenUsageObservation{Reliable: true, StartCumulative: TokenCounts{InputTokens: 100, CachedInputTokens: 40, OutputTokens: 20, TotalTokens: 120}, EndCumulative: TokenCounts{InputTokens: 130, CachedInputTokens: 45, OutputTokens: 28, TotalTokens: 158}}
	value, err = service.SucceedWithUsage(ctx, value.ID, Result{AgentClaim: evidence.AgentClaim{Message: "private final prose"}}, usage)
	if err != nil || value.Status != StatusSucceeded {
		t.Fatalf("result=%+v err=%v", value, err)
	}

	entries := runLogEntries(t, output.Bytes())
	events := make([]string, 0, len(entries))
	for _, entry := range entries {
		events = append(events, entry["event"].(string))
		if entry["runId"] != "run-logged" || entry["taskId"] != "task-a" || entry["executionId"] != "execution-a" {
			t.Fatalf("missing correlation: %#v", entry)
		}
	}
	want := []string{"run.created", "run.preparing", "run.running", "run.collecting", "codex.usage.snapshot", "codex.usage.snapshot", "run.usage.delta", "run.succeeded"}
	if len(events) != len(want) {
		t.Fatalf("events=%v", events)
	}
	for index := range want {
		if events[index] != want[index] {
			t.Fatalf("events=%v want=%v", events, want)
		}
	}
	if bytes.Contains(output.Bytes(), []byte("private final prose")) {
		t.Fatalf("AgentClaim leaked: %s", output.Bytes())
	}
	startData := entries[4]["data"].(map[string]any)
	if startData["telemetrySemantic"] != "CUMULATIVE" || startData["cachedInputTokens"] != float64(40) {
		t.Fatalf("snapshot=%#v", startData)
	}
	deltaData := entries[6]["data"].(map[string]any)
	if deltaData["derivedCachedInputDelta"] != float64(5) || deltaData["derivedTotalDelta"] != float64(38) {
		t.Fatalf("delta=%#v", deltaData)
	}
}

type failingLogWriter struct{}

func (failingLogWriter) Write([]byte) (int, error) {
	return 0, errors.New("injected log write failure")
}

type canceledApprovalLogStore struct{ *MemoryStore }

func (canceledApprovalLogStore) ListCanceledApprovalIDs(context.Context, string) ([]string, error) {
	return []string{"approval-pending"}, nil
}

func TestOperationalSinkFailureCannotChangeRunTransaction(t *testing.T) {
	tests := []struct {
		name   string
		logger func(*testing.T) *observability.Logger
	}{
		{name: "write", logger: func(*testing.T) *observability.Logger {
			return observability.New(observability.Options{Writer: failingLogWriter{}})
		}},
		{name: "creation", logger: func(*testing.T) *observability.Logger {
			return observability.New(observability.Options{Directory: "/dev/null/logs", Writer: io.Discard})
		}},
		{name: "rotation", logger: func(t *testing.T) *observability.Logger {
			dir := t.TempDir()
			generation := filepath.Join(dir, observability.CoreLogFilename+".1")
			if err := os.Mkdir(generation, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(generation, "block"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return observability.New(observability.Options{Directory: dir, Filename: observability.CoreLogFilename, Writer: io.Discard, MaxBytes: 1, Generations: 1})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger := test.logger(t)
			defer logger.Close()
			ctx := observability.WithLogger(context.Background(), logger)
			service := NewService(NewMemoryStore(), func() (string, error) { return "run-survives-log-failure", nil }, nil)
			value, err := service.Create(ctx, "task-a")
			if err != nil || value.ID != "run-survives-log-failure" || value.Status != StatusCreated {
				t.Fatalf("domain result changed by log failure: %+v, %v", value, err)
			}
			stored, err := service.Get(context.Background(), value.ID)
			if err != nil || stored.ID != value.ID {
				t.Fatalf("durable result missing after log failure: %+v, %v", stored, err)
			}
		})
	}
}

func TestUsageLoggingKeepsAmbiguousMissingCachedTokensNull(t *testing.T) {
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), observability.New(observability.Options{Writer: &output}))
	service := NewService(NewMemoryStore(), func() (string, error) { return "run-missing-cached", nil }, nil)
	value, err := service.Create(ctx, "task-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []Status{StatusPreparing, StatusRunning, StatusCollecting} {
		value, err = service.Transition(ctx, value.ID, status)
		if err != nil {
			t.Fatal(err)
		}
	}
	usage := &TokenUsageObservation{Reliable: true, StartCumulative: TokenCounts{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}, EndCumulative: TokenCounts{InputTokens: 20, OutputTokens: 4, TotalTokens: 24}}
	if _, err := service.SucceedWithUsage(ctx, value.ID, Result{}, usage); err != nil {
		t.Fatal(err)
	}
	entries := runLogEntries(t, output.Bytes())
	start := entries[4]["data"].(map[string]any)
	if start["cachedInputTokens"] != nil || start["fieldPresence"].(map[string]any)["cachedInputTokens"] != false {
		t.Fatalf("ambiguous cached input must remain unavailable: %#v", start)
	}
	delta := entries[6]["data"].(map[string]any)
	if delta["derivedCachedInputDelta"] != nil || delta["derivedCachedInputDeltaPresent"] != false {
		t.Fatalf("ambiguous cached delta must remain unavailable: %#v", delta)
	}
}

func TestTerminalRunLogsCanceledPendingApprovalOrdering(t *testing.T) {
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), observability.New(observability.Options{Writer: &output}))
	service := NewService(canceledApprovalLogStore{NewMemoryStore()}, func() (string, error) { return "run-terminal", nil }, nil)
	value, err := service.Create(ctx, "task-a")
	if err != nil {
		t.Fatal(err)
	}
	value, _ = service.Transition(ctx, value.ID, StatusPreparing)
	value, _ = service.Transition(ctx, value.ID, StatusRunning)
	if _, err := service.Fail(ctx, value.ID, Failure{Kind: FailureTask, Code: "review_failed"}); err != nil {
		t.Fatal(err)
	}
	events := runLogEntries(t, output.Bytes())
	got := []string{events[len(events)-3]["event"].(string), events[len(events)-2]["event"].(string), events[len(events)-1]["event"].(string)}
	want := []string{"run.failed", "approval.superseded", "approval.canceled"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("terminal Approval ordering=%v want=%v", got, want)
		}
	}
}

func runLogEntries(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var entries []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return entries
}
