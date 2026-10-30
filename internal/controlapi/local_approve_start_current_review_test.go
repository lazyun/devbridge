package controlapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// This starts from the production browser ingress, which carries no command
// binding or borrowed AI Session. The Task draft intentionally has no mode yet;
// the pending Review is the canonical owner of the proposed Coding mode.
func TestLocalApproveAndStartCurrentReviewRequiresFormalSourceWithoutWrites(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	submitLifecycleApprovalReady(t, f, "operator-approval-boundary")
	ingress, err := localoperator.NewIngress("approve-start-production")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "http://127.0.0.1:4777/action", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4777}))
	ctx, err := ingress.AdmitHTTP(req)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := consoleapi.New(f.plane, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil || current.WorkSpec.ImplementationMode != "" {
		t.Fatalf("expected intent-only draft: mode=%q err=%v", current.WorkSpec.ImplementationMode, err)
	}
	page, err := reader.GetProductTask(ctx, f.project, f.task)
	if err != nil {
		t.Fatal(err)
	}
	var input consoleapi.TaskActionInput
	for _, cap := range page.OperatorActions {
		if cap.Action != "APPROVE_AND_START" {
			continue
		}
		if cap.DomainAvailable || cap.Target == nil || !strings.Contains(cap.Reason, "FORMAL_START_CONFIRMATION_REQUIRED") {
			t.Fatalf("unsupported local formal approval advertised: %+v", cap)
		}
		input = consoleapi.TaskActionInput{Action: cap.Action, Target: cap.Target, RequestID: "operator-no-formal-source"}
	}
	if input.Target == nil {
		t.Fatal("APPROVE_AND_START exact target missing")
	}
	for _, cap := range page.Presentation.TaskView.Actions {
		if cap.Action == "APPROVE_AND_START" && (cap.Available || !strings.Contains(cap.Reason, "FORMAL_START_CONFIRMATION_REQUIRED")) {
			t.Fatalf("action projection disagrees: %+v", cap)
		}
	}
	config, err := sqlite.NewCodexConfigStore(f.plane.DB).Get(ctx, codexconfig.DefaultConfigID)
	if err != nil {
		t.Fatal(err)
	}
	selected := config.Selection("OPERATOR_INVENTORY")
	tables := append(definitionFormalAllTables(), "local_approve_and_start_commands", "operator_execution_start_commands", "runtime_selection_sources", "runtime_selection_bindings")
	before := definitionFormalSnapshot(t, f, tables)
	for _, withSelection := range []bool{false, true} {
		if withSelection {
			input.RequestID += "-selected"
			input.NextRunRuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-luna", ReasoningEffort: "low"}
			input.BackendSelection = &codexconfig.BackendSelectionInput{ConfigID: selected.ConfigID, ConfigFingerprint: selected.ConfigFingerprint}
		}
		_, err := reader.ResolveTaskAction(ctx, f.project, f.task, input)
		var failure *controlapi.Error
		if !errors.As(err, &failure) || failure.Code != "FORMAL_START_CONFIRMATION_REQUIRED" {
			t.Fatalf("selected=%v should fail before approval, got %v", withSelection, err)
		}
		assertDefinitionFormalSnapshot(t, f, tables, before, "local Coding rejection")
		t.Logf("selected=%v: typed formal-source rejection, zero approval/admission/parent/runtime DB delta", withSelection)
	}
}

func TestLocalApproveAndStartHistoricalParentRemainsReadable(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	submitLifecycleApprovalReady(t, f, "historical-local-parent")
	ctx := operatorActionContext(t, f.project, f.task, "historical-local-approval", "APPROVE_AND_START")
	projection, err := f.facade.TaskPresentationForOperator(ctx, f.project, f.area, f.task)
	if err != nil {
		t.Fatal(err)
	}
	var input controlapi.LocalApproveAndStartInput
	for _, cap := range projection.Capabilities {
		if cap.Action == "APPROVE_AND_START" {
			input = controlapi.LocalApproveAndStartInput{TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), RequestID: "historical-local-rejection", PayloadSHA256: strings.Repeat("a", 64), Target: cap.Target}
		}
	}
	if input.Target == nil {
		t.Fatal("missing exact target")
	}
	request, err := localoperator.RequestID(ctx, f.project, f.task, "APPROVE_AND_START", input.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	actor, _ := localoperator.FromContext(ctx)
	target, _ := json.Marshal(input.Target)
	child := func(step string) string {
		raw, _ := json.Marshal([]string{"devbridge.local-approve-and-start.v1", request, step})
		sum := sha256.Sum256(raw)
		return "local_approve_start_" + hex.EncodeToString(sum[:])
	}
	store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	parent := wp.LocalApproveAndStart{ProjectID: f.project, TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: input.DecisionID, ActorID: actor.PrincipalID(), RawRequestID: input.RequestID, RequestID: request, PayloadSHA256: input.PayloadSHA256, TargetJSON: string(target), ApprovalRequestID: child("APPROVE"), StartRequestID: child("START_IMPLEMENTATION")}
	// Persist the original historical failure through its real journal owner.
	if _, _, err = store.BeginLocalApproveAndStart(ctx, parent); err != nil {
		t.Fatal(err)
	}
	original, err := store.RecordLocalApproveAndStartApprovalFailure(ctx, f.project, request, "APPROVAL_PRECONDITION_FAILED", true)
	if err != nil {
		t.Fatal(err)
	}
	tables := append(definitionFormalAllTables(), "local_approve_and_start_commands")
	before := definitionFormalSnapshot(t, f, tables)
	read, err := f.facade.ReadLocalApproveAndStartForOperator(ctx, f.project, input)
	if err != nil || !reflect.DeepEqual(read.Command, original) || read.HandoffInapplicableReason != "REJECTED_BEFORE_RUN_ADMISSION" {
		t.Fatalf("historical read=%+v %v", read, err)
	}
	replay, err := f.facade.ApproveAndStartForOperator(ctx, f.project, input)
	if err != nil || !reflect.DeepEqual(replay.Command, original) {
		t.Fatalf("historical replay=%+v %v", replay, err)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "historical read and replay")
}

func TestLocalOperatorStartsExistingFormalCandidate(t *testing.T) {
	f, id, _ := formalExecutionFixture(t)
	f.plane.Retirement.Close()
	installProtocolFixturePlatform(t, f)
	config, err := f.plane.CodexConfigs.Get(f.ctx, codexconfig.DefaultConfigID)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := f.plane.Coordinator.QualifiedRuntime.(*protocolFixturePlatform).ResolvePlatformReference(f.ctx, config.Selection("OPERATOR_INVENTORY"), "IMPLEMENTATION")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitApproveAndStart(f.ctx, id, platform)
	if err != nil {
		t.Fatal(err)
	}
	selections := sqlite.NewRuntimeSelectionStore(f.plane.DB)
	source, err := selections.ReadSource(f.ctx, receipt.T1.Identity.SourceOriginID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = selections.MaterializeBinding(f.ctx, runtimeselection.BindingInput{RequestID: "runtime_binding_" + source.SourceID, Owner: source.Owner, SourceID: source.SourceID, SourcePayloadHash: source.SourcePayloadHash, Entry: source.Entry, ExpectedLineageVersion: platform.LineageVersion}); err != nil {
		t.Fatal(err)
	}
	ctx := operatorStartContext(t, f.project, f.task, "formal-candidate-console")
	reader, err := consoleapi.New(f.plane, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.GetProductTask(ctx, f.project, f.task)
	if err != nil {
		t.Fatal(err)
	}
	var input consoleapi.TaskActionInput
	for _, cap := range page.OperatorActions {
		if cap.Action == "START_EXECUTION" && cap.DomainAvailable && cap.ChannelAvailable {
			input = consoleapi.TaskActionInput{Action: cap.Action, Target: cap.Target, RequestID: "start-existing-formal-candidate"}
		}
	}
	if input.Target == nil {
		t.Fatalf("formal candidate unavailable: %+v", page.OperatorActions)
	}
	before := definitionFormalSnapshot(t, f, formalExecutionTables())
	foreign := input
	foreignTarget := *input.Target
	foreignTarget.TaskID = "foreign-task"
	foreign.Target = &foreignTarget
	foreign.RequestID = "reject-foreign-browser-target"
	if _, err = reader.ResolveTaskAction(ctx, f.project, f.task, foreign); err == nil {
		t.Fatal("Console accepted a foreign Task target")
	}
	expected := wp.TaskExecutionExpectation{FeatureAreaID: input.Target.FeatureAreaID, TaskDefinitionRevisionID: input.Target.TaskDefinitionRevisionID, ExecutionID: "foreign-execution", Phase: task.PhaseImplementationReady, PrimaryMemberID: input.Target.PrimaryMemberID, ApprovedWorksetHash: input.Target.ApprovedWorksetHash}
	if _, err = f.facade.StartTaskExecutionForOperator(ctx, f.project, f.task, "START_EXECUTION", "reject-foreign-execution", expected); err == nil {
		t.Fatal("facade dispatched a foreign Execution target")
	}
	assertDefinitionFormalSnapshot(t, f, formalExecutionTables(), before, "foreign operator start targets")
	value, err := reader.ResolveTaskAction(ctx, f.project, f.task, input)
	if err != nil {
		t.Fatalf("formal candidate start: %v", err)
	}
	started, ok := value.(executionproposal.StartResult)
	if !ok || started.Proposal.ID != receipt.T2.ExecutionID || started.Run.ID == "" {
		t.Fatalf("formal candidate was not reserved and handed off: %+v", value)
	}
	if !started.Handoff.RecordedAccepted() {
		t.Fatalf("formal candidate handoff unconfirmed: %+v", started)
	}
	t.Logf("Console START_EXECUTION kept formal candidate %s and handed off Run %s", started.Proposal.ID, started.Run.ID)
}
