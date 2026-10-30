package repository

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/reviewaccess"
)

type validator struct{}

func (validator) RepositoryRoot(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = path
	out, err := cmd.Output()
	return string(bytes.TrimSpace(out)), err
}

func (validator) ResolveHEAD(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "HEAD^{commit}")
	cmd.Dir = path
	out, err := cmd.Output()
	return string(out), err
}

type reviewSearcher struct {
	called  bool
	matches []bridgegit.SearchMatch
}

func (s *reviewSearcher) RepositoryRoot(context.Context, string) (string, error) {
	return "/source", nil
}

func (s *reviewSearcher) ResolveHEAD(context.Context, string) (string, error) {
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
}
func (s *reviewSearcher) SearchFixedRoot(context.Context, *reviewaccess.Root, string, []string) ([]bridgegit.SearchMatch, error) {
	s.called = true
	return append([]bridgegit.SearchMatch(nil), s.matches...), nil
}

func TestRegisterGetListAndCanonicalDuplicate(t *testing.T) {
	repo := initRepo(t)
	next := 0
	service := NewService(NewMemoryStore(), validator{}, func() (string, error) { next++; return "repo-test-" + string(rune('0'+next)), nil })
	first, err := service.Register(context.Background(), repo, "primary")
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(repo, ".")
	duplicate, err := service.Register(context.Background(), alias, "different")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != first.ID || duplicate.Name != "primary" {
		t.Fatalf("duplicate=%+v first=%+v", duplicate, first)
	}
	got, err := service.Get(context.Background(), first.ID)
	if err != nil || got != first {
		t.Fatalf("get=%+v err=%v", got, err)
	}
	list, err := service.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
}

func TestRegisterResolvesSelectedSubdirectoryToRepositoryRoot(t *testing.T) {
	repo := initRepo(t)
	subdirectory := filepath.Join(repo, "nested", "child")
	if err := os.MkdirAll(subdirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(), validator{}, func() (string, error) { return "repo-root", nil })
	value, err := service.Register(context.Background(), subdirectory, "")
	if err != nil {
		t.Fatal(err)
	}
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if value.Path != canonicalRepo || value.Name != filepath.Base(canonicalRepo) {
		t.Fatalf("repository = %+v, root = %q", value, repo)
	}
}
func TestRegisterRejectsInvalidPaths(t *testing.T) {
	service := NewService(NewMemoryStore(), validator{}, func() (string, error) { return "id", nil })
	_, err := service.Register(context.Background(), filepath.Join(t.TempDir(), "missing"), "")
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("missing path error = %v", err)
	}
	plain := t.TempDir()
	_, err = service.Register(context.Background(), plain, "")
	if !errors.Is(err, ErrNotGit) {
		t.Fatalf("non-Git path error = %v", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = service.Register(context.Background(), file, "")
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("file error = %v", err)
	}
}
func TestGetMissing(t *testing.T) {
	_, err := NewMemoryStore().Get(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestMemoryStoreRejectsIDCollision(t *testing.T) {
	store := NewMemoryStore()
	if _, _, err := store.PutIfAbsent(context.Background(), Repository{ID: "same", Path: "/one"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutIfAbsent(context.Background(), Repository{ID: "same", Path: "/two"}); !errors.Is(err, ErrIDExists) {
		t.Fatalf("collision = %v", err)
	}
}

func TestReviewSearchDoesNotSemanticallyFilterTestLikePathsOrResults(t *testing.T) {
	repo := initRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "tests"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tests", "fixture.go"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tests/fixture.go"}, {"commit", "-m", "add test-like fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = repo
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	store := NewMemoryStore()
	if _, _, err := store.PutIfAbsent(context.Background(), Repository{ID: "repo-review", ProjectID: "project-review", Name: "Review", Path: repo}); err != nil {
		t.Fatal(err)
	}
	git := &reviewSearcher{matches: []bridgegit.SearchMatch{{Path: "service.go", Line: 1, Snippet: "needle"}, {Path: "service_test.go", Line: 2, Snippet: "needle"}}}
	service := NewService(store, git, func() (string, error) { return "unused", nil })
	if _, err := service.SearchReview(context.Background(), "repo-review", "needle", []string{"../outside"}, 10); !errors.Is(err, reviewaccess.ErrScopeForbidden) || git.called {
		t.Fatalf("structural traversal search err=%v gitCalled=%t", err, git.called)
	}
	if _, err := service.SearchReview(context.Background(), "repo-review", "needle", []string{"tests"}, 10); err != nil || !git.called {
		t.Fatalf("test-like search path received semantic rejection: err=%v gitCalled=%t", err, git.called)
	}
	git.called = false
	result, err := service.SearchReview(context.Background(), "repo-review", "needle", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !git.called || len(result.Matches) != 2 || result.Matches[1].Path != "service_test.go" {
		t.Fatalf("repository result=%+v gitCalled=%t", result, git.called)
	}
}
func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	cmd = exec.Command("git", "config", "user.email", "devbridge@example.test")
	cmd.Dir = repo
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "config", "user.name", "Dev Bridge Test")
	cmd.Dir = repo
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README"}, {"commit", "-m", "initial"}} {
		cmd = exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return repo
}

func TestRegistrationDistinguishesEmptyHEADFromNonGit(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git", "-C", root, "init", "--initial-branch=main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated Git fixture: %v %s", err, out)
	}
	service := NewService(NewMemoryStore(), bridgegit.NewClient(bridgegit.Config{}), nil)
	if _, err := service.ResolveRegistrationPath(context.Background(), root); !errors.Is(err, ErrHEADUnavailable) || errors.Is(err, ErrNotGit) {
		t.Fatalf("empty HEAD classification: %v", err)
	}
}
