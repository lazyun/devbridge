package consoleapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListLogsCombinesFixedProcessFilesFiltersAndSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, filepath.Join(dir, "devbridge.jsonl"), strings.Join([]string{
		`{"schemaVersion":1,"timestamp":"2026-01-02T03:04:05Z","level":"INFO","process":"devbridge","component":"review","event":"featurearea.review.start.accepted","featureAreaId":"featureArea-1","runId":"run-1","message":"accepted"}`,
		`{"timestamp":`,
		`{"schemaVersion":1,"timestamp":"2026-01-02T03:04:07Z","level":"ERROR","process":"devbridge","component":"runtime","event":"run.failed","runId":"run-2","errorCode":"failed"}`,
	}, "\n")+"\n")
	// The incomplete final JSONL record intentionally has no newline.
	writeLog(t, filepath.Join(dir, "devbridge-mcp.jsonl"), `{"schemaVersion":1,"timestamp":"2026-01-02T03:04:06Z","level":"INFO","process":"devbridge-mcp","component":"mcp","event":"mcp.tool.succeeded","runId":"run-1"}`+"\n"+`{"schemaVersion":1,"timestamp":"2030-01-01T00:00:00Z","level":"INFO","process":"devbridge-mcp","component":"mcp","event":"incomplete.valid.json","runId":"run-1"}`)

	reader := &Reader{logDirectory: dir}
	page, err := reader.ListLogs(context.Background(), LogListFilter{Limit: 500, RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Process != "devbridge-mcp" || page.Items[1].Process != "devbridge" {
		t.Fatalf("combined recent filtered entries = %#v", page.Items)
	}
	if page.Items[0].source != "" || page.Items[0].line != 0 {
		t.Fatalf("private source metadata exposed: %#v", page.Items[0])
	}
}

func TestListLogsLimitIsBoundedAndDoesNotUseRequestPaths(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, filepath.Join(dir, "devbridge.jsonl"), `{"schemaVersion":1,"timestamp":"2026-01-02T03:04:05Z","level":"INFO","process":"devbridge","component":"core","event":"run.created"}`+"\n")
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	writeLog(t, outside, `{"schemaVersion":1,"timestamp":"2030-01-01T00:00:00Z","level":"ERROR","process":"outside","component":"secret","event":"should.not.read"}`+"\n")
	reader := &Reader{logDirectory: dir}
	if _, err := reader.ListLogs(context.Background(), LogListFilter{Limit: MaxLogLimit + 1}); err == nil {
		t.Fatal("expected capped limit error")
	}
	page, err := reader.ListLogs(context.Background(), LogListFilter{})
	if err != nil || page.Limit != DefaultLogLimit || len(page.Items) != 1 || page.Items[0].Process != "devbridge" {
		t.Fatalf("fixed-directory read = %#v, %v", page, err)
	}
}

func TestListLogsIncludesOnlyFixedRotatedGenerations(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, filepath.Join(dir, "devbridge.jsonl.1"), `{"schemaVersion":1,"timestamp":"2026-01-02T03:04:05Z","level":"INFO","process":"devbridge","component":"run","event":"run.succeeded"}`+"\n")
	writeLog(t, filepath.Join(dir, "devbridge-mcp.jsonl.5"), `{"schemaVersion":1,"timestamp":"2026-01-02T03:04:06Z","level":"INFO","process":"devbridge-mcp","component":"mcp","event":"mcp.tool.succeeded"}`+"\n")
	writeLog(t, filepath.Join(dir, "devbridge.jsonl.6"), `{"schemaVersion":1,"timestamp":"2030-01-01T00:00:00Z","level":"ERROR","process":"outside","component":"secret","event":"should.not.read"}`+"\n")
	page, err := (&Reader{logDirectory: dir}).ListLogs(context.Background(), LogListFilter{Limit: 10})
	if err != nil || len(page.Items) != 2 || page.Items[0].Process != "devbridge-mcp" || page.Items[1].Event != "run.succeeded" {
		t.Fatalf("fixed rotated generations = %#v, %v", page, err)
	}
}

func writeLog(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
