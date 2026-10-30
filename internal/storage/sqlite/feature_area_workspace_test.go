package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workspace"
)

func TestFeatureAreaWorkspaceCanonicalOwnerConcurrentCreationAndReopen(t *testing.T) {
	f := newAgentStorageFixture(t, "canonical-workspace")
	ctx := context.Background()
	service := workspace.NewService(NewWorkspaceStore(f.db), nil, nil)
	values := make(chan workspace.Workspace, 12)
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := service.EnsureForFeatureArea(ctx, f.project.ID, f.task.RepositoryID, f.area.ID, f.task.BaseCommit)
			values <- v
			errs <- e
		}()
	}
	wg.Wait()
	close(values)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id string
	for v := range values {
		if id == "" {
			id = v.ID
		}
		if v.ID != id {
			t.Fatal("concurrent creation published two workspaces")
		}
	}
	for _, q := range []string{
		`UPDATE feature_areas SET status='COMPLETE' WHERE id=?`,
		`UPDATE feature_areas SET current_workspace_id=NULL WHERE id=?`,
	} {
		if _, e := f.db.SQL().Exec(q, f.area.ID); e == nil {
			t.Fatal("invalid FeatureArea mutation accepted", q)
		}
	}
	for _, q := range []string{
		`UPDATE workspaces SET project_id='foreign' WHERE id=?`,
		`UPDATE workspaces SET repository_id='foreign' WHERE id=?`,
		`UPDATE workspaces SET feature_area_id='foreign' WHERE id=?`,
		`UPDATE workspaces SET base_commit='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' WHERE id=?`,
	} {
		if _, e := f.db.SQL().Exec(q, id); e == nil {
			t.Fatal("workspace ownership mutation accepted", q)
		}
	}
	if _, e := service.EnsureForFeatureArea(ctx, "foreign", f.task.RepositoryID, f.area.ID, f.task.BaseCommit); e == nil {
		t.Fatal("foreign Project borrowed workspace")
	}
	if e := f.db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e := Open(ctx, f.root)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	value, e := workspace.NewService(NewWorkspaceStore(db), nil, nil).EnsureForFeatureArea(ctx, f.project.ID, f.task.RepositoryID, f.area.ID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if e != nil || value.ID != id || value.BaseCommit != f.task.BaseCommit {
		t.Fatalf("reopen replaced canonical workspace: %+v %v", value, e)
	}
	area, e := NewFeatureAreaStore(db).Get(ctx, f.area.ID)
	if e != nil || area.CurrentWorkspaceID != id {
		t.Fatalf("pointer lost: %+v %v", area, e)
	}
	var obsolete int
	if e := db.SQL().QueryRow(`SELECT count(*) FROM pragma_table_info('workspaces') WHERE name IN ('task_id','review_id','execution_id','run_id')`).Scan(&obsolete); e != nil || obsolete != 0 {
		t.Fatalf("dual owner columns=%d %v", obsolete, e)
	}
}

func TestFeatureAreaTerminalRacesNextTaskAdmissionOnSameWorkspace(t *testing.T) {
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			f := newReviewApprovalFixture(t)
			ctx := context.Background()
			old := f.start(t)
			ws, e := workspace.NewService(NewWorkspaceStore(f.db), nil, nil).EnsureForFeatureArea(ctx, f.project, f.candidate.RepositoryID, f.area, f.candidate.BaseCommit)
			if e != nil {
				t.Fatal(e)
			}
			scope := run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}
			start := make(chan struct{})
			finished := make(chan error, 1)
			admitted := make(chan review.StartAdmission, 1)
			admissionErr := make(chan error, 1)
			go func() {
				<-start
				_, e := NewRunStore(f.db).Update(ctx, old.Run.ID, func(v *run.Run) error {
					v.Status = run.StatusSucceeded
					v.UpdatedAt = f.now
					v.Result = &run.Result{AgentClaim: evidence.AgentClaim{Message: "Read-only inspection complete"}}
					return nil
				})
				finished <- e
			}()
			go func() {
				<-start
				a, e := f.reviews.AdmitStart(ctx, f.candidate, "next-task-race", strings.Repeat("b", 64), f.candidate.Instruction, scope, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope})
				admitted <- a
				admissionErr <- e
			}()
			close(start)
			if e := <-finished; e != nil {
				t.Fatal(e)
			}
			next, e := <-admitted, <-admissionErr
			if e != nil {
				var busy *review.AdmissionBusyError
				if !errors.As(e, &busy) {
					t.Fatal(e)
				}
				next, e = f.reviews.AdmitStart(ctx, f.candidate, "next-task-after-quiescence", strings.Repeat("c", 64), f.candidate.Instruction, scope, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope})
				if e != nil {
					t.Fatal(e)
				}
			}
			current, e := NewWorkspaceStore(f.db).GetByFeatureArea(ctx, f.area)
			if e != nil || current.ID != ws.ID {
				t.Fatalf("terminal/admission race replaced workspace: %+v %v", current, e)
			}
			var claims, tasks int
			if e = f.db.SQL().QueryRow(`SELECT COUNT(*) FROM feature_area_agent_claims WHERE feature_area_id=?`, f.area).Scan(&claims); e != nil || claims != 1 {
				t.Fatalf("claims=%d %v", claims, e)
			}
			if e = f.db.SQL().QueryRow(`SELECT COUNT(*) FROM tasks WHERE feature_area_id=?`, f.area).Scan(&tasks); e != nil || tasks != 1 {
				t.Fatalf("phantom next Tasks=%d %v", tasks, e)
			}
			claim, e := NewAgentConversationStore(f.db).GetClaim(ctx, f.area)
			if e != nil || claim.RunID != next.Run.ID || next.Review.TaskID != old.Review.TaskID {
				t.Fatalf("wrong next owner: %+v %+v %v", claim, next, e)
			}
		})
	}
}
