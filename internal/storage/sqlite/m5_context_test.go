package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/repository"
)

func TestM5ContextStateProbeAndModuleDescriptionAreDurable(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repoPath := t.TempDir()
	now := time.Date(2026, 9, 4, 4, 0, 0, 0, time.UTC)
	db, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewProjectStore(db).Create(ctx, project.Project{ID: "project", Name: "P", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewRepositoryStore(db).PutIfAbsent(ctx, repository.Repository{ID: "repo", ProjectID: "project", Name: "R", Path: filepath.Clean(repoPath), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	seedBoundSession(t, db, aisession.Session{ID: "session", ProjectID: "project", CreatedAt: now, UpdatedAt: now})
	moduleService := module.NewService(NewModuleStore(db), NewProjectStore(db), func() (string, error) { return "unclassified", nil }, func() time.Time { return now })
	unclassified, err := moduleService.EnsureUnclassified(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if unclassified.Description == "" || !unclassified.SystemOwned {
		t.Fatalf("unclassified=%+v", unclassified)
	}
	contexts := projectcontext.NewService(NewProjectContextStore(db), func() time.Time { now = now.Add(time.Second); return now })
	probes, err := contextprobe.NewService(NewContextProbeStore(db), NewAISessionStore(db), NewRepositoryStore(db), NewCodexConfigStore(db), contexts, func() (string, error) { return "probe", nil }, func() time.Time { now = now.Add(time.Second); return now })
	if err != nil {
		t.Fatal(err)
	}
	instruction := "  exact bytes\nare durable  "
	probe, err := probes.Create(ctx, contextprobe.CreateInput{AISessionID: "session", RepositoryID: "repo", CodexConfigID: "codex_config_default", Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}, CodexInstruction: instruction})
	if err != nil {
		t.Fatal(err)
	}
	probe, err = probes.Start(ctx, probe.ID)
	if err != nil {
		t.Fatal(err)
	}
	promptSentAt := now.Add(time.Second)
	probe, err = probes.RecordPrompt(ctx, probe.ID, instruction, promptSentAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probes.RecordPrompt(ctx, probe.ID, instruction+"changed", promptSentAt); !errors.Is(err, contextprobe.ErrConflict) {
		t.Fatalf("mutable Context Probe prompt error=%v", err)
	}
	probe, err = probes.AttachThread(ctx, probe.ID, "context-thread")
	if err != nil {
		t.Fatal(err)
	}
	probe, err = probes.AttachTurn(ctx, probe.ID, "context-turn")
	if err != nil {
		t.Fatal(err)
	}
	probe, err = probes.NoGuidance(ctx, probe.ID, "no root guidance", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Status != contextprobe.StatusNoGuidance {
		t.Fatalf("probe=%+v", probe)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := NewContextProbeStore(db).Get(ctx, "probe")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(instruction))
	if got.CodexInstruction != instruction || got.FinalCodexPrompt != instruction || got.PromptSHA256 != hex.EncodeToString(sum[:]) || got.PromptBytes != int64(len(instruction)) || got.PromptSentAt == nil || got.Status != contextprobe.StatusNoGuidance || got.ThreadID != "context-thread" || got.TurnID != "context-turn" || got.Result != "no root guidance" {
		t.Fatalf("durable probe=%+v", got)
	}
	contextValue, err := NewProjectContextStore(db).Get(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if contextValue.Status != projectcontext.StatusNoGuidance || contextValue.LatestProbeID != "probe" {
		t.Fatalf("durable context=%+v", contextValue)
	}
	listed, err := NewModuleStore(db).ListByProject(ctx, "project")
	if err != nil || len(listed) != 1 || listed[0].Description == "" || !listed[0].SystemOwned {
		t.Fatalf("modules=%+v err=%v", listed, err)
	}
}
