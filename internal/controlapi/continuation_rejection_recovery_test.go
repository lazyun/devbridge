package controlapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestRequestChangesUnavailableAndRetiredNoEffectRecovery(t *testing.T) {
	f, id, platform := formalExecutionFixtureForUpper(t, newUpperLifecycleFixtureScoped(t, "data-instance-local"))
	f.plane.Retirement.Close()
	receipt, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitApproveAndStart(f.ctx, id, platform)
	if err != nil {
		t.Fatal(err)
	}
	parent := receipt.Archive.Commit
	parent.BackendSelection, parent.OriginalRequest, parent.SubmittedHash = nil, nil, ""
	if out, e := f.facade.CommitConfirmation(f.ctx, parent); e != nil || out.State != "COMPLETED" {
		t.Fatalf("complete original approval: %+v %v", out, e)
	}
	_, _, err = sqlite.NewExecutionCancellationStore(f.plane.DB).Begin(f.ctx, executioncancel.Input{AISessionID: f.session, ProjectID: f.project, ExecutionID: receipt.T2.ExecutionID, OwnerType: "EXECUTION", OwnerID: receipt.T2.ExecutionID, RequestID: "change-predecessor-cancel", Reason: "Cancel the prepared candidate"}, false)
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	read := assertUnsupportedChangeAndRetiredRecovery(t, f, current, receipt.T2.ExecutionID, definitionFormalBusinessTables)
	captureRetiredContinuationCard(t, f, read)

}

func TestOperatorFormalCandidateKeepsOriginalStartAction(t *testing.T) {
	f, id, platform := formalExecutionFixture(t)
	f.plane.Retirement.Close()
	receipt, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitApproveAndStart(f.ctx, id, platform)
	if err != nil {
		t.Fatal(err)
	}
	selections := sqlite.NewRuntimeSelectionStore(f.plane.DB)
	source, err := selections.ReadSource(f.ctx, receipt.T1.Identity.SourceOriginID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = selections.MaterializeBinding(f.ctx, runtimeselection.BindingInput{RequestID: "runtime_binding_" + source.SourceID, Owner: source.Owner, SourceID: source.SourceID, SourcePayloadHash: source.SourcePayloadHash, Entry: source.Entry, ExpectedLineageVersion: platform.LineageVersion})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := consoleapi.New(f.plane, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	before := definitionFormalSnapshot(t, f, formalExecutionTables())
	page, err := reader.GetProductTask(operatorStartContext(t, f.project, f.task, "formal-ready-action"), f.project, f.task)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, capability := range page.OperatorActions {
		if capability.Action == "START_EXECUTION" {
			found = capability.DomainAvailable && capability.ChannelAvailable && capability.Target != nil && capability.Target.ExecutionID == receipt.T2.ExecutionID && capability.Target.TaskDefinitionRevisionID == receipt.Archive.RevisionID
		}
	}
	if !found {
		t.Fatalf("formally bound candidate lost operator start: %+v", page.OperatorActions)
	}
	for _, capability := range page.Presentation.TaskView.Actions {
		if capability.Action == "START_EXECUTION" && len(capability.RequiredInputs) != 0 {
			t.Fatalf("bound candidate asks to reselect runtime: %+v", capability)
		}
	}
	assertDefinitionFormalSnapshot(t, f, formalExecutionTables(), before, "operator existing-candidate action read")
}

func assertUnsupportedChangeAndRetiredRecovery(t *testing.T, f *upperLifecycleFixture, current task.Task, oldExecution string, business []string) c.ReadInput {
	t.Helper()
	work, verification := task.CloneWorkSpec(current.WorkSpec), task.CloneVerificationSpec(current.VerificationSpec)
	work.Goal = "Revised explicit user goal"
	work.CodexInstruction = "Apply revised user scope to both same.txt files."
	in := c.PrepareInput{AISessionID: f.session, RequestID: "decision-successor-prepare", Purpose: c.Execution, Action: "REQUEST_CHANGES", Execution: &c.ExecutionBody{Action: "REQUEST_CHANGES", TaskID: f.task, FeatureAreaID: f.area, ReviewID: current.CurrentReviewID, ExecutionID: oldExecution, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(oldExecution)}, Variants: []c.Variant{{ID: "revised", Summary: "Explicit revised scope after stop", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &work, VerificationSpec: &verification}}}
	all := append(append([]string{}, business...), "confirmation_intents", "confirmation_commit_requests", "task_plans", "events")
	before := definitionFormalSnapshot(t, f, all)
	if _, err := f.facade.PrepareConfirmation(f.ctx, in); !isFormalReviewRequired(err) {
		t.Fatalf("unsupported change prepare must explicitly require Review: %v", err)
	}
	assertDefinitionFormalSnapshot(t, f, all, before, "unsupported prepare")
	presentation, err := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range presentation.TaskView.Actions {
		if action.Action == "REQUEST_CHANGES" && (action.Available || !strings.Contains(action.Reason, "REQUEST_CHANGES_REQUIRES_FORMAL_REVIEW")) {
			t.Fatalf("unsupported change advertised: %+v", action)
		}
	}

	// Recreate an actual old-version attempt through the same storage writers:
	// original prepare and consumed journal, then a canonical business transaction
	// that really rolls back at the binding guard. No success row is fabricated.
	store := sqlite.NewConfirmationStore(f.plane.DB)
	in.RequestID = "historical-change-prepare"
	in.SubmittedHash = c.ValueHash(in)
	prepared, err := store.Prepare(f.ctx, f.project, in)
	if err != nil {
		t.Fatal(err)
	}
	read := c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}
	opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: read})
	if err != nil || opts.Eligibility.Available || len(opts.Variants) != 1 || opts.Variants[0].Available || opts.Variants[0].Reason != "REQUEST_CHANGES_REQUIRES_FORMAL_REVIEW" {
		t.Fatalf("historical prepared options remain actionable: %+v %v", opts, err)
	}
	predecessor, err := f.facade.GetExecutionProposal(f.ctx, f.session, oldExecution)
	if err != nil || predecessor.BackendSelection == nil {
		t.Fatal(err)
	}
	runtime, backend := predecessor.RuntimeConfig, *predecessor.BackendSelection
	commit := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "historical-change-commit", ContentHash: prepared.ContentHash, VariantID: "revised", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}}
	commit.SubmittedHash = c.ValueHash(commit)
	originalRequest := commit
	commit.OriginalRequest = &originalRequest
	commit.BackendSelection = &backend
	step := c.Step{Kind: "RESOLVE_CONTINUATION", RequestID: "confirmation_" + c.ValueHash([]string{prepared.IntentID, "RESOLVE_CONTINUATION"})[:32], State: "UNKNOWN"}
	start := c.Step{Kind: "START_IMPLEMENTATION", RequestID: "confirmation_" + c.ValueHash([]string{prepared.IntentID, "START_IMPLEMENTATION"})[:32], State: "PENDING"}
	if _, fresh, err := store.BeginCommit(f.ctx, commit, []c.Step{step, start}); err != nil || !fresh {
		t.Fatalf("historical begin fresh=%v %v", fresh, err)
	}
	beforeBusiness := definitionFormalSnapshot(t, f, business)
	_, err = f.plane.WorkflowProgression.ResolveTaskAcceptance(taskaction.WithIntent(f.ctx, prepared.IntentID), wp.TaskAcceptanceInput{AISessionID: f.session, TaskID: current.ID, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: oldExecution, DecisionID: wp.TaskAcceptanceDecisionID(oldExecution), Option: wp.AcceptanceRequestChanges, RequestID: step.RequestID, ConfirmationIntentID: prepared.IntentID, StartEffect: wp.StartEffectStartAuthorized, NextWorkSpec: &work, NextVerificationSpec: &verification, NextRepositoryTargets: current.RepositoryTargets, NextPrimaryMemberID: current.PrimaryMemberID, NextRunRuntimeConfig: &runtime, BackendSelection: &backend})
	if !errors.Is(err, runtimeselection.ErrBindingMissing) {
		t.Fatalf("real historical business rejection: %v", err)
	}
	assertDefinitionFormalSnapshot(t, f, business, beforeBusiness, "rejected historical transaction")
	step.Effect = "UNKNOWN"
	step.Failure = c.NewFailure(err.Error(), "CORE", "BUSINESS_STEP", err.Error(), c.ReadOriginalReceipt, nil)
	step.Code, step.Message = step.Failure.Code, step.Failure.Message
	if _, err = store.RecordStep(f.ctx, f.session, prepared.IntentID, prepared.Version, step, "UNKNOWN"); err != nil {
		t.Fatal(err)
	}
	read.RequestID = commit.RequestID
	live, err := f.facade.ReadConfirmation(f.ctx, read)
	if err != nil || live.State != "UNKNOWN" || live.CanResume {
		t.Fatalf("same-worker negative inference: %+v %v", live, err)
	}
	_, protocolFixture := f.plane.Coordinator.QualifiedRuntime.(*protocolFixturePlatform)
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	if protocolFixture {
		installProtocolFixturePlatform(t, f)
	}
	before = definitionFormalSnapshot(t, f, all)
	for range 2 {
		retired, err := f.facade.ReadConfirmation(f.ctx, read)
		if err != nil || retired.State != "REJECTED" || retired.CanResume || retired.Steps[0].Effect != "NO_EFFECT_PROVEN" || retired.Submission == nil || retired.Submission.Outcome != c.Consumed || retired.Submission.BusinessOutcome != "REJECTED_NO_EFFECT" || retired.Submission.NextAction != c.ReadCurrentTarget {
			t.Fatalf("retired exact no-effect proof: %+v %v", retired, err)
		}
		assertDefinitionFormalSnapshot(t, f, all, before, "retired readback must not rewrite original facts")
	}
	return read
}

func assertPublicRetryKeepsFrozenDefinition(t *testing.T, f *upperLifecycleFixture, current task.Task, executionID string) {
	t.Helper()
	work, verification := task.CloneWorkSpec(current.WorkSpec), task.CloneVerificationSpec(current.VerificationSpec)
	in := c.PrepareInput{AISessionID: f.session, RequestID: "frozen-retry-prepare", Purpose: c.Execution, Action: "RETRY_IMPLEMENTATION", Execution: &c.ExecutionBody{Action: "RETRY_IMPLEMENTATION", TaskID: f.task, FeatureAreaID: f.area, ReviewID: current.CurrentReviewID, ExecutionID: executionID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(executionID)}, Variants: []c.Variant{{ID: "retry", Summary: "Retry exactly the approved definition", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &work, VerificationSpec: &verification}}}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, in)
	if err != nil {
		t.Fatalf("exact retry prepare: %v", err)
	}
	opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
	predecessor, readErr := f.facade.GetExecutionProposal(f.ctx, f.session, executionID)
	if err != nil || readErr != nil || !opts.Eligibility.Available || opts.RuntimeSelectionMode != "FROZEN_EXECUTION" || opts.LockedRuntimeConfig == nil || *opts.LockedRuntimeConfig != predecessor.RuntimeConfig || !reflect.DeepEqual(opts.LockedBackend, predecessor.BackendSelection) {
		t.Fatalf("retry options did not freeze original authority: %+v %v/%v", opts, err, readErr)
	}
	if _, err = f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(prepared)); err != nil {
		t.Fatal(err)
	}
	in.RequestID = "changed-instruction-retry-prepare"
	work.CodexInstruction += "\nDifferent authored instruction."
	in.Variants[0].ExecutionInstructions = &task.ExecutionInstructions{Implementation: work.CodexInstruction, Verification: &verification.CodexInstruction}
	tables := append(definitionFormalAllTables(), "task_plans")
	before := definitionFormalSnapshot(t, f, tables)
	_, err = f.facade.PrepareConfirmation(f.ctx, in)
	var failure *c.Failure
	if !errors.As(err, &failure) || failure.Code != "RETRY_REQUIRES_FROZEN_DEFINITION" {
		t.Fatalf("instruction-only retry became a new definition: %v", err)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "changed retry prepare")
}

func isFormalReviewRequired(err error) bool {
	var failure *c.Failure
	return errors.As(err, &failure) && failure.Code == "REQUEST_CHANGES_REQUIRES_FORMAL_REVIEW" && failure.NextAction == c.ReadCurrentTarget
}

// Capture the actual public wire after a real transaction rollback and cold restart.
// The Node test mounts these bytes, rather than inventing a negative receipt.
func captureRetiredContinuationCard(t *testing.T, f *upperLifecycleFixture, read c.ReadInput) {
	t.Helper()
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "retired-continuation-test", Ready: true}, f.plane.AIContext, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := mcpserver.New(mcpserver.Config{}, service)
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(adapter.Handler())
	defer host.Close()
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "retired-continuation-card", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: host.URL + mcpserver.MCPPath, HTTPClient: host.Client(), DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	stack := &cardLifecycleStack{t: t, fixture: f, ctx: ctx, session: session}
	current, err := f.facade.ReadConfirmation(f.ctx, read)
	if err != nil {
		t.Fatal(err)
	}
	show, out := stack.show(current, "")
	if out.Intent == nil || out.Intent.Submission == nil || out.Intent.Submission.BusinessOutcome != "REJECTED_NO_EFFECT" {
		t.Fatalf("retired Card lost no-effect result: %s", stack.lastRaw)
	}
	requireMountedCard(t, show, *out.Intent)
	original, returned := stack.read(current, current.Commit.RequestID)
	if returned.Intent == nil || returned.Intent.Submission == nil || returned.Intent.Submission.BusinessOutcome != "REJECTED_NO_EFFECT" {
		t.Fatalf("retired read lost no-effect result: %s", stack.lastRaw)
	}
	if dir := os.Getenv("DEVBRIDGE_RETIRED_CONTINUATION_FIXTURE_DIR"); dir != "" {
		raw, err := json.MarshalIndent(map[string]any{"_meta": map[string]any{"capturedBy": "internal/controlapi/continuation_rejection_recovery_test.go", "mainScriptSHA256": metaString(show, "devbridge/mainScriptSHA256"), "singleSourceOfTruth": "Actual MCP wire after original REQUEST_CHANGES rollback and retired worker readback. Never hand-edit an envelope."}, "entry": rawEnvelope(show), "read": rawEnvelope(original)}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "confirmation_retired_continuation_v5.json"), append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
