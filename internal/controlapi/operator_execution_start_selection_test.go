package controlapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func operatorStartContext(t *testing.T, project, taskID, realm string) context.Context {
	return operatorActionContext(t, project, taskID, realm, "START_EXECUTION")
}

func operatorActionContext(t *testing.T, project, taskID, realm, action string) context.Context {
	t.Helper()
	i, err := localoperator.NewIngress(realm)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:4777/start", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4777}))
	ctx, err := i.AdmitHTTP(r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = localoperator.BindTask(ctx, project, taskID, action)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestOperatorApprovedDefinitionCannotInventFormalRuntimeSource(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	submitLifecycleApprovalReady(t, f, "operator-authored-definition")
	approvalContext := operatorActionContext(t, f.project, f.task, "operator-author-test", "APPROVE")
	approved, err := f.facade.ResolveReviewApprovalForOperator(approvalContext, f.project, f.task, "APPROVE", wp.ResolveInput{TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), Option: "APPROVE", RequestID: "operator-definition-approve", StartEffect: wp.StartEffectNoStart}, nil)
	if err != nil || approved.Task == nil || approved.Task.CurrentExecutionID != "" {
		t.Fatalf("canonical operator NO_START approval=%+v %v", approved, err)
	}
	current := *approved.Task
	var ownerSession, kind, author string
	if err = f.plane.DB.SQL().QueryRow(`SELECT COALESCE(ai_session_id,''),actor_kind,actor_id FROM review_approval_materializations WHERE decision_id=?`, current.ReviewApprovalID).Scan(&ownerSession, &kind, &author); err != nil || ownerSession != "" || kind != "LOCAL_OPERATOR" || author == "" {
		t.Fatalf("operator approval acquired AI session: %q/%s/%s %v", ownerSession, kind, author, err)
	}
	raw, _ := json.Marshal(current.RepositoryTargets)
	ctx := operatorStartContext(t, f.project, current.ID, "operator-author-test")
	requestID, err := localoperator.RequestID(ctx, f.project, current.ID, "START_EXECUTION", "operator-definition-start")
	if err != nil {
		t.Fatal(err)
	}
	config, err := sqlite.NewCodexConfigStore(f.plane.DB).Get(ctx, codexconfig.DefaultConfigID)
	if err != nil {
		t.Fatal(err)
	}
	backend := config.Selection("OPERATOR_INVENTORY")
	request := wp.OperatorExecutionStartRequest{ProjectID: f.project, TaskID: current.ID, RequestID: requestID, Expected: wp.TaskExecutionExpectation{FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, Phase: task.PhaseImplementationReady, PrimaryMemberID: current.PrimaryMemberID, ApprovedWorksetHash: fmt.Sprintf("%x", sha256.Sum256(raw))}, RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-luna", ReasoningEffort: "low"}, BackendSelection: codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}
	before := operatorStartSnapshot(t, f)
	v, r, err := f.plane.WorkflowProgression.ReserveOperatorSelectedTaskExecution(ctx, request, backend)
	if !errors.Is(err, runtimeselection.ErrBindingMissing) || v.ID != "" || r.ID != "" {
		t.Fatalf("operator-only provenance became formal runtime authority: execution=%s run=%s err=%v", v.ID, r.ID, err)
	}
	if operatorStartSnapshot(t, f) != before {
		t.Fatal("unbound operator start retained selection, candidate, admission, or runtime work")
	}
	if _, _, found, readErr := f.plane.WorkflowProgression.ReadOperatorSelectedTaskExecution(ctx, request); readErr != nil || found {
		t.Fatalf("rejected operator command acquired a receipt: found=%t err=%v", found, readErr)
	}
}

func operatorStartSnapshot(t *testing.T, f *upperLifecycleFixture) string {
	t.Helper()
	var rows string
	if err := f.plane.DB.SQL().QueryRow(`SELECT COALESCE(group_concat(request_json||payload_sha256||runtime_config_json||backend_selection_json||created_at,''),'') FROM operator_execution_start_commands ORDER BY project_id,actor_id,request_id`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return definitionRuntimeSnapshot(t, f, true) + rows
}

func TestOperatorExplicitDefinitionStartCannotBorrowAIFormalSource(t *testing.T) {
	withDefinitionOnlyCodingReview(t, func(f *upperLifecycleFixture, current task.Task, prior review.Review) {
		t := f.t
		alternate, _ := definitionOnlyAlternateBackend(t, f)
		backend := alternate.Selection("OPERATOR_INVENTORY")
		selected := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-luna", ReasoningEffort: "low"}
		if selected == prior.RuntimeConfig {
			t.Fatal("fixture runtime must differ")
		}
		raw, _ := json.Marshal(current.RepositoryTargets)
		expected := wp.TaskExecutionExpectation{FeatureAreaID: current.FeatureAreaID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, Phase: task.PhaseImplementationReady, PrimaryMemberID: current.PrimaryMemberID, ApprovedWorksetHash: fmt.Sprintf("%x", sha256.Sum256(raw))}
		ctx := operatorStartContext(t, f.project, current.ID, "operator-start-test")
		input := controlapi.OperatorApprovedTaskStartInput{ProjectID: f.project, TaskID: current.ID, RequestID: "explicit-operator-start", Expected: expected, RuntimeConfig: selected, BackendSelection: codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}
		console, err := consoleapi.New(f.plane, f.facade)
		if err != nil {
			t.Fatal(err)
		}
		webTask, err := console.GetProductTask(ctx, f.project, current.ID)
		if err != nil {
			t.Fatal(err)
		}
		var browserInput consoleapi.TaskActionInput
		for _, capability := range webTask.OperatorActions {
			if capability.Action == "START_EXECUTION" {
				if capability.DomainAvailable || !strings.Contains(capability.Reason, "FORMAL_START_CONFIRMATION_REQUIRED") {
					t.Fatalf("unbound Web start advertised as executable: %+v", capability)
				}
				browserInput = consoleapi.TaskActionInput{Action: capability.Action, Target: capability.Target, RequestID: input.RequestID, NextRunRuntimeConfig: &selected, BackendSelection: &input.BackendSelection}
			}
		}
		if browserInput.Target == nil || browserInput.Target.PrimaryMemberID != expected.PrimaryMemberID || browserInput.Target.ApprovedWorksetHash != expected.ApprovedWorksetHash {
			t.Fatalf("Web start target unavailable/incomplete: %+v", webTask.OperatorActions)
		}
		var inputs []string
		for _, capability := range webTask.Presentation.TaskView.Actions {
			if capability.Action == "START_EXECUTION" {
				inputs = capability.RequiredInputs
			}
		}
		if !reflect.DeepEqual(inputs, []string{"nextRunRuntimeConfig", "backendSelection"}) {
			t.Fatalf("Web start selection inputs=%v", inputs)
		}
		requestID, err := localoperator.RequestID(ctx, f.project, current.ID, "START_EXECUTION", input.RequestID)
		if err != nil {
			t.Fatal(err)
		}
		request := wp.OperatorExecutionStartRequest{ProjectID: input.ProjectID, TaskID: input.TaskID, RequestID: requestID, Expected: expected, RuntimeConfig: selected, BackendSelection: input.BackendSelection}
		store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
		actor, _ := localoperator.FromContext(ctx)
		before := operatorStartSnapshot(t, f)
		if _, found, readErr := f.facade.ReadApprovedTaskStartForOperator(ctx, input); readErr != nil || found {
			t.Fatalf("missing command read: found=%t err=%v", found, readErr)
		}
		if _, _, err = store.ReserveTaskExecutionExpectedForOperator(ctx, f.project, current.ID, "START_EXECUTION", actor.PrincipalID(), "raw-no-selection", &expected); !errors.Is(err, runtimeselection.ErrBindingMissing) {
			t.Fatalf("raw operator inherited runtime: %v", err)
		}
		if _, _, err = store.ReserveOperatorSelectedTaskExecution(f.ctx, request, backend); !errors.Is(err, localoperator.ErrAccessDenied) {
			t.Fatalf("AI context acted as operator: %v", err)
		}
		if operatorStartSnapshot(t, f) != before {
			t.Fatal("rejected authority wrote facts")
		}
		// The definition has a genuine AI formal approval archive, and the
		// backend has an explicitly installed test-only platform qualification.
		// Neither authorizes an operator command to synthesize the missing
		// START_APPROVED formal source and exact revision binding.
		for _, stage := range []string{"same-process", "cold-reopen"} {
			if stage == "cold-reopen" {
				reopenDirectDeliveryFixture(t, f)
				f.plane.Retirement.Close()
				store = sqlite.NewWorkflowProgressionStore(f.plane.DB)
				console, err = consoleapi.New(f.plane, f.facade)
				if err != nil {
					t.Fatal(err)
				}
			}
			before = operatorStartSnapshot(t, f)
			v, r, reserveErr := store.ReserveOperatorSelectedTaskExecution(ctx, request, backend)
			if !errors.Is(reserveErr, runtimeselection.ErrBindingMissing) || v.ID != "" || r.ID != "" {
				t.Fatalf("%s unbound operator selection: execution=%s run=%s err=%v", stage, v.ID, r.ID, reserveErr)
			}
			webResult, startErr := console.ResolveTaskAction(ctx, f.project, current.ID, browserInput)
			var public *controlapi.Error
			if !errors.As(startErr, &public) || public.Code != "FORMAL_START_CONFIRMATION_REQUIRED" {
				t.Fatalf("%s Web selection bypassed formal source: %+v", stage, webResult)
			}
			if result, ok := webResult.(executionproposal.StartResult); ok && (result.Proposal.ID != "" || result.Run.ID != "" || result.Receipt != nil) {
				t.Fatalf("%s rejected Web selection exposed nonexistent receipt: %+v", stage, result)
			}
			if _, found, readErr := f.facade.ReadApprovedTaskStartForOperator(ctx, input); readErr != nil || found {
				t.Fatalf("%s rejected command became readable: found=%t err=%v", stage, found, readErr)
			}
			if operatorStartSnapshot(t, f) != before {
				t.Fatalf("%s missing formal authority wrote facts", stage)
			}
		}
	})
}
