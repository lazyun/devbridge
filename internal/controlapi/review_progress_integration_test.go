package controlapi_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

type reviewProgressObservations struct {
	*sqlite.AgentConversationStore
	reads      int
	beforeRead func()
	readError  error
}

type reviewProgressRunner struct{}

func (reviewProgressRunner) StartRun(context.Context, string) (run.Run, error) {
	return run.Run{}, nil
}
func (reviewProgressRunner) CancelRun(context.Context, string) (run.Run, error) {
	return run.Run{}, nil
}

func (s *reviewProgressObservations) State(ctx context.Context, id string) (agentconversation.State, error) {
	s.reads++
	if s.readError != nil {
		return agentconversation.State{}, s.readError
	}
	if s.beforeRead != nil {
		hook := s.beforeRead
		s.beforeRead = nil
		hook()
	}
	return s.AgentConversationStore.State(ctx, id)
}

func TestReviewProgressCurrentOwnershipUsesOneAuthoritativeSnapshot(t *testing.T) {
	f := newOwnershipWorkflowFixture(t, featurearea.StatusActive)
	ctx := f.ctx
	initializePresentationRepository(t, f)
	semantic := f.createTask(t)
	create := func(id string) review.Review {
		t.Helper()
		value, err := f.plane.Reviews.Create(ctx, f.codingReview(t, review.Review{ReviewDepth: "DEEP", TaskID: semantic.ID, ID: id, ProjectID: f.projectID, FeatureAreaID: f.areaID, CreatedByAISessionID: f.sessionID, RepositoryID: f.repositoryID, CodexConfigID: codexconfig.DefaultConfigID, BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "Review progress", Instruction: "Inspect source"}))
		if err != nil {
			t.Fatal(err)
		}
		reserved, err := sqlite.NewRunStore(f.plane.DB).CreateProductScopeReviewAttempt(ctx, value.ID, id+"-run", value.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository}, f.now)
		if err != nil {
			t.Fatal(err)
		}
		value.RunID = reserved.ID
		return value
	}
	setStatus := func(id string, status run.Status) {
		t.Helper()
		// Durable fixture setup only: no production runtime is started.
		if _, err := f.plane.DB.SQL().ExecContext(ctx, `UPDATE runs SET status=? WHERE id=?`, status, id); err != nil {
			t.Fatal(err)
		}
	}
	release := func(id string) {
		t.Helper()
		if _, err := f.plane.DB.SQL().ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE run_id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	old := create("review_completed_history")
	setStatus(old.RunID, run.StatusSucceeded)
	release(old.RunID)
	var active review.Review
	observed := &reviewProgressObservations{AgentConversationStore: f.plane.AgentConversations}
	// Commit a new admission between the history list and ownership read.
	// The exact owner must be loaded even though it was absent from that list.
	observed.beforeRead = func() { active = create("review_current_owner") }
	plane := f.plane
	var err error
	f.facade, err = controlapi.New(controlapi.Dependencies{DevelopmentCycles: plane.DevelopmentCycles, Projects: plane.Projects, ProjectRegistration: plane.ProjectRegistration, ProjectSelection: plane.ProjectSelection, AISessions: plane.AISessions, Repositories: plane.Repositories, Modules: plane.Modules, ProjectContexts: plane.ProjectContexts, ContextProbes: plane.ContextProbes, ContextProbeRunner: plane.ContextProbeRunner, Classifications: plane.Classifications, FeatureAreas: plane.FeatureAreas, AgentConversations: observed, Documents: plane.Documents, DecisionResolutions: plane.DecisionResolutions, Reviews: plane.Reviews, Tasks: plane.Tasks, TaskPlans: plane.TaskPlans, ScopeReviews: plane.ScopeReviews, ScopeExpansions: plane.ScopeExpansions, ExecutionProposals: plane.ExecutionProposals, ExecutionCancellations: plane.ExecutionCancellations, ExecutionGoals: plane.ExecutionGoals, Runs: plane.Runs, Workspaces: plane.Workspaces, Evidence: plane.Evidence, Artifacts: plane.Artifacts, Events: plane.Events, Approvals: plane.Approvals, Runner: reviewProgressRunner{}, ExecutionAllowlist: plane.CodexRuntime, WorkflowProgression: plane.WorkflowProgression})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := f.facade.CurrentWorkflowForSession(ctx, f.sessionID, f.areaID)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.ActiveReview == nil || admitted.ActiveReview.ID != active.ID || admitted.LatestReview.ID != active.ID || admitted.CurrentReview.ID != active.ID {
		t.Fatalf("concurrent admission fell back to completed history: %+v", admitted)
	}
	for _, status := range []run.Status{run.StatusRunning, run.StatusSucceeded} {
		setStatus(active.RunID, status)
		before := featureAreaPresentationCounts(t, ctx, f)
		workflow, err := f.facade.CurrentWorkflowForSession(ctx, f.sessionID, f.areaID)
		if err != nil {
			t.Fatal(err)
		}
		if workflow.ActiveReview == nil || workflow.ActiveReview.ID != active.ID || workflow.CurrentReview.ID != active.ID || workflow.LatestReview.ID != active.ID || workflow.AgentOperation == nil || workflow.AgentOperation.Claim.OperationID != active.ID {
			t.Fatalf("owner selection=%+v", workflow)
		}
		readsBefore := observed.reads
		projection, err := f.facade.FeatureAreaPresentationForSession(ctx, f.sessionID, f.areaID)
		if err != nil {
			t.Fatal(err)
		}
		if observed.reads != readsBefore+1 {
			t.Fatalf("projection combined %d ownership observations", observed.reads-readsBefore)
		}
		if projection.Progress[0].Status != "CURRENT" || projection.Details.ReviewID != active.ID || (projection.CurrentPhase != "REVIEW" && projection.CurrentPhase != string(task.PhaseReviewing)) || projection.TaskStages == nil || projection.TaskStages.Phase != string(task.PhaseReviewing) || projection.CurrentOperation == nil || projection.CurrentOperation.ID != active.ID {
			t.Fatalf("active Review incorrectly completed: %+v", projection)
		}
		assertFeatureAreaPresentationCounts(t, ctx, f, before)
	}
	release(active.RunID)
	projection, err := f.facade.FeatureAreaPresentationForSession(ctx, f.sessionID, f.areaID)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Progress[0].Status != "CURRENT" || projection.Details.ReviewID != active.ID || projection.CurrentOperation != nil {
		t.Fatalf("released completed Review=%+v", projection)
	}
	historical, err := f.plane.Runs.Get(ctx, old.RunID)
	if err != nil || historical.Status != run.StatusSucceeded {
		t.Fatalf("history changed: %+v err=%v", historical, err)
	}
	observed.readError = errors.New("ownership read unavailable")
	if _, err := f.facade.FeatureAreaPresentationForSession(ctx, f.sessionID, f.areaID); err == nil {
		t.Fatal("failed ownership read silently fell back to historical completion")
	}
}
