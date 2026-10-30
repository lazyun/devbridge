package contextprobe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
)

type sessionLookup map[string]aisession.Session

func (s sessionLookup) Get(_ context.Context, id string) (aisession.Session, error) {
	value, ok := s[id]
	if !ok {
		return value, aisession.ErrNotFound
	}
	return value, nil
}

type repositoryLookup map[string]repository.Repository

func (r repositoryLookup) Get(_ context.Context, id string) (repository.Repository, error) {
	value, ok := r[id]
	if !ok {
		return value, repository.ErrNotFound
	}
	return value, nil
}

type configLookup map[string]codexconfig.Config

func (c configLookup) Get(_ context.Context, id string) (codexconfig.Config, error) {
	value, ok := c[id]
	if !ok {
		return value, codexconfig.ErrNotFound
	}
	return value, nil
}

func TestCreatePreservesInstructionAndValidatesCanonicalScope(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root guidance"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 1, 2, 3, 0, time.UTC)
	contexts := projectcontext.NewService(projectcontext.NewMemoryStore(), func() time.Time { return now })
	service, err := NewService(NewMemoryStore(), sessionLookup{"session": {ID: "session", ProjectID: "project"}}, repositoryLookup{"repo": {ID: "repo", ProjectID: "project", Path: root}}, configLookup{"config": {ID: "config", Enabled: true}}, contexts, func() (string, error) { return "probe", nil }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for _, empty := range []string{"", " \n\t"} {
		if _, err := service.Create(ctx, CreateInput{AISessionID: "session", RepositoryID: "repo", CodexConfigID: "config", Scope: Scope{Kind: ScopeRepository}, CodexInstruction: empty}); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("empty instruction accepted: %v", err)
		}
	}
	instruction := "  Read only the named guidance.\nReturn your exact findings.  "
	value, err := service.Create(ctx, CreateInput{AISessionID: "session", RepositoryID: "repo", CodexConfigID: "config", Scope: Scope{Kind: ScopePaths, Paths: []string{"AGENTS.md", "pkg"}}, CodexInstruction: instruction})
	if err != nil {
		t.Fatal(err)
	}
	if value.CodexInstruction != instruction {
		t.Fatalf("instruction changed: %q", value.CodexInstruction)
	}
	if value.Status != StatusCreated || len(value.Scope.Paths) != 2 {
		t.Fatalf("probe = %+v", value)
	}
	contextValue, err := contexts.Get(ctx, "project")
	if err != nil || contextValue.Status != projectcontext.StatusRequired {
		t.Fatalf("context = %+v err=%v", contextValue, err)
	}

	for name, scope := range map[string]Scope{
		"parent":                {Kind: ScopePaths, Paths: []string{"../secret"}},
		"absolute":              {Kind: ScopePaths, Paths: []string{filepath.Join(outside, "secret")}},
		"symlink escape":        {Kind: ScopePaths, Paths: []string{"escape"}},
		"empty paths":           {Kind: ScopePaths},
		"repository with paths": {Kind: ScopeRepository, Paths: []string{"AGENTS.md"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.Create(ctx, CreateInput{AISessionID: "session", RepositoryID: "repo", CodexConfigID: "config", Scope: scope, CodexInstruction: "inspect"})
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestValidateScopeAcceptsFileDirectoryAndNestedDirectoryWithoutScanning(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "guidance.md"), []byte("guidance"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pkg", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An authorized directory may contain arbitrary descendants. Validation
	// deliberately checks only the named boundary and does not enumerate it.
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "pkg", "nested", "unvisited-escape")); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"regular file":     "guidance.md",
		"directory":        "pkg",
		"nested directory": "pkg/nested",
	} {
		t.Run(name, func(t *testing.T) {
			scope, err := validateScope(root, Scope{Kind: ScopePaths, Paths: []string{path}})
			if err != nil {
				t.Fatalf("validate %q: %v", path, err)
			}
			if len(scope.Paths) != 1 || scope.Paths[0] != path {
				t.Fatalf("scope=%+v", scope)
			}
		})
	}
}

func TestValidateScopeRejectsMissingAbsoluteTraversalEscapeAndCanonicalDuplicates(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "pkg", "nested"), filepath.Join(root, "nested-alias")); err != nil {
		t.Fatal(err)
	}

	for name, paths := range map[string][]string{
		"nonexistent":                    {"missing"},
		"absolute":                       {secret},
		"parent traversal":               {"pkg/../secret"},
		"symlink escape":                 {"escape"},
		"lexically normalized duplicate": {"pkg/nested", "pkg/./nested"},
		"canonical symlink duplicate":    {"pkg/nested", "nested-alias"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateScope(root, Scope{Kind: ScopePaths, Paths: paths})
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("paths=%q error=%v", paths, err)
			}
		})
	}
}

func TestProbeLifecycleUpdatesProjectContextAndIndependentUsage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 9, 4, 1, 0, 0, 0, time.UTC)
	contexts := projectcontext.NewService(projectcontext.NewMemoryStore(), func() time.Time { now = now.Add(time.Second); return now })
	service, err := NewService(NewMemoryStore(), sessionLookup{"session": {ID: "session", ProjectID: "project"}}, repositoryLookup{"repo": {ID: "repo", ProjectID: "project", Path: root}}, configLookup{"config": {ID: "config", Enabled: true}}, contexts, func() (string, error) { return "probe", nil }, func() time.Time { now = now.Add(time.Second); return now })
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.Create(ctx, CreateInput{AISessionID: "session", RepositoryID: "repo", CodexConfigID: "config", Scope: Scope{Kind: ScopeRepository}, CodexInstruction: "inspect only what is needed"})
	if err != nil {
		t.Fatal(err)
	}
	value, err = service.Start(ctx, value.ID)
	if err != nil || value.Status != StatusRunning {
		t.Fatalf("start = %+v err=%v", value, err)
	}
	if _, err := service.AttachThread(ctx, value.ID, "thread"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AttachTurn(ctx, value.ID, "turn"); err != nil {
		t.Fatal(err)
	}
	usage := &run.TokenUsageObservation{StartCumulative: run.TokenCounts{TotalTokens: 10}, EndCumulative: run.TokenCounts{TotalTokens: 17}, Reliable: true}
	value, err = service.Succeed(ctx, value.ID, "bounded result", "artifact", "sha256:abc", usage)
	if err != nil {
		t.Fatal(err)
	}
	if value.Status != StatusSucceeded || value.TokenUsage.Status != run.TokenUsageAvailable || value.TokenUsage.Usage == nil || value.TokenUsage.Usage.TotalTokens != 7 {
		t.Fatalf("finished = %+v", value)
	}
	contextValue, err := contexts.Get(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if contextValue.Status != projectcontext.StatusReady || contextValue.LatestProbeID != value.ID || contextValue.Result != "bounded result" {
		t.Fatalf("context = %+v", contextValue)
	}
}
