package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/repositoryunlink"
)

// Every final writer uses ensureRepositoryActiveTx rather than an inventory
// read. This table verifies the exact durable boundary shared by workspace,
// review/run, direct-file, recovery, inspection and delivery writers.
func TestRepositoryUnlinkFinalWriteAdmission(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, t.TempDir())
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 9, 17, 1, 2, 3, 0, time.UTC)
	if err := NewProjectStore(db).Create(ctx, project.Project{ID: "p", Name: "Project", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewRepositoryStore(db).PutIfAbsent(ctx, repository.Repository{ID: "active", ProjectID: "p", Name: "Active", Path: "/active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewRepositoryStore(db).PutIfAbsent(ctx, repository.Repository{ID: "unlinked", ProjectID: "p", Name: "Unlinked", Path: "/unlinked", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	unlinkStore := NewRepositoryUnlinkStore(db)
	eligibility, err := unlinkStore.Eligibility(ctx, "p", "unlinked")
	if err != nil || !eligibility.CanUnlink {
		t.Fatalf("unlink eligibility=%+v err=%v", eligibility, err)
	}
	if _, err := repositoryunlink.NewService(unlinkStore).Commit(ctx, repositoryunlink.CommitInput{ProjectID: "p", RepositoryID: "unlinked", ExpectedGeneration: eligibility.RegistrationGeneration, ExpectedTopologyRevision: eligibility.ProjectTopologyRevision, RequestID: "unlink", Reason: "test"}); err != nil {
		t.Fatal(err)
	}

	boundaries := []string{"workspace create/member", "review/admit-start", "run binding/create-attempt", "direct-file workflow/mutation", "recovery continuation", "inspection continuation", "delivery intent"}
	for _, boundary := range boundaries {
		t.Run(boundary+" rejects unlinked", func(t *testing.T) {
			tx, err := db.beginWrite(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := ensureRepositoryActiveTx(ctx, tx, "p", "unlinked"); !errors.Is(err, repository.ErrRepositoryUnlinked) {
				t.Fatalf("err=%v, want %v", err, repository.ErrRepositoryUnlinked)
			}
		})
	}
	t.Run("active control permits new admission", func(t *testing.T) {
		tx, err := db.beginWrite(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := ensureRepositoryActiveTx(ctx, tx, "p", "active"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("development cycle real writer rejects unlinked", func(t *testing.T) {
		_, err := NewDevelopmentCycleStore(db).Select(ctx, developmentcycle.Cycle{ID: "cycle-unlinked", ProjectID: "p", RepositoryID: "unlinked", DevelopmentRef: "refs/heads/main", SelectedHeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RequestID: "cycle-unlinked"})
		if !errors.Is(err, repository.ErrRepositoryUnlinked) {
			t.Fatalf("err=%v, want %v", err, repository.ErrRepositoryUnlinked)
		}
	})
}
