package controlapi_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestTaskReviewModeNoneRequiresSeparateImplementationConfirmation(t *testing.T) {
	for _, lost := range []bool{false, true} {
		name := "normal"
		if lost {
			name = "lost-confirmation-response"
		}
		t.Run(name, func(t *testing.T) { testSkippedReviewImplementation(t, lost, false) })
	}
	t.Run("repository-chosen-at-implementation", func(t *testing.T) { testSkippedReviewImplementation(t, false, true) })
}
func testSkippedReviewImplementation(t *testing.T, lost, repositoryLater bool) {
	f := newCompositionFixture(t)
	input := f.taskPrepare("prepare-mode", compositionUpperVariant("ai", "AI Review", "Read and submit the report."))
	if repositoryLater {
		input.Action = "CREATE_TASK"
		input.Task.RepositoryID = ""
		input.Task.StartReview = false
		input.Task.ReviewDepth = ""
		input.Task.ReviewPlan = run.ReviewPlan{}
		input.Variants = []c.Variant{{ID: "create", Label: "Create", Summary: "Create planning intent"}}
	}
	input.Task.Constraints = []string{"Use only the selected repository"}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	request := taskReviewCommit(prepared, "create-without-review", taskReviewBool(false))
	request.Selection.ReviewMode = "NONE"
	created, err := f.facade.CommitConfirmation(f.ctx, request)
	if err != nil {
		t.Fatalf("creation: %v %+v", err, created)
	}
	owner, err := f.plane.Tasks.Get(f.ctx, compositionStep(t, created, "CREATE_TASK").TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Phase != task.PhaseImplementationReady || owner.CurrentReviewID != "" || owner.CurrentExecutionID != "" || owner.RequiredReviewDepth != nil || owner.WorkSpec.ImplementationMode != "" || owner.CurrentDefinitionRevisionID == "" {
		t.Fatalf("creation did not stop at implementation confirmation: %+v", owner)
	}
	if a, b, c := f.counts(t); a != 1 || b != 0 || c != 0 {
		t.Fatalf("unexpected objects: %d %d %d", a, b, c)
	}
	guide, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).ReadTask(f.ctx, f.projectID, owner.FeatureAreaID, owner.ID)
	if err != nil || guide.NextAction.Code != "CONFIRM_AND_START_IMPLEMENTATION" {
		t.Fatalf("skipped task guidance: %+v %v", guide, err)
	}
	changed := request
	changed.Selection.ReviewMode = "UPPER_LLM"
	if _, err := f.facade.CommitConfirmation(f.ctx, changed); err == nil {
		t.Fatal("consumed skip selection was replaced")
	}
	eligible, err := f.plane.Tasks.CanConfirmUnreviewedImplementation(f.ctx, f.sessionID, owner.ID)
	if err != nil || !eligible {
		t.Fatalf("missing review-skip receipt: %v %v", eligible, err)
	}
	// A forged call without the user's exact consumed confirmation cannot approve.
	_, err = f.plane.Tasks.ConfirmUnreviewedImplementation(f.ctx, task.UnreviewedImplementationInput{AISessionID: f.sessionID, TaskID: owner.ID, IntentID: "forged", RequestID: "forged", ExpectedRevisionID: owner.CurrentDefinitionRevisionID})
	if err == nil {
		t.Fatal("forged implementation confirmation was accepted")
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Implement the requested change", Constraints: []string{"Use only the selected repository"}, CodexInstruction: "Implement the requested change.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "selected repository", CodexInstruction: "Run the test suite.", AcceptanceCriteria: []string{"Tests pass"}, Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}}}
	plan := c.PrepareInput{AISessionID: f.sessionID, RequestID: "prepare-implementation", Purpose: c.Execution, Action: "CONFIRM_IMPLEMENTATION", Execution: &c.ExecutionBody{Action: "CONFIRM_IMPLEMENTATION", TaskID: owner.ID, FeatureAreaID: owner.FeatureAreaID, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID}, Variants: []c.Variant{{ID: "implementation", Label: "Implementation", Summary: "Implement and verify", ImplementationMode: task.ImplementationCodingAgent, WorkSpec: &work, VerificationSpec: &verification}}}
	if repositoryLater {
		incomplete := plan
		incomplete.RequestID = "plan-without-repository"
		if _, err := f.facade.PrepareConfirmation(f.ctx, incomplete); err == nil {
			t.Fatal("implementation plan omitted its Repository")
		}
		repository, err := f.plane.Repositories.Get(f.ctx, f.repositoryID)
		if err != nil {
			t.Fatal(err)
		}
		plan.Execution.RepositoryID = f.repositoryID
		plan.Execution.BaseCommit = compositionGit(t, repository.Path, "rev-parse", "HEAD")
	}
	pending, err := f.facade.PrepareConfirmation(f.ctx, plan)
	if err != nil {
		t.Fatalf("prepare implementation: %v", err)
	}
	opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.sessionID, IntentID: pending.IntentID, Version: pending.Version, Purpose: pending.Purpose, ContentHash: pending.ContentHash}})
	if err != nil || !opts.Eligibility.Available {
		t.Fatalf("definition-only choices: %+v %v", opts, err)
	}
	approval := c.CommitInput{AISessionID: f.sessionID, IntentID: pending.IntentID, Version: pending.Version, Purpose: pending.Purpose, Action: pending.Action, RequestID: "confirm-plan", ContentHash: pending.ContentHash, VariantID: "implementation"}
	if lost {
		consumed := approval
		consumed.SubmittedHash = c.ValueHash(consumed)
		step := c.Step{Kind: "CONFIRM_IMPLEMENTATION", RequestID: "implementation-confirm-step", State: "PENDING"}
		if _, _, err := f.plane.Confirmations.BeginCommit(f.ctx, consumed, []c.Step{step}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.plane.Tasks.ConfirmUnreviewedImplementation(f.ctx, task.UnreviewedImplementationInput{AISessionID: f.sessionID, TaskID: owner.ID, IntentID: pending.IntentID, RequestID: approval.RequestID, ExpectedRevisionID: owner.CurrentDefinitionRevisionID, RepositoryID: pending.Content.Execution.RepositoryID, BaseCommit: pending.Content.Execution.BaseCommit, WorkSpec: work, VerificationSpec: verification}); err != nil {
			t.Fatal(err)
		}
		// The business transaction committed, but its caller could not determine
		// whether it completed. Status recovery must use that exact durable receipt.
		step.State = "UNKNOWN"
		step.Effect = "UNKNOWN"
		if _, err := f.plane.Confirmations.RecordStep(f.ctx, f.sessionID, pending.IntentID, pending.Version, step, "UNKNOWN"); err != nil {
			t.Fatal(err)
		}
	}
	accepted, err := f.facade.CommitConfirmation(f.ctx, approval)
	if err != nil || accepted.State != "COMPLETED" {
		t.Fatalf("implementation confirmation: %+v %v", accepted, err)
	}
	owner, err = f.plane.Tasks.Get(f.ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if owner.RepositoryID != f.repositoryID || !reflect.DeepEqual(owner.WorkSpec, work) || owner.ReviewApprovalID != "" || owner.CurrentReviewID != "" || owner.CurrentExecutionID != "" {
		t.Fatalf("unexpected approved definition: %+v", owner)
	}
	if a, b, c := f.counts(t); a != 1 || b != 0 || c != 0 {
		t.Fatalf("confirmation started work: %d %d %d", a, b, c)
	}
	replay, err := f.facade.CommitConfirmation(f.ctx, approval)
	if err != nil || !reflect.DeepEqual(replay.Steps, accepted.Steps) {
		t.Fatalf("approval replay: %+v %v", replay, err)
	}
	if _, err := f.facade.CommitConfirmation(f.ctx, request); err != nil {
		t.Fatalf("creation replay: %v", err)
	}
	// Confirming content does not grant a raw store caller permission to start.
	store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	expected := wp.TaskExecutionExpectation{FeatureAreaID: owner.FeatureAreaID, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, Phase: task.PhaseImplementationReady}
	if _, _, err := store.ReserveTaskExecutionExpectedCommand(f.ctx, f.sessionID, owner.ID, "START_EXECUTION", "raw-start", &expected); err == nil {
		t.Fatal("definition confirmation authorized an unconfirmed runtime start")
	}
	if _, err := f.plane.DB.SQL().Exec(`UPDATE tasks SET goal='tampered' WHERE id=?`, owner.ID); err == nil {
		t.Fatal("confirmed implementation definition was mutable")
	}
	if _, err := f.plane.FeatureAreas.Activate(f.ctx, f.sessionID, owner.FeatureAreaID, "activate-skip"); err != nil {
		t.Fatal(err)
	}
	// Isolated fixture: persist the separately selected start through the real
	// confirmation store, then reserve only. No backend process is dispatched.
	startInput := c.PrepareInput{AISessionID: f.sessionID, RequestID: "prepare-explicit-start", Purpose: c.Execution, Action: "START_APPROVED", Execution: &c.ExecutionBody{Action: "START_APPROVED", TaskID: owner.ID, FeatureAreaID: owner.FeatureAreaID, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, ExpectedTarget: &expected}, Variants: []c.Variant{{ID: "start", Label: "Start", Summary: "Start confirmed plan", ImplementationMode: task.ImplementationCodingAgent, WorkSpec: &owner.WorkSpec, VerificationSpec: &owner.VerificationSpec}}}
	start, err := f.facade.PrepareConfirmation(f.ctx, startInput)
	if err != nil {
		t.Fatal(err)
	}
	runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-luna", ReasoningEffort: "medium"}
	backend := codexconfig.BackendSelection{ConfigID: "fixture-backend", ConfigFingerprint: strings.Repeat("a", 64), ConfigName: "Fixture backend", Source: "SESSION_INVENTORY"}
	commit := c.CommitInput{AISessionID: f.sessionID, IntentID: start.IntentID, Version: start.Version, Purpose: start.Purpose, Action: start.Action, RequestID: "start-runtime", ContentHash: start.ContentHash, VariantID: "start", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}, BackendSelection: &backend}
	commit.SubmittedHash = c.ValueHash(commit)
	step := c.Step{Kind: "START_IMPLEMENTATION", RequestID: "explicit-start-step", State: "PENDING"}
	if _, _, err = f.plane.Confirmations.BeginCommit(f.ctx, commit, []c.Step{step}); err != nil {
		t.Fatal(err)
	}
	admitted, reserved, err := store.ReserveTaskExecutionExpectedCommand(taskaction.WithIntent(f.ctx, start.IntentID), f.sessionID, owner.ID, "START_EXECUTION", step.RequestID, &expected)
	if err != nil || admitted.ID == "" || reserved.ID != "" {
		t.Fatalf("explicit start admission: %+v %+v %v", admitted, reserved, err)
	}
	if admitted.RuntimeConfig != runtime || admitted.BackendSelection == nil || admitted.BackendSelection.ConfigID != backend.ConfigID {
		t.Fatalf("lost explicit runtime: %+v", admitted)
	}

}

func TestTaskReviewModeMustMatchPreparedExecutor(t *testing.T) {
	f := newCompositionFixture(t)
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("prepare", compositionUpperVariant("ai", "AI Review", "Read and report.")))
	if err != nil {
		t.Fatal(err)
	}
	request := taskReviewCommit(prepared, "mismatched-mode", taskReviewBool(true))
	request.Selection.ReviewMode = string(review.ExecutorCodingAgent)
	if _, err = f.facade.CommitConfirmation(f.ctx, request); err == nil {
		t.Fatal("mismatched Review executor was accepted")
	}
	if a, b, c := f.counts(t); a != 0 || b != 0 || c != 0 {
		t.Fatalf("rejected selection created objects: %d %d %d", a, b, c)
	}
}

func TestTaskReviewModeAIWorksWhenCodexRuntimeIsUnavailable(t *testing.T) {
	f := newCompositionFixture(t)
	ai := compositionUpperVariant("ai", "AI file review", "Read authorized files and submit the report.")
	coding := c.Variant{ID: "codex", Label: "Codex review", Summary: "Review in Codex", ExecutorKind: string(review.ExecutorCodingAgent), ReviewInstruction: "Review the repository read-only and report findings."}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("both-review-methods", ai, coding))
	if err != nil {
		t.Fatal(err)
	}
	on := true
	opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.sessionID, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}, StartReview: &on})
	if err != nil {
		t.Fatal(err)
	}
	available := map[string]bool{}
	for _, variant := range opts.Variants {
		available[variant.ID] = variant.Available
	}
	if !available["ai"] || available["codex"] {
		t.Fatalf("executor eligibility was not independent: %+v", opts)
	}
	request := taskReviewCommit(prepared, "choose-ai", &on)
	request.Selection.ReviewMode = "UPPER_LLM"
	request.VariantID = "ai"
	accepted, err := f.facade.CommitConfirmation(f.ctx, request)
	if err != nil || accepted.State != "COMPLETED" {
		t.Fatalf("AI review used missing Codex runtime: %+v %v", accepted, err)
	}
	owner, err := f.plane.Tasks.Get(f.ctx, compositionStep(t, accepted, "CREATE_TASK").TaskID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.plane.Reviews.Get(f.ctx, owner.CurrentReviewID)
	if err != nil || current.ExecutorKind != review.ExecutorUpperLLM || current.RuntimeConfig.Model != "" {
		t.Fatalf("wrong review method: %+v %v", current, err)
	}
}
