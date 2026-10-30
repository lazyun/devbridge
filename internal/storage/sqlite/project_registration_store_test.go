package sqlite

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/projectregistration"
	"github.com/lazyun/devbridge/internal/repository"
)

func registrationGitFixture(t *testing.T, root string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--initial-branch=main"}, {"config", "user.email", "fixture@example.invalid"}, {"config", "user.name", "Isolated fixture"}, {"commit", "--allow-empty", "-m", "baseline"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture Git: %v %s", err, out)
		}
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
func registrationService(t *testing.T, db *DB) *projectregistration.Service {
	t.Helper()
	s, err := projectregistration.NewService(project.NewService(NewProjectStore(db), nil, nil), repository.NewService(NewRepositoryStore(db), bridgegit.NewClient(bridgegit.Config{}), nil), NewProjectRegistrationStore(db))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func registrationCount(t *testing.T, db *DB, table string) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestProjectCentricRegistrationAtomicReplayAndOwnership(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := openTestDB(t, root)
	defer db.Close()
	s := registrationService(t, db)
	app := registrationGitFixture(t, filepath.Join(root, "one", "same"))
	sdk := registrationGitFixture(t, filepath.Join(root, "two", "same"))
	backend := registrationGitFixture(t, filepath.Join(root, "backend"))
	input := projectregistration.Input{RequestID: "create-multi", Name: "Logical project", Repositories: []projectregistration.RepositoryInput{{Path: app}, {Path: sdk}, {Path: backend}}}
	bad := input
	bad.RequestID = "invalid-batch"
	bad.Repositories = append([]projectregistration.RepositoryInput{}, input.Repositories...)
	bad.Repositories[2].Path = root
	if _, err := s.Register(ctx, bad); !errors.Is(err, repository.ErrNotGit) {
		t.Fatalf("invalid batch error: %v", err)
	}
	if registrationCount(t, db, "projects") != 0 || registrationCount(t, db, "repositories") != 0 || registrationCount(t, db, "project_registration_receipts") != 0 {
		t.Fatal("invalid batch partially persisted")
	}
	got, err := s.Register(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Repositories) != 3 || got.Repository.ID != "" || got.Repositories[0].Name == got.Repositories[1].Name {
		t.Fatalf("multi registration %+v", got)
	}
	// Replay is durable and precedes fresh Git/path validation, including loss of a source path.
	if err := os.Rename(app, app+"-offline"); err != nil {
		t.Fatal(err)
	}
	replay, err := s.Register(ctx, input)
	if err != nil || !reflect.DeepEqual(got, replay) {
		t.Fatalf("response-loss replay: %v %+v", err, replay)
	}
	if err := os.Rename(app+"-offline", app); err != nil {
		t.Fatal(err)
	}
	changed := input
	changed.Name = "different"
	if _, err = s.Register(ctx, changed); !errors.Is(err, projectregistration.ErrRequestConflict) {
		t.Fatalf("payload conflict: %v", err)
	}
	empty, err := s.Register(ctx, projectregistration.Input{RequestID: "empty", Name: "No Git root"})
	if err != nil || len(empty.Repositories) != 0 {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	if _, err = s.Register(ctx, projectregistration.Input{RequestID: "cross", ProjectID: empty.Project.ID, Repositories: []projectregistration.RepositoryInput{{Path: app}}}); !errors.Is(err, repository.ErrPathConflict) {
		t.Fatalf("cross ownership: %v", err)
	}
	appendInput := projectregistration.Input{RequestID: "append-existing", ProjectID: got.Project.ID, Repositories: []projectregistration.RepositoryInput{{Path: app}}}
	existing, err := s.Register(ctx, appendInput)
	if err != nil || len(existing.Repositories) != 1 || existing.Repositories[0].ID != got.Repositories[0].ID {
		t.Fatalf("existing member: %+v %v", existing, err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(app, alias); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Register(ctx, projectregistration.Input{RequestID: "dup", Name: "duplicate", Repositories: []projectregistration.RepositoryInput{{Path: app}, {Path: alias}}}); !errors.Is(err, projectregistration.ErrInvalidArgument) {
		t.Fatalf("symlink duplicate: %v", err)
	}
	if registrationCount(t, db, "repositories") != 3 {
		t.Fatal("ownership changed")
	}
}

func TestProjectCentricRegistrationRollbackAndConcurrentRequest(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := openTestDB(t, root)
	defer db.Close()
	s := registrationService(t, db)
	repo := registrationGitFixture(t, filepath.Join(root, "repo"))
	input := projectregistration.Input{RequestID: "stable", Name: "Atomic", Repositories: []projectregistration.RepositoryInput{{Path: repo}}}
	if _, err := db.sql.Exec(`CREATE TRIGGER injected_registration_failure BEFORE INSERT ON repositories BEGIN SELECT RAISE(ABORT,'injected'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(ctx, input); err == nil {
		t.Fatal("expected injected failure")
	}
	for _, table := range []string{"projects", "repositories", "project_registration_receipts", "events"} {
		if n := registrationCount(t, db, table); n != 0 {
			t.Fatalf("partial %s=%d", table, n)
		}
	}
	if _, err := db.sql.Exec(`DROP TRIGGER injected_registration_failure`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	values := make([]projectregistration.Registration, 8)
	errs := make([]error, 8)
	for i := range values {
		wg.Add(1)
		go func(i int) { defer wg.Done(); values[i], errs[i] = s.Register(ctx, input) }(i)
	}
	wg.Wait()
	for i := range values {
		if errs[i] != nil || !reflect.DeepEqual(values[0], values[i]) {
			t.Fatalf("replay %d: %v %+v", i, errs[i], values[i])
		}
	}
	if registrationCount(t, db, "projects") != 1 || registrationCount(t, db, "repositories") != 1 || registrationCount(t, db, "project_registration_receipts") != 1 {
		t.Fatal("duplicate business write")
	}
}

func TestProjectRegistrationReceiptSurvivesRestartAndAppend(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := openTestDB(t, root)
	input := projectregistration.Input{RequestID: "empty-restart", Name: "Logical"}
	first, err := registrationService(t, db).Register(ctx, input)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDB(t, root)
	defer db.Close()
	s := registrationService(t, db)
	replay, err := s.Register(ctx, input)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("durable restart replay %+v %v", replay, err)
	}
	repo := registrationGitFixture(t, filepath.Join(root, "newly-added"))
	appendInput := projectregistration.Input{RequestID: "append-new", ProjectID: first.Project.ID, Repositories: []projectregistration.RepositoryInput{{Path: repo, Alias: "app"}}}
	appended, err := s.Register(ctx, appendInput)
	if err != nil || appended.Project.ID != first.Project.ID || len(appended.Repositories) != 1 || appended.Repositories[0].Name != "app" {
		t.Fatalf("append %+v %v", appended, err)
	}
	if registrationCount(t, db, "projects") != 1 || registrationCount(t, db, "repositories") != 1 {
		t.Fatal("append created a second Project")
	}
	if _, err = db.sql.Exec(`DELETE FROM project_registration_receipts WHERE request_id=?`, input.RequestID); err == nil {
		t.Fatal("receipt ownership evidence deleted")
	}
}
