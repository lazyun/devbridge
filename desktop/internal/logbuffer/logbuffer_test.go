package logbuffer

import (
	"strings"
	"testing"
)

func TestBufferKeepsBoundedNewestLines(t *testing.T) {
	b := New(2)
	b.Append("core", Stdout, []byte("first\nsecond\nthird\n"))
	got := b.Snapshot()
	if len(got) != 2 || got[0].Text != "second" || got[1].Text != "third" {
		t.Fatalf("Snapshot() = %#v", got)
	}
	if got[0].Source != "core" || got[0].Stream != Stdout || got[0].Time.IsZero() {
		t.Fatalf("entry metadata = %#v", got[0])
	}
}

func TestAppendSanitizesCredentials(t *testing.T) {
	b := New(8)
	b.Append("mcp", Stderr, []byte("Authorization: Bearer top-secret\napi_key=abc123 password: hunter2\n"))
	for _, entry := range b.Snapshot() {
		if strings.Contains(entry.Text, "top-secret") || strings.Contains(entry.Text, "abc123") || strings.Contains(entry.Text, "hunter2") {
			t.Fatalf("secret leaked in %q", entry.Text)
		}
		if !strings.Contains(entry.Text, "[REDACTED]") {
			t.Fatalf("expected redaction in %q", entry.Text)
		}
	}
}

func TestClearAndNonPositiveCapacity(t *testing.T) {
	b := New(0)
	b.AppendLine("core", Stdout, "one")
	b.AppendLine("core", Stdout, "two")
	if got := b.Snapshot(); len(got) != 1 || got[0].Text != "two" {
		t.Fatalf("bounded snapshot = %#v", got)
	}
	b.Clear()
	if b.Len() != 0 || len(b.Snapshot()) != 0 {
		t.Fatal("Clear did not empty buffer")
	}
}

func TestEntryLengthIsBounded(t *testing.T) {
	b := New(1)
	b.AppendLine("core", Stdout, strings.Repeat("x", maxEntryBytes+100))
	got := b.Snapshot()
	if len(got) != 1 || len(got[0].Text) > maxEntryBytes+len("…") {
		t.Fatalf("unbounded entry length: %d", len(got[0].Text))
	}
}
