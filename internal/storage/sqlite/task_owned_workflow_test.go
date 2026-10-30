package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/evidence"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func newTaskOwnedFixture(t *testing.T, projectIntent ...bool) (*reviewApprovalFixture, task.Task) {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t, t.TempDir())
	t.Cleanup(func() { db.Close() })
	p := project.Project{ID: "owned-project", Name: "Owned", CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewProjectStore(db).Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	r := repository.Repository{ID: "owned-repo", ProjectID: p.ID, Name: "Source", Path: filepath.Join(db.stateRoot, "source"), CreatedAt: testNow, UpdatedAt: testNow}
	if _, _, err := NewRepositoryStore(db).PutIfAbsent(ctx, r); err != nil {
		t.Fatal(err)
	}
	m := module.Module{ID: "owned-module", ProjectID: p.ID, Key: "core", Name: "Core", CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewModuleStore(db).Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	sid := aisession.Session{ID: "owned-session", ProjectID: p.ID, CreatedAt: testNow, UpdatedAt: testNow}
	sessions := NewAISessionStore(db)
	if _, err := sessions.InitializeSession(ctx, "owned-caller", "owned-initialize", sid.ID, testNow); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sessions.BindSessionProject(ctx, "owned-caller", "owned-bind", sid.ID, p.ID, "owned-selection", testNow); err != nil {
		t.Fatal(err)
	}
	area := featurearea.FeatureArea{ID: "owned-area", ProjectID: p.ID, CreatedByAISessionID: sid.ID, Title: "Configuration", ProblemStatement: "Configuration intent", Status: featurearea.StatusDiscovering, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewFeatureAreaStore(db).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	value := task.Task{ID: "owned-task", Title: "Reload model configuration", Type: task.TypeDevelopment, Scope: task.ScopeFeatureArea, ProjectID: p.ID, FeatureAreaID: area.ID, ModuleID: m.ID, RepositoryID: r.ID, CodexConfigID: codexconfig.DefaultConfigID, Goal: "Reload model configuration", ProblemStatement: "Keep model configuration fresh without disrupting running work", Constraints: []string{}, BaseCommit: strings.Repeat("a", 40), Status: task.StatusActive, Phase: task.PhaseReviewRequired, CreatedAt: testNow, UpdatedAt: testNow}
	if len(projectIntent) > 0 && projectIntent[0] {
		value.RepositoryID, value.BaseCommit, value.CodexConfigID = "", "", ""
	}
	if err := NewTaskStore(db).Create(ctx, value); err != nil {
		t.Fatal(err)
	}
	f := &reviewApprovalFixture{db: db, store: NewWorkflowProgressionStore(db), project: p.ID, area: area.ID, session: sid.ID, now: testNow}
	f.store.clock = acceptanceTestClock(testNow.Add(time.Hour))
	f.reviews, _ = review.NewService(NewReviewStore(db), nil, func() time.Time { return testNow })
	config, err := NewCodexConfigStore(db).Get(ctx, codexconfig.DefaultConfigID)
	if err != nil {
		t.Fatal(err)
	}
	backend := config.Selection("ISOLATED_TEST")
	f.candidate = review.Review{Model: "gpt-5.6-terra", ReasoningEffort: "high", BackendSelection: &backend, ReviewDepth: task.ReviewDepthFast, TaskID: value.ID, ProjectID: p.ID, FeatureAreaID: area.ID, CreatedByAISessionID: sid.ID, RepositoryID: r.ID, CodexConfigID: value.CodexConfigID, BaseCommit: value.BaseCommit, RepositoryProvenance: &review.RepositoryProvenance{RepositoryID: r.ID, Commit: value.BaseCommit, RepositoryHEAD: value.BaseCommit, BranchKind: review.ProvenanceBranch, BranchRef: "refs/heads/dev", InspectionMode: review.InspectionFeatureAreaWorkspace, CapturedAt: testNow}, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: value.ProblemStatement, Instruction: "Inspect read-only; this is not the Task title"}
	return f, value
}
func ownedTask(t *testing.T, f *reviewApprovalFixture) task.Task {
	t.Helper()
	v, e := NewTaskStore(f.db).Get(context.Background(), f.candidate.TaskID)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func ownedCount(t *testing.T, f *reviewApprovalFixture, table string) int {
	t.Helper()
	var n int
	if e := f.db.sql.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}

func TestTaskOwnedCreationAndReviewSuccession(t *testing.T) {
	f, initial := newTaskOwnedFixture(t)
	ctx := context.Background()
	v := ownedTask(t, f)
	if v.Status != task.StatusActive || v.Phase != task.PhaseReviewRequired || v.Title != initial.Title || v.CurrentDefinitionRevisionID == "" || v.ReviewApprovalID != "" || v.CurrentExecutionID != "" {
		t.Fatalf("new intent: %+v", v)
	}
	for _, table := range []string{"reviews", "execution_proposals"} {
		if ownedCount(t, f, table) != 0 {
			t.Fatalf("premature %s", table)
		}
	}
	first := f.start(t)
	v = ownedTask(t, f)
	if v.CurrentReviewID != first.Review.ID || v.Phase != task.PhaseReviewing || first.Review.Ordinal != 1 || first.Run.TaskID != v.ID {
		t.Fatalf("admission %+v %+v", v, first)
	}
	f.complete(t, first, "Blocked: missing architecture evidence")
	if ownedTask(t, f).Phase != task.PhaseReviewing || ownedCount(t, f, "review_approval_decisions") != 1 {
		t.Fatal("unsatisfied review advanced Task")
	}
	resolve := f.input(first, "REVISE")
	resolve.DecisionID, resolve.RequestID = wp.InvalidResultDecisionID(first.Review.ID), "resolve-invalid-first"
	resolved, err := f.store.Resolve(ctx, resolve)
	if err != nil {
		t.Fatal(err)
	}
	successorReview, err := f.reviews.Get(ctx, resolved.SuccessorReviewID)
	if err != nil {
		t.Fatal(err)
	}
	successorRun, err := NewRunStore(f.db).Get(ctx, resolved.SuccessorRunID)
	if err != nil {
		t.Fatal(err)
	}
	second := review.StartAdmission{Review: successorReview, Run: successorRun}
	if second.Review.Ordinal != 2 || second.Review.TaskID != v.ID || second.Review.ID == first.Review.ID {
		t.Fatal("Review history not Task-owned")
	}
	if err := NewReviewStore(f.db).LinkRun(ctx, first.Review.ID, second.Run.ID); !errors.Is(err, review.ErrConflict) {
		t.Fatalf("Review rebound Run: %v", err)
	}
	f.complete(t, second, approvedResult(t))
	if ownedTask(t, f).Phase != task.PhaseReviewApprovalPending {
		t.Fatal("gate did not advance")
	}
	if _, err := f.store.Resolve(ctx, f.input(first, "APPROVE")); err == nil {
		t.Fatal("historical Review approved")
	}
	revise := f.input(second, "REVISE")
	revise.RequestID = "revise-2"
	revised, err := f.store.Resolve(ctx, revise)
	if err != nil {
		t.Fatal(err)
	}
	if ownedTask(t, f).Phase != task.PhaseReviewing {
		t.Fatal("revise phase")
	}
	thirdValue, err := f.reviews.Get(ctx, revised.SuccessorReviewID)
	if err != nil || thirdValue.Ordinal != 3 || thirdValue.RunID != revised.SuccessorRunID {
		t.Fatal("ordinal")
	}
	third := review.StartAdmission{Review: thirdValue, Run: run.Run{ID: thirdValue.RunID}}
	f.complete(t, third, approvedResult(t))
	if err := f.store.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	out, err := f.store.Resolve(ctx, f.input(third, "APPROVE"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Task.ID != initial.ID || out.Task.Phase != task.PhaseImplementationReady || out.Task.Title != initial.Title || out.Task.ProblemStatement != initial.ProblemStatement || out.Task.CurrentDefinitionRevisionID == "" {
		t.Fatalf("approval changed intent: %+v", out.Task)
	}
	// Approval freezes the one approved execution candidate, but it does not
	// reserve a Run or claim the FeatureArea.  The old fixture predated that
	// durable candidate and incorrectly treated it as an execution start.
	if ownedCount(t, f, "tasks") != 1 || ownedCount(t, f, "execution_proposals") != 1 || ownedCount(t, f, "runs") != 3 || ownedCount(t, f, "feature_area_agent_claims") != 0 || ownedCount(t, f, "task_definition_revisions") != 2 {
		t.Fatalf("approval materialization=%d candidates=%d runs=%d claims=%d revisions=%d", ownedCount(t, f, "tasks"), ownedCount(t, f, "execution_proposals"), ownedCount(t, f, "runs"), ownedCount(t, f, "feature_area_agent_claims"), ownedCount(t, f, "task_definition_revisions"))
	}
	replay, err := f.store.Resolve(ctx, f.input(third, "APPROVE"))
	if err != nil || !replay.Replay || replay.Task.ID != initial.ID {
		t.Fatalf("replay %+v %v", replay, err)
	}
	_, err = f.store.Resolve(ctx, revise)
	if err != nil {
		t.Fatalf("exact receipt replay lost: %v", err)
	}
	stale := f.input(second, "APPROVE")
	stale.RequestID = "old-approval"
	if _, err = f.store.Resolve(ctx, stale); err == nil {
		t.Fatal("superseded Review approved")
	}
}

func TestTaskOwnedConcurrentReviewAndExecutionAuthorization(t *testing.T) {
	f, _ := newTaskOwnedFixture(t)
	ctx := context.Background()
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository}
	plan := run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope}
	var mu sync.Mutex
	accepted := []review.StartAdmission{}
	var wg sync.WaitGroup
	for _, id := range []string{"one", "two"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			v, e := f.reviews.AdmitStart(ctx, f.candidate, id, strings.Repeat("a", 64), f.candidate.Instruction, scope, plan)
			if e == nil {
				mu.Lock()
				accepted = append(accepted, v)
				mu.Unlock()
			}
		}(id)
	}
	wg.Wait()
	if len(accepted) != 1 || ownedCount(t, f, "reviews") != 1 {
		t.Fatalf("concurrent reviews %d", len(accepted))
	}
	f.complete(t, accepted[0], approvedResult(t))
	if _, e := f.store.Resolve(ctx, f.input(accepted[0], "APPROVE")); e != nil {
		t.Fatal(e)
	}
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, _, e := f.store.ReserveTaskExecution(ctx, f.session, f.candidate.TaskID)
			ids <- v.ID
			errs <- e
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	current := ownedTask(t, f).CurrentExecutionID
	for id := range ids {
		if id != current || id == "" {
			t.Fatal("duplicate current Execution")
		}
	}
	if ownedCount(t, f, "execution_proposals") != 1 {
		t.Fatal("duplicate Execution history")
	}
	if ownedCount(t, f, "execution_proposal_admissions") != 1 || ownedCount(t, f, "runtime_preparation_attempts") != 0 || ownedTask(t, f).Phase != task.PhaseImplementationReady || ownedCount(t, f, "runs") != 1 || ownedCount(t, f, "feature_area_agent_claims") != 0 {
		t.Fatal("concurrent admission crossed the Run/claim boundary")
	}
	// All preparation retries reuse the committed admission request and payload.
	// The prerequisite Review is the other Run counted below.
	preparedIDs := make(chan string, 8)
	preparationErrors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, prepared, err := NewExecutionProposalStore(f.db).PrepareAdmittedExecution(ctx, current, testNow)
			preparedIDs <- prepared.ID
			preparationErrors <- err
		}()
	}
	wg.Wait()
	close(preparedIDs)
	close(preparationErrors)
	for err := range preparationErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for id := range preparedIDs {
		if id != "run_impl_"+current {
			t.Fatalf("noncanonical preparation replay Run %q", id)
		}
	}
	if ownedCount(t, f, "runtime_preparation_attempts") != 1 || ownedCount(t, f, "runtime_preparation_receipts") != 1 {
		t.Fatal("concurrent preparation duplicated durable attempt or receipt")
	}
	if ownedTask(t, f).Phase != task.PhaseImplementing || ownedCount(t, f, "runs") != 2 || ownedCount(t, f, "feature_area_agent_claims") != 1 {
		t.Fatal("start did not atomically reserve one implementation and claim")
	}
}

func TestTaskOwnedExecutionCyclesAndColdResume(t *testing.T) {
	f, original := newTaskOwnedFixture(t)
	ctx := context.Background()
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	approved, err := f.store.Resolve(ctx, f.input(a, "APPROVE"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.store.AuthorizeTaskExecution(ctx, f.session, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	first = terminalExecutionWithReport(t, f, first)
	if v := ownedTask(t, f); v.Status != task.StatusActive || v.Phase != task.PhaseAcceptancePending {
		t.Fatalf("transport terminalized or misplaced Task: %s %s", v.Status, v.Phase)
	}
	work := task.CloneWorkSpec(first.WorkSpec)
	work.CodexInstruction += "; address acceptance findings"
	verification := task.CloneVerificationSpec(first.VerificationSpec)
	changes, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(first.ID), ExecutionID: first.ID, Option: wp.AcceptanceRequestChanges, RequestID: "changes", NextWorkSpec: &work, NextVerificationSpec: &verification})
	if err != nil {
		t.Fatal(err)
	}
	if changes.Task.ID != original.ID || changes.Task.Phase != task.PhaseImplementationReady || changes.Task.CurrentDefinitionRevisionID == approved.Task.CurrentDefinitionRevisionID || changes.Task.CurrentExecutionID != changes.SuccessorExecutionID {
		t.Fatalf("request changes: %+v", changes)
	}
	second, err := NewExecutionProposalStore(f.db).Get(ctx, changes.SuccessorExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	second = terminalExecutionWithReport(t, f, second)
	again, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(second.ID), ExecutionID: second.ID, Option: wp.AcceptanceRequestReverification, RequestID: "reverify"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Task.Phase != task.PhaseVerificationReady || again.Task.CurrentDefinitionRevisionID != changes.Task.CurrentDefinitionRevisionID || again.SuccessorExecutionID == second.ID {
		t.Fatalf("reverification: %+v", again)
	}
	_, only, err := NewExecutionProposalStore(f.db).ConsumeAndCreateVerificationRun(ctx, again.SuccessorExecutionID, "verification-only", testNow.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if only.Purpose != run.PurposeVerification || only.TaskID != original.ID || ownedTask(t, f).Phase != task.PhaseVerifying {
		t.Fatal("verification-only ownership")
	}
	// A verification mutation never terminalizes the semantic Task; it enters
	// the frozen RECOVERY_REQUIRED Phase while Status stays ACTIVE.
	_, err = NewRunStore(f.db).Update(ctx, only.ID, func(r *run.Run) error {
		r.Status = run.StatusInfrastructureFailed
		r.Failure = &run.Failure{Kind: run.FailureInfrastructure, Code: "VERIFICATION_MUTATED_WORKTREE", Message: "immutable worktree guard"}
		r.UpdatedAt = testNow.Add(4 * time.Hour)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if v := ownedTask(t, f); v.Status != task.StatusActive || v.Phase != task.PhaseRecoveryRequired {
		t.Fatalf("infra changed Task: %+v", v)
	}
	var impls int
	if err = f.db.sql.QueryRow(`SELECT count(*) FROM runs WHERE execution_proposal_id=? AND purpose='IMPLEMENTATION'`, again.SuccessorExecutionID).Scan(&impls); err != nil || impls != 0 {
		t.Fatal("synthetic implementation")
	}
	root := f.db.stateRoot
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	f.store = NewWorkflowProgressionStore(f.db)
	if v := ownedTask(t, f); v.Status != task.StatusActive || v.Phase != task.PhaseRecoveryRequired || v.CurrentExecutionID != again.SuccessorExecutionID || v.CurrentReviewID != a.Review.ID {
		t.Fatalf("cold phase lost: %+v", v)
	}
}

func approveFixtureTask(t *testing.T, db *DB, current task.Task, session aisession.Session) task.Task {
	t.Helper()
	ctx := context.Background()
	if current.WorkSpec.ImplementationMode == "" {
		current.WorkSpec.ImplementationMode = task.ImplementationCodingAgent
	}
	if current.VerificationSpec.Mode == task.VerificationModeNone {
		current.VerificationSpec = task.VerificationSpec{Mode: task.VerificationModeRun, Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "test -d .", Required: true}}, AcceptanceCriteria: []string{"Fixture result matches intent"}, CodexInstruction: "Verify read-only", Scope: "Repository"}
	}
	persisted, err := NewTaskStore(db).Get(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ReviewApprovalID != "" {
		return persisted
	}
	config, err := NewCodexConfigStore(db).Get(ctx, current.CodexConfigID)
	if err != nil {
		t.Fatal(err)
	}
	model, effort := current.Model, current.ReasoningEffort
	if model == "" {
		model = "gpt-5.6-terra"
	}
	if effort == "" {
		effort = "high"
	}
	v := review.Review{ReviewDepth: task.ReviewDepthFast, TaskID: current.ID, ProjectID: current.ProjectID, FeatureAreaID: current.FeatureAreaID, CreatedByAISessionID: session.ID, AuthorKind: string(review.AuthorAISession), AuthorID: session.ID, RepositoryID: current.RepositoryID, CodexConfigID: current.CodexConfigID, Model: model, ReasoningEffort: effort, RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: model, ReasoningEffort: effort}, BackendSelection: func() *codexconfig.BackendSelection { x := config.Selection("fixture"); return &x }(), BaseCommit: current.BaseCommit, RepositoryProvenance: &review.RepositoryProvenance{RepositoryID: current.RepositoryID, Commit: current.BaseCommit, RepositoryHEAD: current.BaseCommit, BranchKind: review.ProvenanceBranch, BranchRef: "refs/heads/dev", InspectionMode: review.InspectionFeatureAreaWorkspace, CapturedAt: testNow}, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: current.Goal, Instruction: "Read-only fixture review"}
	if v.EngineeringGoal == "" {
		v.EngineeringGoal = current.ProblemStatement
	}
	service, _ := review.NewService(NewReviewStore(db), nil, func() time.Time { return testNow })
	if _, prepareErr := service.Prepare(v); prepareErr != nil {
		t.Fatalf("prepare fixture Review: %+v: %v", v, prepareErr)
	}
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository}
	a, err := service.AdmitStart(ctx, v, "fixture-review-"+workflowHash(current.ID)[:24], strings.Repeat("a", 64), v.Instruction, scope, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(review.ImplementationResult{ReviewDisposition: review.DispositionApprovalReady, Version: 1, Summary: "Approved bounded fixture change", WorkSpec: current.WorkSpec, VerificationSpec: current.VerificationSpec})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRunStore(db).Update(ctx, a.Run.ID, func(r *run.Run) error {
		r.Status = run.StatusSucceeded
		r.Result = &run.Result{AgentClaim: evidence.AgentClaim{Message: string(raw)}}
		r.UpdatedAt = testNow
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	selection := config.Selection("fixture-approval")
	runtimeConfig := codexruntime.NextRunRuntimeConfig{Model: model, ReasoningEffort: effort}
	out, err := NewWorkflowProgressionStore(db).Resolve(ctx, wp.ResolveInput{AISessionID: session.ID, TaskID: current.ID, DecisionID: wp.DecisionID(a.Review.ID), Option: "APPROVE", RequestID: "fixture-approve-" + workflowHash(current.ID)[:24], BackendSelection: &selection, NextRunRuntimeConfig: &runtimeConfig})
	if err != nil {
		g, readErr := NewWorkflowProgressionStore(db).Read(ctx, current.ProjectID, current.FeatureAreaID)
		t.Fatalf("fixture approval: %v; state=%s; materialization=%+v; readErr=%v", err, g.CurrentState, g.MaterializationFailure, readErr)
	}
	return *out.Task
}

func TestTaskOwnedStartAtomicRollbackAndResume(t *testing.T) {
	// Admission (transaction A) and Run creation (transaction B) are now
	// separate.  Faults in transaction A block admission; faults in
	// transaction B preserve the admission but block the Run.
	f, initial := newTaskOwnedFixture(t)
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	ctx := context.Background()
	if _, err := f.store.Resolve(ctx, f.input(a, "APPROVE")); err != nil {
		t.Fatal(err)
	}
	// Fault in admission table: ReserveTaskExecution must fail.
	if _, err := f.db.sql.Exec(`CREATE TRIGGER fail_start BEFORE INSERT ON execution_start_effects BEGIN SELECT RAISE(ABORT,'injected start failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.ReserveTaskExecution(ctx, f.session, initial.ID); err == nil {
		t.Fatal("fault accepted")
	}
	current := ownedTask(t, f)
	if current.Phase != task.PhaseImplementationReady || current.CurrentExecutionID == "" || ownedCount(t, f, "execution_proposals") != 1 || ownedCount(t, f, "runs") != 1 || ownedCount(t, f, "feature_area_agent_claims") != 0 {
		t.Fatal("partial start transaction")
	}
	if _, err := f.db.sql.Exec(`DROP TRIGGER fail_start`); err != nil {
		t.Fatal(err)
	}
	candidate, reserved, err := f.store.ReserveTaskExecution(ctx, f.session, initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Status != executionproposal.StatusAdmitted || reserved.ID != "" {
		t.Fatal("admission did not separate from Run creation")
	}
	// Fault in Run creation table: admission persists, Run creation fails.
	for _, table := range []string{"runs", "feature_area_agent_claims"} {
		t.Run(table, func(t *testing.T) {
			f2, initial2 := newTaskOwnedFixture(t)
			a2 := f2.start(t)
			f2.complete(t, a2, approvedResult(t))
			if _, err := f2.store.Resolve(ctx, f2.input(a2, "APPROVE")); err != nil {
				t.Fatal(err)
			}
			value, adm, err := f2.store.ReserveTaskExecution(ctx, f2.session, initial2.ID)
			if err != nil {
				t.Fatal(err)
			}
			if value.Status != executionproposal.StatusAdmitted || adm.ID != "" {
				t.Fatal("admission did not separate")
			}
			if _, err := f2.db.sql.Exec(`CREATE TRIGGER fail_start BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(ABORT,'injected start failure'); END`); err != nil {
				t.Fatal(err)
			}
			if _, _, err := continueAdmittedExecution(ctx, f2.db, value.ID, testNow); err == nil {
				t.Fatal("fault accepted")
			}
			if _, err := f2.db.sql.Exec(`DROP TRIGGER fail_start`); err != nil {
				t.Fatal(err)
			}
			_, r, err := continueAdmittedExecution(ctx, f2.db, value.ID, testNow)
			if err != nil || r.ID == "" {
				t.Fatalf("retry failed: %+v %v", r, err)
			}
		})
	}
	// Replay after clearing the admission fault.
	root := f.db.stateRoot
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewWorkflowProgressionStore(db)
	replay, reserved2, err := store.ReserveTaskExecution(ctx, f.session, initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != candidate.ID {
		t.Fatalf("replay mismatch: %q vs %q", replay.ID, candidate.ID)
	}
	if replay.Status == executionproposal.StatusAdmitted && reserved2.ID == "" {
		_, reserved2, err = NewExecutionProposalStore(db).PrepareAdmittedExecution(ctx, replay.ID, testNow)
		if err != nil {
			t.Fatal(err)
		}
	}
}
