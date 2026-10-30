package codexconfig

import (
	"context"
	"testing"
	"time"
)

func TestCreateGetList(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store, func() (string, error) { return "config", nil }, func() time.Time { return time.Unix(1, 0) })
	value, err := service.Create(context.Background(), CreateInput{Name: "Primary", CodexPath: "/opt/codex/bin/codex", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if value.CodexPath != "/opt/codex/bin/codex" || !value.Enabled {
		t.Fatalf("config=%+v", value)
	}
	got, err := service.Get(context.Background(), value.ID)
	if err != nil || got != value {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	values, err := service.List(context.Background())
	if err != nil || len(values) != 1 {
		t.Fatalf("values=%+v err=%v", values, err)
	}
}

func TestCreateRejectsRelativeCodexPath(t *testing.T) {
	service := NewService(NewMemoryStore(), func() (string, error) { return "config", nil }, nil)
	if _, err := service.Create(context.Background(), CreateInput{Name: "Bad", CodexPath: "relative", Enabled: true}); err == nil {
		t.Fatal("relative Codex path accepted")
	}
}
