package controlplane

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/dataroot"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/recovery"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

func TestConcurrentFreshControlPlaneBootstrapHasOneWriter(t *testing.T) {
	ctx := context.Background()
	for iteration := 0; iteration < 4; iteration++ {
		root := filepath.Join(t.TempDir(), "fresh-data-root")
		start := make(chan struct{})
		type result struct {
			plane *ControlPlane
			err   error
		}
		results := make(chan result, 2)
		for i := 0; i < 2; i++ {
			go func() {
				<-start
				plane, err := Open(ctx, Config{DataRoot: root})
				results <- result{plane: plane, err: err}
			}()
		}
		close(start)
		first, second := <-results, <-results
		var winner *ControlPlane
		var loserErr error
		for _, candidate := range []result{first, second} {
			if candidate.err == nil {
				if winner != nil || candidate.plane == nil || !candidate.plane.Ready {
					t.Fatalf("iteration %d invalid winner result=%+v", iteration, candidate)
				}
				winner = candidate.plane
			} else {
				loserErr = candidate.err
			}
		}
		if winner == nil || !errors.Is(loserErr, dataroot.ErrWriterLocked) {
			if first.plane != nil {
				_ = first.plane.Close()
			}
			if second.plane != nil {
				_ = second.plane.Close()
			}
			t.Fatalf("iteration %d first=%v second=%v, want one DATA_ROOT_WRITER_LOCKED", iteration, first.err, second.err)
		}
		paths, _ := dataroot.Derive(root)
		if _, err := dataroot.Open(root); !errors.Is(err, dataroot.ErrWriterLocked) {
			_ = winner.Close()
			t.Fatalf("iteration %d competing layout open not fenced: %v", iteration, err)
		}
		created := createClaimedPreparingRun(t, winner)
		if got, err := winner.Runs.Get(ctx, created.ID); err != nil || got.Status != run.StatusPreparing {
			_ = winner.Close()
			t.Fatalf("iteration %d winner durable write=%+v err=%v", iteration, got, err)
		}
		if marker, err := os.ReadFile(filepath.Join(paths.Root, dataroot.MarkerFilename)); err != nil || string(marker) != "{\n  \"formatVersion\": 1\n}\n" {
			_ = winner.Close()
			t.Fatalf("iteration %d marker=%q err=%v", iteration, marker, err)
		}
		if err := winner.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestControlPlaneWriterLockPrecedesRecovery(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "shared-data-root")
	first, err := Open(ctx, Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	firstClosed := false
	t.Cleanup(func() {
		if !firstClosed {
			_ = first.Close()
		}
	})
	epoch := first.DB.RuntimeEpoch()
	active := createClaimedPreparingRun(t, first)

	second, err := Open(ctx, Config{DataRoot: root})
	if second != nil || !errors.Is(err, dataroot.ErrWriterLocked) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second Open plane=%v err=%v, want writer lock conflict", second, err)
	}
	got, err := first.Runs.Get(ctx, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != run.StatusPreparing {
		t.Fatalf("failed competing Open ran recovery: status=%s", got.Status)
	}
	var authority, claimEpoch string
	if err := first.DB.SQL().QueryRow(`SELECT epoch_id FROM runtime_authority WHERE singleton=1`).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	if err := first.DB.SQL().QueryRow(`SELECT runtime_epoch FROM feature_area_agent_claims WHERE run_id=?`, active.ID).Scan(&claimEpoch); err != nil {
		t.Fatal(err)
	}
	if authority != epoch || got.RuntimeEpoch != epoch || claimEpoch != epoch {
		t.Fatalf("authority=%q Run=%q claim=%q, want %q", authority, got.RuntimeEpoch, claimEpoch, epoch)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	firstClosed = true
	restarted, err := Open(ctx, Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.DB.RuntimeEpoch() == "" || restarted.DB.RuntimeEpoch() == epoch {
		t.Fatalf("restart epoch=%q, previous=%q", restarted.DB.RuntimeEpoch(), epoch)
	}
	recovered, err := restarted.Runs.Get(ctx, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != run.StatusInfrastructureFailed || recovered.Failure == nil || recovered.Failure.Code != recovery.InterruptedCode {
		t.Fatalf("recovered Run=%+v", recovered)
	}
}

func TestControlPlanesWithDifferentDataRootsCoexist(t *testing.T) {
	ctx := context.Background()
	first, err := Open(ctx, Config{DataRoot: filepath.Join(t.TempDir(), "first")})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, Config{DataRoot: filepath.Join(t.TempDir(), "second")})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if !first.Ready || !second.Ready {
		t.Fatalf("planes ready: first=%v second=%v", first.Ready, second.Ready)
	}
	if first.DB.RuntimeEpoch() == "" || second.DB.RuntimeEpoch() == "" || first.DB.RuntimeEpoch() == second.DB.RuntimeEpoch() {
		t.Fatalf("epochs first=%q second=%q", first.DB.RuntimeEpoch(), second.DB.RuntimeEpoch())
	}
}

func createClaimedPreparingRun(t *testing.T, plane *ControlPlane) run.Run {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 7, 2, 3, 4, 0, time.UTC)
	projectValue := project.Project{ID: "project-writer", Name: "Writer", CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewProjectStore(plane.DB).Create(ctx, projectValue); err != nil {
		t.Fatal(err)
	}
	repositoryValue := repository.Repository{ID: "repo-writer", ProjectID: projectValue.ID, Name: "Repo", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if _, _, err := sqlite.NewRepositoryStore(plane.DB).PutIfAbsent(ctx, repositoryValue); err != nil {
		t.Fatal(err)
	}
	ref := aiprojectselection.ClientBindingReference{CallerScope: "runtime-writer-fixture"}
	var sessionID string
	if plane.AIContext != nil {
		initialized, err := plane.AIContext.InitializeSession(ctx, ref, "runtime-writer-initialize", "")
		if err != nil {
			t.Fatal(err)
		}
		bound, err := plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, projectValue.ID, "runtime-writer-bind")
		if err != nil {
			t.Fatal(err)
		}
		sessionID = bound.Context.Session.ID
	} else {
		// Bootstrap recovery is deliberately constructed before the public plane
		// services exist.  Use the same durable Session-first store protocol.
		sessions := sqlite.NewAISessionStore(plane.DB)
		if _, err := sessions.InitializeSession(ctx, ref.CallerScope, "runtime-writer-initialize", "session-writer", now); err != nil {
			t.Fatal(err)
		}
		bound, _, _, err := sessions.BindSessionProject(ctx, ref.CallerScope, "runtime-writer-bind", "session-writer", projectValue.ID, "project_selection_session-writer", now)
		if err != nil {
			t.Fatal(err)
		}
		sessionID = bound.ID
	}
	area := featurearea.FeatureArea{ID: "area-writer", ProjectID: projectValue.ID, CreatedByAISessionID: sessionID, Title: "Writer safety", ProblemStatement: "Protect active ownership", Status: featurearea.StatusDiscovering, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewFeatureAreaStore(plane.DB).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	m := module.Module{ID: "module-writer", ProjectID: projectValue.ID, Key: "writer", Name: "Writer", CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewModuleStore(plane.DB).Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	intent := task.Task{ID: "task-writer", ProjectID: projectValue.ID, FeatureAreaID: area.ID, ModuleID: m.ID, RepositoryID: repositoryValue.ID, CodexConfigID: codexconfig.DefaultConfigID, Model: "gpt-5.6-terra", ReasoningEffort: "high", Title: "Writer intent", ProblemStatement: "Protect active ownership", BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: task.StatusActive, Phase: task.PhaseReviewRequired, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewTaskStore(plane.DB).Create(ctx, intent); err != nil {
		t.Fatal(err)
	}
	var (
		config codexconfig.Config
		err    error
	)
	if plane.CodexConfigs != nil {
		config, err = plane.CodexConfigs.Get(ctx, codexconfig.DefaultConfigID)
	} else {
		config, err = sqlite.NewCodexConfigStore(plane.DB).Get(ctx, codexconfig.DefaultConfigID)
	}
	if err != nil {
		t.Fatal(err)
	}
	selection := config.Selection("runtime-writer-fixture")
	owner := review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: intent.ID, ID: "review_writer", ProjectID: projectValue.ID, FeatureAreaID: area.ID, CreatedByAISessionID: sessionID, AuthorKind: string(review.AuthorAISession), AuthorID: sessionID, RepositoryID: repositoryValue.ID, ExecutorKind: review.ExecutorCodingAgent, CodexConfigID: codexconfig.DefaultConfigID, BackendSelection: &selection, Model: "gpt-5.6-terra", ReasoningEffort: "high", BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "Exercise writer ownership", Instruction: "Inspect exact capability", CreatedAt: now}
	if err := sqlite.NewReviewStore(plane.DB).Create(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.NewReviewGrantStore(plane.DB).Create(ctx, reviewgrant.Grant{ReviewID: owner.ID, ProjectID: owner.ProjectID, FeatureAreaID: owner.FeatureAreaID, RepositoryID: owner.RepositoryID, Mode: reviewgrant.ModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	created, err := plane.Runs.CreateFocusedScopeReviewForReview(ctx, owner.ID, owner.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository})
	if err != nil {
		t.Fatal(err)
	}
	created, err = plane.Runs.Transition(ctx, created.ID, run.StatusPreparing)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestBorrowedBootstrapOwnershipOutlivesControlPlane(t *testing.T) {
	root, err := dataroot.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	plane, err := OpenWithDataRoot(context.Background(), Config{DataRoot: root.Paths.Root}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := plane.Close(); err != nil {
		t.Fatal(err)
	}
	// The process still owns final logger flush/close after the plane shuts down.
	if !root.WriterHeld() {
		t.Fatal("plane released process bootstrap ownership")
	}
	if other, err := Open(context.Background(), Config{DataRoot: root.Paths.Root}); !errors.Is(err, dataroot.ErrWriterLocked) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("contender: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if plane, err := OpenWithDataRoot(context.Background(), Config{}, root); err == nil {
		plane.Close()
		t.Fatal("closed ownership accepted")
	}
	next, err := Open(context.Background(), Config{DataRoot: root.Paths.Root})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
}

func TestControlPlaneBootstrapFailureReleasesOwnedRoot(t *testing.T) {
	root := t.TempDir()
	opened, err := dataroot.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	// Force SQLite open to fail after DataRoot ownership has been acquired.
	dbPath := filepath.Join(opened.Paths.State, "devbridge.db")
	if err := os.Mkdir(dbPath, 0700); err != nil {
		t.Fatal(err)
	}
	opened.Close()
	if plane, err := Open(context.Background(), Config{DataRoot: root}); err == nil {
		plane.Close()
		t.Fatal("invalid database accepted")
	}
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}
	next, err := Open(context.Background(), Config{DataRoot: root})
	if err != nil {
		t.Fatalf("retry after SQLite failure: %v", err)
	}
	defer next.Close()
}

func TestBorrowedBootstrapRejectsMismatchedPaths(t *testing.T) {
	root, err := dataroot.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	other := t.TempDir()
	if plane, err := OpenWithDataRoot(context.Background(), Config{DataRoot: other}, root); err == nil {
		plane.Close()
		t.Fatal("mismatched config accepted")
	}
	forged := root
	forged.Paths.State = other
	if plane, err := OpenWithDataRoot(context.Background(), Config{}, forged); err == nil {
		plane.Close()
		t.Fatal("changed paths accepted")
	}
	if entries, err := os.ReadDir(other); err != nil || len(entries) != 0 {
		t.Fatalf("mismatched root mutated: %v %v", entries, err)
	}
}
