package controlapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/bridgehttp"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/projectregistration"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/repositoryunlink"
)

type unlinkProductionFixture struct {
	ctx      context.Context
	root     string
	repoPath string
	plane    *controlplane.ControlPlane
	facade   *controlapi.Facade
	project  string
	repo     repository.Repository
}

func newUnlinkProductionFixture(t *testing.T) *unlinkProductionFixture {
	t.Helper()
	f := &unlinkProductionFixture{ctx: context.Background(), root: t.TempDir()}
	f.repoPath = filepath.Join(f.root, "source")
	if err := os.MkdirAll(f.repoPath, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = f.repoPath
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return string(bytes.TrimSpace(out))
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(f.repoPath, "tracked.txt"), []byte("immutable git identity\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked.txt")
	git("-c", "user.name=unlink fixture", "-c", "user.email=unlink@example.invalid", "commit", "-m", "initial")
	var err error
	f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: filepath.Join(f.root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.plane.Close() })
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := f.plane.ProjectRegistration.Register(f.ctx, projectregistration.Input{RequestID: "register", Name: "unlink project", Repositories: []projectregistration.RepositoryInput{{Path: f.repoPath, Alias: "source"}}})
	if err != nil {
		t.Fatal(err)
	}
	f.project, f.repo = registered.Project.ID, registered.Repositories[0]
	return f
}

func prepareUnlink(t *testing.T, f *unlinkProductionFixture, requestID string) controlapi.RepositoryUnlinkProjection {
	t.Helper()
	value, err := f.facade.PrepareRepositoryUnlink(f.ctx, controlapi.RepositoryUnlinkPrepareInput{RequestID: requestID, ProjectID: f.project, RepositoryID: f.repo.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !value.UnlinkEligibility.CanUnlink || value.RegistrationState != repository.RegistrationActive {
		t.Fatalf("unexpected prepare: %+v", value)
	}
	return value
}

func commitUnlink(t *testing.T, f *unlinkProductionFixture, requestID string, prepared controlapi.RepositoryUnlinkProjection) controlapi.RepositoryUnlinkProjection {
	t.Helper()
	value, err := f.facade.CommitRepositoryUnlink(f.ctx, controlapi.RepositoryUnlinkCommitInput{RequestID: requestID, ProjectID: f.project, RepositoryID: f.repo.ID, ExpectedGeneration: prepared.RegistrationGeneration, ExpectedTopologyRevision: prepared.UnlinkEligibility.ProjectTopologyRevision, Reason: "remove from current development resources"})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestRepositoryUnlinkProductionSafetyReplayAndProjection(t *testing.T) {
	f := newUnlinkProductionFixture(t)
	headBefore := runGit(t, f.repoPath, "rev-parse", "HEAD")
	refsBefore := runGit(t, f.repoPath, "show-ref")
	trackedBefore, err := os.ReadFile(filepath.Join(f.repoPath, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	prepared := prepareUnlink(t, f, "unlink-safe")
	result := commitUnlink(t, f, "unlink-safe", prepared)
	if result.RegistrationState != repository.RegistrationUnlinked || result.RegistrationGeneration != prepared.RegistrationGeneration+1 || result.UnlinkEligibility.ProjectTopologyRevision != prepared.UnlinkEligibility.ProjectTopologyRevision+1 || result.UnlinkedAt == nil || !result.HistoryAvailable || len(result.AvailableActions) != 0 {
		t.Fatalf("bad result projection: %+v", result)
	}
	if got := runGit(t, f.repoPath, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("HEAD changed: %s -> %s", headBefore, got)
	}
	if got := runGit(t, f.repoPath, "show-ref"); got != refsBefore {
		t.Fatalf("refs changed:\n%s\n->\n%s", refsBefore, got)
	}
	trackedAfter, err := os.ReadFile(filepath.Join(f.repoPath, "tracked.txt"))
	if err != nil || !bytes.Equal(trackedBefore, trackedAfter) {
		t.Fatalf("working tree changed: %q %v", trackedAfter, err)
	}
	active, err := f.plane.Repositories.List(f.ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("active inventory=%+v err=%v", active, err)
	}
	history, err := f.facade.ListRepositoryUnlinkProjects(f.ctx, f.project, true)
	if err != nil || len(history) != 1 || history[0].RepositoryID != f.repo.ID || history[0].RepositoryPath != f.repo.Path {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	if err := f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: filepath.Join(f.root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	replay := commitUnlink(t, f, "unlink-safe", prepared)
	if !reflect.DeepEqual(result.UnlinkEligibility, replay.UnlinkEligibility) {
		t.Fatalf("response-loss replay differs:\nfirst=%+v\nreplay=%+v", result, replay)
	}
	var receipts, events int
	if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM project_repository_unlink_receipts WHERE request_id='unlink-safe'`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM events WHERE type='project.repository_unlinked' AND entity_id=?`, f.repo.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || events != 1 {
		t.Fatalf("receipt/event multiplicity=%d/%d", receipts, events)
	}
	if _, err := f.facade.CommitRepositoryUnlink(f.ctx, controlapi.RepositoryUnlinkCommitInput{RequestID: "unlink-safe", ProjectID: f.project, RepositoryID: f.repo.ID, ExpectedGeneration: prepared.RegistrationGeneration, ExpectedTopologyRevision: prepared.UnlinkEligibility.ProjectTopologyRevision, Reason: "changed payload"}); err == nil || !errors.Is(err, repositoryunlink.ErrRequestConflict) {
		t.Fatalf("changed replay err=%v", err)
	}
}

func TestRepositoryUnlinkProductionStaleConcurrentAndPathUnavailable(t *testing.T) {
	t.Run("stale admission and missing path", func(t *testing.T) {
		f := newUnlinkProductionFixture(t)
		prepared := prepareUnlink(t, f, "unlink-stale")
		second := filepath.Join(f.root, "second")
		if err := os.MkdirAll(second, 0o700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "init", "-b", "main")
		cmd.Dir = second
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("init second git: %s: %v", out, err)
		}
		if err := os.WriteFile(filepath.Join(second, "tracked.txt"), []byte("second\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "tracked.txt"}, {"-c", "user.name=unlink fixture", "-c", "user.email=unlink@example.invalid", "commit", "-m", "initial"}} {
			cmd = exec.Command("git", args...)
			cmd.Dir = second
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("second git %v: %s: %v", args, out, err)
			}
		}
		if _, err := f.plane.ProjectRegistration.Register(f.ctx, projectregistration.Input{RequestID: "append", ProjectID: f.project, Repositories: []projectregistration.RepositoryInput{{Path: second, Alias: "second"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.facade.CommitRepositoryUnlink(f.ctx, controlapi.RepositoryUnlinkCommitInput{RequestID: "unlink-stale", ProjectID: f.project, RepositoryID: f.repo.ID, ExpectedGeneration: prepared.RegistrationGeneration, ExpectedTopologyRevision: prepared.UnlinkEligibility.ProjectTopologyRevision, Reason: "stale"}); err == nil || !errors.Is(err, repositoryunlink.ErrStale) {
			t.Fatalf("stale commit err=%v", err)
		}
		prepared = prepareUnlink(t, f, "unlink-path-missing")
		moved := f.repoPath + "-moved"
		if err := os.Rename(f.repoPath, moved); err != nil {
			t.Fatal(err)
		}
		result := commitUnlink(t, f, "unlink-path-missing", prepared)
		if result.RepositoryPath != f.repo.Path || result.RegistrationState != repository.RegistrationUnlinked {
			t.Fatalf("path-unavailable result=%+v", result)
		}
	})
	t.Run("different request concurrency", func(t *testing.T) {
		f := newUnlinkProductionFixture(t)
		prepared := prepareUnlink(t, f, "prepare-race")
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, id := range []string{"race-a", "race-b"} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				<-start
				_, err := f.facade.CommitRepositoryUnlink(f.ctx, controlapi.RepositoryUnlinkCommitInput{RequestID: id, ProjectID: f.project, RepositoryID: f.repo.ID, ExpectedGeneration: prepared.RegistrationGeneration, ExpectedTopologyRevision: prepared.UnlinkEligibility.ProjectTopologyRevision, Reason: "race"})
				errs <- err
			}(id)
		}
		close(start)
		wg.Wait()
		close(errs)
		success, rejected := 0, 0
		for err := range errs {
			if err == nil {
				success++
			} else if errors.Is(err, repositoryunlink.ErrAlreadyUnlinked) || errors.Is(err, repositoryunlink.ErrStale) {
				rejected++
			} else {
				t.Fatalf("unexpected race error: %v", err)
			}
		}
		if success != 1 || rejected != 1 {
			t.Fatalf("race success/rejected=%d/%d", success, rejected)
		}
		var receipts int
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM project_repository_unlink_receipts`).Scan(&receipts); err != nil || receipts != 1 {
			t.Fatalf("receipts=%d err=%v", receipts, err)
		}
	})
	t.Run("same request concurrency", func(t *testing.T) {
		f := newUnlinkProductionFixture(t)
		prepared := prepareUnlink(t, f, "same-race")
		start := make(chan struct{})
		results := make(chan controlapi.RepositoryUnlinkProjection, 2)
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				value, err := f.facade.CommitRepositoryUnlink(f.ctx, controlapi.RepositoryUnlinkCommitInput{RequestID: "same-race", ProjectID: f.project, RepositoryID: f.repo.ID, ExpectedGeneration: prepared.RegistrationGeneration, ExpectedTopologyRevision: prepared.UnlinkEligibility.ProjectTopologyRevision, Reason: "same race"})
				results <- value
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("same-request race err=%v", err)
			}
		}
		var first *controlapi.RepositoryUnlinkProjection
		for value := range results {
			if first == nil {
				copy := value
				first = &copy
			} else if !reflect.DeepEqual(first.UnlinkEligibility, value.UnlinkEligibility) {
				t.Fatalf("same-request results differ: %+v %+v", *first, value)
			}
		}
		var receipts, events int
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM project_repository_unlink_receipts`).Scan(&receipts); err != nil {
			t.Fatal(err)
		}
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM events WHERE type='project.repository_unlinked'`).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if receipts != 1 || events != 1 {
			t.Fatalf("same-request receipt/event=%d/%d", receipts, events)
		}
	})
}

func TestRepositoryUnlinkProductionPreservesCrossProjectIdentityAndHTTPHistory(t *testing.T) {
	f := newUnlinkProductionFixture(t)
	prepared := prepareUnlink(t, f, "unlink-http")
	commitUnlink(t, f, "unlink-http", prepared)
	if _, err := f.plane.ProjectRegistration.Register(f.ctx, projectregistration.Input{RequestID: "reuse-other", Name: "other", Repositories: []projectregistration.RepositoryInput{{Path: f.repoPath, Alias: "other"}}}); err == nil || !errors.Is(err, repository.ErrUnlinkedHistoryExists) {
		t.Fatalf("cross-project path reuse err=%v", err)
	}
	if _, err := f.plane.ProjectRegistration.Register(f.ctx, projectregistration.Input{RequestID: "reuse-original", ProjectID: f.project, Repositories: []projectregistration.RepositoryInput{{Path: f.repoPath, Alias: "source-again"}}}); err == nil || !errors.Is(err, repository.ErrUnlinkedHistoryExists) {
		t.Fatalf("same-project path reuse err=%v", err)
	}
	server, err := bridgehttp.New(f.plane, false)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/repository-unlink/list?project_id="+f.project+"&include_historical=true", nil)
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}))
	req.RemoteAddr = "127.0.0.1:45678"
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		ProjectID    string                                  `json:"projectId"`
		Repositories []controlapi.RepositoryUnlinkProjection `json:"repositories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ProjectID != f.project || len(body.Repositories) != 1 || body.Repositories[0].RegistrationState != repository.RegistrationUnlinked || !body.Repositories[0].HistoryAvailable || len(body.Repositories[0].AvailableActions) != 0 {
		t.Fatalf("HTTP projection=%+v", body)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return string(bytes.TrimSpace(out))
}
