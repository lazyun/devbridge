package controlapi_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	rs "github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func unlockedFormalContextFixture(t *testing.T) (*upperLifecycleFixture, fe.ReadIdentity, rs.PlatformReference) {
	t.Helper()
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	_, approve := prepareDefinitionFormalApproval(t, f, "context-prior")
	if approved, err := f.facade.CommitConfirmation(f.ctx, approve); err != nil || approved.State != "COMPLETED" {
		t.Fatalf("prior definition approval: %+v %v", approved, err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "context-adoption-prepare", Purpose: c.Execution, Action: "START_APPROVED", Execution: &c.ExecutionBody{Action: "START_APPROVED", TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExpectedTarget: &expected}, Variants: []c.Variant{{ID: "start", Summary: "Select implementation runtime", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := differentDefinitionRuntime(t, opts, codexruntime.NextRunRuntimeConfig{})
	if len(opts.BackendCandidates) == 0 {
		t.Fatal("missing qualified backend candidate")
	}
	backend := opts.BackendCandidates[0]
	ref := formalExecutionPlatform(t, f, backend)
	commit := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "context-adoption-commit", ContentHash: prepared.ContentHash, VariantID: "start", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}}
	commit.SubmittedHash = c.ValueHash(commit)
	commit.BackendSelection = &backend
	step := c.Step{Kind: "START_IMPLEMENTATION", RequestID: "confirmation_" + c.ValueHash([]string{prepared.IntentID, "START_IMPLEMENTATION"})[:32], State: "UNKNOWN"}
	if _, _, err = sqlite.NewConfirmationStore(f.plane.DB).BeginCommit(f.ctx, commit, []c.Step{step}); err != nil {
		t.Fatal(err)
	}
	id, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).FormalExecutionIdentity(f.ctx, f.session, prepared.IntentID, commit.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	return f, id, ref
}

func changeFormalDevelopmentContext(t *testing.T, f *upperLifecycleFixture) {
	t.Helper()
	repository, err := f.plane.Repositories.Get(f.ctx, f.repos["app"])
	if err != nil {
		t.Fatal(err)
	}
	gitOutput(t, repository.Path, "branch", "formal-context-new-development", f.heads["app"])
	if _, err = f.facade.SelectDevelopmentBranchForSession(f.ctx, developmentcycle.SelectInput{AISessionID: f.session, ProjectID: f.project, RepositoryID: repository.ID, DevelopmentRef: "refs/heads/formal-context-new-development", RequestID: "formal-context-reselection"}); err != nil {
		t.Fatal(err)
	}
}

func assertFormalContextFailure(t *testing.T, err error, code string) {
	t.Helper()
	var failure *c.Failure
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("fresh formal writer did not reject changed context with %s: %v", code, err)
	}
}

func TestFormalContextFenceUnlockedAdoptionRejectsTopologyTOCTOU(t *testing.T) {
	f, id, ref := unlockedFormalContextFixture(t)
	// BeginCommit already recorded consumption. Changing production topology
	// here bypasses every facade preflight and exercises the business writer.
	f.appendTopologyMember(t, "formal-context-topology-member")
	tables := definitionFormalEveryTable(t, f)
	before := definitionFormalSnapshot(t, f, tables)
	_, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitAdoption(f.ctx, id, ref)
	assertFormalContextFailure(t, err, "PROJECT_TOPOLOGY_CHANGED")
	assertDefinitionFormalSnapshot(t, f, tables, before, "stale unlocked admission must write no business, source or completion")
}

func TestFormalContextFenceLockedAdoptionRejectsDevelopmentTOCTOU(t *testing.T) {
	f, _, id, _ := lockedFormalAdoptionFixture(t)
	changeFormalDevelopmentContext(t, f)
	tables := definitionFormalEveryTable(t, f)
	before := definitionFormalSnapshot(t, f, tables)
	_, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitAdoption(f.ctx, id, rs.PlatformReference{})
	assertFormalContextFailure(t, err, "DEVELOPMENT_BRANCH_CONFIGURATION_CHANGED")
	assertDefinitionFormalSnapshot(t, f, tables, before, "stale locked confirmation must preserve its prior admission without new output")
}

func TestFormalContextFenceDefinitionApprovalRejectsTopologyTOCTOU(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	prepared, commit := prepareDefinitionFormalApproval(t, f, "context-approval")
	commit.SubmittedHash = c.ValueHash(commit)
	step := c.Step{Kind: "APPROVE", RequestID: "confirmation_" + c.ValueHash([]string{prepared.IntentID, "APPROVE"})[:32], State: "UNKNOWN"}
	if _, _, err := sqlite.NewConfirmationStore(f.plane.DB).BeginCommit(f.ctx, commit, []c.Step{step}); err != nil {
		t.Fatal(err)
	}
	f.appendTopologyMember(t, "formal-approval-context-member")
	tables := definitionFormalEveryTable(t, f)
	before := definitionFormalSnapshot(t, f, tables)
	_, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).Resolve(taskaction.WithIntent(f.ctx, prepared.IntentID), wp.ResolveInput{AISessionID: f.session, TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), Option: "APPROVE", RequestID: step.RequestID, ConfirmationIntentID: prepared.IntentID, StartEffect: wp.StartEffectNoStart})
	assertFormalContextFailure(t, err, "PROJECT_TOPOLOGY_CHANGED")
	assertDefinitionFormalSnapshot(t, f, tables, before, "stale definition approval must not consume its Decision or complete confirmation")
}

func TestFormalContextFenceFinalReceiptReplayPrecedesChangedContext(t *testing.T) {
	f, _, id, _ := lockedFormalAdoptionFixture(t)
	store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	receipt, err := store.CommitAdoption(f.ctx, id, rs.PlatformReference{})
	if err != nil {
		t.Fatal(err)
	}
	f.appendTopologyMember(t, "formal-completed-context-member")
	changeFormalDevelopmentContext(t, f)
	tables := definitionFormalEveryTable(t, f)
	before := definitionFormalSnapshot(t, f, tables)
	replayed, err := store.CommitAdoption(f.ctx, id, rs.PlatformReference{})
	if err != nil || !reflect.DeepEqual(receipt, replayed) {
		t.Fatalf("final historical receipt consulted mutable context: %v", err)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "complete receipt replay must remain a zero-write historical read")
}
