package controlapi_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

// startupContext makes the fixture's durable Session context usable without
// launching discovery. Repository/module/configuration inventories remain the
// real temporary control-plane stores.
type startupContext struct{ *aicontext.Service }

func (s startupContext) GetSession(ctx context.Context, id string) (aicontext.SessionContext, error) {
	v, err := s.Service.GetSession(ctx, id)
	if err == nil {
		v.ProjectContext = projectcontext.ProjectContext{ProjectID: v.Project.ID, Status: projectcontext.StatusNoGuidance}
	}
	return v, err
}

// startupRunner is deliberately a fake runtime handoff: it returns the
// reserved durable Run and never invokes a Coding Agent.
type startupRunner struct{}

func (startupRunner) StartReview(ctx context.Context, _ string, reserve func(context.Context) (run.Run, error)) (run.Run, error) {
	return reserve(ctx)
}
func (startupRunner) StartReservedReview(_ context.Context, _ string, value run.Run) (run.Run, error) {
	return value, nil
}

type startupScopeReviews struct {
	*scopereview.Service
	mu       sync.Mutex
	failNext error
	starts   int
}

func (s *startupScopeReviews) StartAdmitted(ctx context.Context, input scopereview.AdmittedStartInput) (review.StartAdmission, error) {
	s.mu.Lock()
	s.starts++
	err := s.failNext
	s.failNext = nil
	s.mu.Unlock()
	if err != nil {
		return review.StartAdmission{}, err
	}
	return s.Service.StartAdmitted(ctx, input)
}

type startupTasks struct {
	*task.Service
	mu       sync.Mutex
	failNext error
}

func (s *startupTasks) Create(ctx context.Context, input task.CreateInput) (task.Task, error) {
	s.mu.Lock()
	err := s.failNext
	s.failNext = nil
	s.mu.Unlock()
	if err != nil {
		return task.Task{}, err
	}
	return s.Service.Create(ctx, input)
}

type startupFixture struct {
	ownershipWorkflowFixture
	facade *controlapi.Facade
	scope  *startupScopeReviews
	tasks  *startupTasks
}

func newStartupFixture(t *testing.T) startupFixture {
	return newStartupFixtureWithBranch(t, true)
}

func newStartupFixtureWithBranch(t *testing.T, selectBranch bool) startupFixture {
	t.Helper()
	base := newOwnershipWorkflowFixture(t, featurearea.StatusDiscovering)
	initializePresentationRepository(t, base)
	if selectBranch {
		head := repositoryHead(t, base, "HEAD")
		if _, err := base.facade.SelectDevelopmentBranchForSession(base.ctx, developmentcycle.SelectInput{AISessionID: base.sessionID, RepositoryID: base.repositoryID, DevelopmentRef: "refs/heads/master", RequestID: "startup-select", ProjectID: base.projectID}); err != nil {
			// The initial branch name can differ by platform; select the actual HEAD ref.
			if _, retry := base.facade.SelectDevelopmentBranchForSession(base.ctx, developmentcycle.SelectInput{AISessionID: base.sessionID, RepositoryID: base.repositoryID, DevelopmentRef: "refs/heads/main", RequestID: "startup-select-main", ProjectID: base.projectID}); retry != nil {
				t.Fatalf("select development branch (head %s): %v / %v", head, err, retry)
			}
		}
	}
	ctx := startupContext{base.plane.AIContext}
	scopeService, err := scopereview.NewService(base.plane.AISessions, base.plane.Repositories, base.plane.Runs, startupRunner{}, sqlite.NewReviewStore(base.plane.DB))
	if err != nil {
		t.Fatal(err)
	}
	scopeService.SetReviews(base.plane.Reviews)
	scopeService.SetInitialGrantService(base.plane.ReviewGrants)
	scope := &startupScopeReviews{Service: scopeService}
	tasks := &startupTasks{Service: base.plane.Tasks}
	p := base.plane
	facade, err := controlapi.New(controlapi.Dependencies{StartupContext: ctx, DevelopmentCycles: p.DevelopmentCycles, Projects: p.Projects, ProjectRegistration: p.ProjectRegistration, ProjectSelection: p.ProjectSelection, AISessions: p.AISessions, Repositories: p.Repositories, Modules: p.Modules, ProjectContexts: p.ProjectContexts, ContextProbes: p.ContextProbes, ContextProbeRunner: p.ContextProbeRunner, Classifications: p.Classifications, FeatureAreas: p.FeatureAreas, AgentConversations: p.AgentConversations, Documents: p.Documents, DecisionResolutions: p.DecisionResolutions, Reviews: p.Reviews, Tasks: tasks, TaskPlans: p.TaskPlans, ScopeReviews: scope, ScopeExpansions: p.ScopeExpansions, ExecutionProposals: p.ExecutionProposals, ExecutionCancellations: p.ExecutionCancellations, ExecutionGoals: p.ExecutionGoals, Runs: p.Runs, Workspaces: p.Workspaces, Evidence: p.Evidence, Artifacts: p.Artifacts, Events: p.Events, Approvals: p.Approvals, Runner: p.Supervisor, ExecutionAllowlist: p.CodexRuntime})
	if err != nil {
		t.Fatal(err)
	}
	return startupFixture{ownershipWorkflowFixture: base, facade: facade, scope: scope, tasks: tasks}
}

func (f startupFixture) input(requestID string) controlapi.FeatureAreaCreateInput {
	return controlapi.FeatureAreaCreateInput{AISessionID: f.sessionID, Title: "Initial review composition", ProblemStatement: "Investigate the first formal task without changing the repository.", Mode: controlapi.CreateAndReview, RequestID: requestID, InitialTask: &controlapi.InitialTaskInput{Title: "Inspect startup composition", ProblemStatement: "Investigate the first formal task without changing the repository.", Constraints: []string{"Read-only"}, RepositoryID: f.repositoryID}, InitialReview: &controlapi.InitialReviewInput{ReviewDepth: "DEEP", ReviewPlan: controlapi.InitialReviewPlan{Mode: "REPOSITORY"}, ReviewInstruction: "Inspect the production startup path. Do not modify repository files.", Model: "gpt-5.6-luna"}}
}

func startupCounts(t *testing.T, f startupFixture) (areas, tasks, reviews, runs, executions, revisions int) {
	t.Helper()
	if err := f.plane.DB.SQL().QueryRowContext(context.Background(), `SELECT (SELECT COUNT(*) FROM feature_areas), (SELECT COUNT(*) FROM tasks), (SELECT COUNT(*) FROM reviews), (SELECT COUNT(*) FROM runs), (SELECT COUNT(*) FROM execution_proposals), (SELECT COUNT(*) FROM task_definition_revisions)`).Scan(&areas, &tasks, &reviews, &runs, &executions, &revisions); err != nil {
		t.Fatal(err)
	}
	return
}

func TestFeatureAreaStartupCompositionUsesDurableObjectsAndReplaysTerminalReview(t *testing.T) {
	f := newStartupFixture(t)
	beforeA, beforeT, beforeR, beforeRun, beforeE, beforeRev := startupCounts(t, f)
	first, err := f.facade.CreateFeatureAreaWithInitialReview(f.ctx, f.input("startup-normal"))
	if err != nil || first.Startup == nil || !first.Startup.ReviewAdmitted || first.Startup.TaskID == "" || first.Startup.ReviewID == "" || first.Startup.RunID == "" {
		t.Fatalf("startup=%+v err=%v", first, err)
	}
	a, tasks, reviews, runs, executions, revisions := startupCounts(t, f)
	if a != beforeA+1 || tasks != beforeT+1 || reviews != beforeR+1 || runs != beforeRun+1 || executions != beforeE || revisions != beforeRev+1 {
		t.Fatalf("counts=%d/%d/%d/%d/%d/%d", a, tasks, reviews, runs, executions, revisions)
	}
	assertInitialAuthoredDraft(t, f.ctx, f.facade, f.sessionID, first.Startup.TaskID)
	linked, err := f.plane.Runs.Get(context.Background(), first.Startup.RunID)
	if err != nil || linked.TaskID != first.Startup.TaskID || linked.ReviewID != first.Startup.ReviewID || linked.Purpose != run.PurposeScopeReview || linked.OperationKind != run.OperationReview {
		t.Fatalf("incorrect read-only Run ownership: %+v %v", linked, err)
	}
	claim, err := sqlite.NewAgentConversationStore(f.plane.DB).GetClaim(context.Background(), first.FeatureArea.ID)
	if err != nil || claim.RunID != linked.ID || claim.OperationID != first.Startup.ReviewID || string(claim.Kind) != "REVIEW" {
		t.Fatalf("incorrect canonical claim: %+v %v", claim, err)
	}
	if _, err := f.plane.DB.SQL().ExecContext(context.Background(), `UPDATE runs SET status='SUCCEEDED' WHERE id=?`, first.Startup.RunID); err != nil {
		t.Fatal(err)
	}
	// A completed receipt replays durable facts before re-reading mutable branch
	// context; a moved current HEAD cannot rewrite it into a new Review.
	repositoryGit(t, f.ownershipWorkflowFixture, "commit", "--allow-empty", "-m", "move branch after startup")
	replay, err := f.facade.CreateFeatureAreaWithInitialReview(f.ctx, f.input("startup-normal"))
	if err != nil || replay.Startup.TaskID != first.Startup.TaskID || replay.Startup.ReviewID != first.Startup.ReviewID || replay.Startup.RunID != first.Startup.RunID {
		t.Fatalf("terminal replay=%+v err=%v", replay, err)
	}
	if gotA, gotT, gotR, gotRun, _, _ := startupCounts(t, f); gotA != a || gotT != tasks || gotR != reviews || gotRun != runs {
		t.Fatalf("replay duplicated durable objects: %d/%d/%d/%d", gotA, gotT, gotR, gotRun)
	}
}

func TestFeatureAreaStartupCompositionResumesTaskAndReviewFailures(t *testing.T) {
	f := newStartupFixture(t)
	f.tasks.failNext = errors.New("task write interrupted")
	partial, err := f.facade.CreateFeatureAreaWithInitialReview(f.ctx, f.input("startup-task-retry"))
	if err != nil || partial.Startup == nil || partial.Startup.FailedStep != "TASK_CREATE" || partial.Startup.TaskID != "" {
		t.Fatalf("task partial=%+v err=%v", partial.Startup, err)
	}
	resumed, err := f.facade.CreateFeatureAreaWithInitialReview(f.ctx, f.input("startup-task-retry"))
	if err != nil || !resumed.Startup.ReviewAdmitted {
		t.Fatalf("task resume=%+v err=%v", resumed, err)
	}

	f.scope.failNext = errors.New("review admission interrupted")
	partial, err = f.facade.CreateFeatureAreaWithInitialReview(f.ctx, f.input("startup-review-retry"))
	if err != nil || partial.Startup == nil || partial.Startup.FailedStep != "REVIEW_ADMISSION" || partial.Startup.TaskID == "" || partial.Startup.ReviewAdmitted {
		t.Fatalf("review partial=%+v err=%v", partial.Startup, err)
	}
	resumed, err = f.facade.CreateFeatureAreaWithInitialReview(f.ctx, f.input("startup-review-retry"))
	if err != nil || !resumed.Startup.ReviewAdmitted || resumed.Startup.TaskID != partial.Startup.TaskID {
		t.Fatalf("review resume=%+v err=%v", resumed, err)
	}
}

func TestFeatureAreaStartupCompositionRejectsInvalidModelBeforeAreaAndConvergesConcurrentRoot(t *testing.T) {
	f := newStartupFixture(t)
	beforeA, beforeT, beforeR, beforeRun, _, _ := startupCounts(t, f)
	invalid := f.input("startup-invalid-model")
	invalid.InitialReview.Model = "not-enabled"
	if _, err := f.facade.CreateFeatureAreaWithInitialReview(f.ctx, invalid); err == nil {
		t.Fatal("invalid model created startup")
	}
	if a, tasks, reviews, runs, _, _ := startupCounts(t, f); a != beforeA || tasks != beforeT || reviews != beforeR || runs != beforeRun {
		t.Fatalf("invalid model left durable objects: %d/%d/%d/%d", a, tasks, reviews, runs)
	}

	in := f.input("startup-concurrent")
	results := make([]controlapi.FeatureAreaCreateResult, 2)
	errs := make([]error, 2)
	var group sync.WaitGroup
	for i := range results {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			results[index], errs[index] = f.facade.CreateFeatureAreaWithInitialReview(f.ctx, in)
		}(i)
	}
	group.Wait()
	for i, err := range errs {
		if err != nil || results[i].Startup == nil || !results[i].Startup.ReviewAdmitted {
			t.Fatalf("concurrent[%d]=%+v err=%v", i, results[i].Startup, err)
		}
	}
	if results[0].FeatureArea.ID != results[1].FeatureArea.ID || results[0].Startup.TaskID != results[1].Startup.TaskID || results[0].Startup.ReviewID != results[1].Startup.ReviewID || results[0].Startup.RunID != results[1].Startup.RunID {
		t.Fatalf("concurrent roots diverged: %+v / %+v", results[0].Startup, results[1].Startup)
	}
	if a, tasks, reviews, runs, _, _ := startupCounts(t, f); a != beforeA+1 || tasks != beforeT+1 || reviews != beforeR+1 || runs != beforeRun+1 {
		t.Fatalf("concurrent root duplicated objects: %d/%d/%d/%d", a, tasks, reviews, runs)
	}
}

func TestFeatureAreaStartupCreateOnlyCompatibilityAndNestedRejection(t *testing.T) {
	f := newStartupFixture(t)
	beforeA, beforeT, beforeR, beforeRun, _, _ := startupCounts(t, f)
	legacy, err := f.facade.CreateFeatureAreaWithInitialReview(f.ctx, controlapi.FeatureAreaCreateInput{AISessionID: f.sessionID, Title: "Legacy only", ProblemStatement: "Record without review."})
	if err != nil || legacy.Startup != nil {
		t.Fatalf("legacy create=%+v err=%v", legacy, err)
	}
	if a, tasks, reviews, runs, _, _ := startupCounts(t, f); a != beforeA+1 || tasks != beforeT || reviews != beforeR || runs != beforeRun {
		t.Fatalf("legacy create started work: %d/%d/%d/%d", a, tasks, reviews, runs)
	}
	request := controlapi.FeatureAreaCreateInput{AISessionID: f.sessionID, Title: "Receipted only", ProblemStatement: "Record without review.", Mode: controlapi.CreateOnly, RequestID: "startup-only"}
	first, err := f.facade.CreateFeatureAreaWithInitialReview(f.ctx, request)
	second, replayErr := f.facade.CreateFeatureAreaWithInitialReview(f.ctx, request)
	if err != nil || replayErr != nil || first.FeatureArea.ID != second.FeatureArea.ID || first.Startup == nil || second.Startup == nil {
		t.Fatalf("create-only replay=%+v/%+v err=%v/%v", first, second, err, replayErr)
	}
	bad := request
	bad.InitialTask = f.input("nested-unused").InitialTask
	if _, err := f.facade.CreateFeatureAreaWithInitialReview(f.ctx, bad); err == nil {
		t.Fatal("CREATE_ONLY accepted nested startup input")
	}
	if a, tasks, reviews, runs, _, _ := startupCounts(t, f); a != beforeA+2 || tasks != beforeT || reviews != beforeR || runs != beforeRun {
		t.Fatalf("create-only changed work inventory: %d/%d/%d/%d", a, tasks, reviews, runs)
	}
}

func TestFeatureAreaStartupPreflightFailuresCreateNothing(t *testing.T) {
	cases := []struct {
		name          string
		withoutBranch bool
		mutate        func(controlapi.FeatureAreaCreateInput) controlapi.FeatureAreaCreateInput
	}{
		{"missing repository", false, func(v controlapi.FeatureAreaCreateInput) controlapi.FeatureAreaCreateInput {
			v.InitialTask.RepositoryID = "missing"
			return v
		}},
		{"missing instruction", false, func(v controlapi.FeatureAreaCreateInput) controlapi.FeatureAreaCreateInput {
			v.InitialReview.ReviewInstruction = ""
			return v
		}},
		{"bad depth", false, func(v controlapi.FeatureAreaCreateInput) controlapi.FeatureAreaCreateInput {
			v.InitialReview.ReviewDepth = "INVALID"
			return v
		}},
		{"bad focused plan", false, func(v controlapi.FeatureAreaCreateInput) controlapi.FeatureAreaCreateInput {
			v.InitialReview.ReviewPlan = controlapi.InitialReviewPlan{Mode: "FOCUSED"}
			return v
		}},
		{"unknown session", false, func(v controlapi.FeatureAreaCreateInput) controlapi.FeatureAreaCreateInput {
			v.AISessionID = "unknown-session"
			return v
		}},
		{"branch not selected", true, func(v controlapi.FeatureAreaCreateInput) controlapi.FeatureAreaCreateInput { return v }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newStartupFixtureWithBranch(t, !tc.withoutBranch)
			beforeA, beforeT, beforeR, beforeRun, _, _ := startupCounts(t, f)
			if _, err := f.facade.CreateFeatureAreaWithInitialReview(f.ctx, tc.mutate(f.input("startup-preflight-"+tc.name))); err == nil {
				t.Fatal("preflight unexpectedly succeeded")
			}
			if a, tasks, reviews, runs, _, _ := startupCounts(t, f); a != beforeA || tasks != beforeT || reviews != beforeR || runs != beforeRun {
				t.Fatalf("preflight persisted objects: %d/%d/%d/%d", a, tasks, reviews, runs)
			}
		})
	}
}

func TestFeatureAreaStartupRootRejectsDifferentRawPromptWithoutNewEntities(t *testing.T) {
	f := newStartupFixture(t)
	first, err := f.facade.CreateFeatureAreaWithInitialReview(f.ctx, f.input("startup-prompt-conflict"))
	if err != nil {
		t.Fatal(err)
	}
	beforeA, beforeT, beforeR, beforeRun, _, _ := startupCounts(t, f)
	conflict := f.input("startup-prompt-conflict")
	conflict.InitialReview.ReviewInstruction = "Inspect the production startup path.  Do not modify repository files."
	if _, err := f.facade.CreateFeatureAreaWithInitialReview(f.ctx, conflict); err == nil {
		t.Fatal("different raw prompt reused root request")
	}
	if a, tasks, reviews, runs, _, _ := startupCounts(t, f); a != beforeA || tasks != beforeT || reviews != beforeR || runs != beforeRun {
		t.Fatalf("prompt conflict created entities after %s: %d/%d/%d/%d", first.FeatureArea.ID, a, tasks, reviews, runs)
	}
}
