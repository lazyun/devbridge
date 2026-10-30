package controlapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
)

func TestPendingFromProjectionRetainsOnlyExactDecisionTargets(t *testing.T) {
	value := presentation.Projection{
		CurrentState:    "REVIEW_APPROVAL_PENDING",
		UpdatedAt:       time.Unix(10, 0),
		PendingDecision: &presentation.Decision{Kind: "REVIEW_APPROVAL", ID: "REVIEW_APPROVAL:review-a", SubjectID: "task-a", AllowedActions: []string{"APPROVE", "REVISE", "REJECT"}},
		Capabilities: []presentation.Capability{
			{Action: "APPROVE", Available: true, Target: &presentation.ActionTarget{TaskID: "task-a", FeatureAreaID: "area-a", DecisionID: "REVIEW_APPROVAL:review-a", SubjectID: "task-a"}},
			{Action: "RETIRE", Available: true, Target: &presentation.ActionTarget{FeatureAreaID: "area-a"}},
		},
	}
	got := pendingFromProjection(value, "TASK", "task-a", "area-a")
	if got.Decision == nil || got.Decision.ID != "REVIEW_APPROVAL:review-a" || got.SemanticState != "REVIEW_APPROVAL_PENDING" || len(got.Options) != 1 || got.Options[0].Target.DecisionID != got.Decision.ID {
		t.Fatalf("pending decision lost exact authority: %+v", got)
	}
	if len(got.RequiredInput) == 0 || got.RequiredInput[0] != "requestId" {
		t.Fatalf("review replay requirement missing: %+v", got.RequiredInput)
	}
}

func TestRuntimePendingDecisionKeepsRuntimeSeparateFromTaskDecision(t *testing.T) {
	permissions := json.RawMessage(`[{"permission":"filesystem.write"}]`)
	got := runtimePendingDecision(approval.Approval{ID: "approval-a", RunID: "run-a", Kind: approval.KindPermissions, Status: approval.StatusPending, WorkflowOwnedRun: true, RequestPayload: json.RawMessage(`{"permissions":[{"permission":"filesystem.write"}],"authorization":"secret"}`), AllowedDecisions: []approval.DecisionType{approval.DecisionGrantRequestedPermissions, approval.DecisionDecline}, CreatedAt: time.Unix(20, 0)})
	if got.Kind != "RUNTIME_APPROVAL" || got.Owner != "RUNTIME" || got.Decision == nil || got.Decision.ID != "RUNTIME_APPROVAL:approval-a" || len(got.Options) != 2 {
		t.Fatalf("runtime projection=%+v", got)
	}
	for _, option := range got.Options {
		if option.Target == nil || option.Target.SubjectID != "approval-a" || option.Target.RunID != "run-a" || !option.Available {
			t.Fatalf("runtime option leaked inferred authority: %+v", option)
		}
	}
	if got.Decision == nil || string(got.Decision.Permissions) != string(permissions) {
		t.Fatalf("requested permissions were not propagated: %+v", got.Decision)
	}
	if got.Decision.CancelEffect != "DECLINE_ACTION" || got.RuntimeApprovalRequest == nil || got.RuntimeApprovalRequestTruncated || string(got.RuntimeApprovalRequest) == `{"permissions":[{"permission":"filesystem.write"}],"authorization":"secret"}` || !strings.Contains(string(got.RuntimeApprovalRequest), "[REDACTED]") {
		t.Fatalf("runtime approval request presentation is unsafe or incomplete: %+v", got)
	}
	legacy := runtimePendingDecision(approval.Approval{ID: "legacy", RunID: "run-legacy", Kind: approval.KindLegacyCommand, Status: approval.StatusPending, AllowedDecisions: []approval.DecisionType{approval.DecisionCancel}})
	if legacy.Decision == nil || legacy.Decision.CancelEffect != "CANCEL_LEGACY_RUN" {
		t.Fatalf("legacy approval cancel effect=%+v", legacy.Decision)
	}
}

func TestRuntimeRequestPendingDecisionKeepsRequestUserInputWithoutChoices(t *testing.T) {
	created := time.Unix(30, 0)
	request := agentruntime.Request{
		Identity:  agentruntime.Identity{ConnectionID: "connection-a"},
		RequestID: json.RawMessage(`"request-a"`),
		ItemID:    "item-a",
		Method:    "item/tool/requestUserInput",
		Payload:   json.RawMessage(`{"question":"continue?"}`),
		State:     "PENDING",
		CreatedAt: created,
	}
	got := runtimeRequestPendingDecision(run.Run{ID: "run-a", TaskID: "stale-task", FeatureAreaID: "stale-area"}, request, task.Task{ID: "task-a", FeatureAreaID: "area-a"})
	if got.Kind != "RUNTIME_REQUEST" || got.Owner != "RUNTIME" || got.TaskID != "task-a" || got.FeatureAreaID != "area-a" || got.RuntimeRequest == nil {
		t.Fatalf("runtime request projection=%+v", got)
	}
	if len(got.RuntimeRequest.AvailableResponses) != 0 || got.RuntimeRequest.Method != "item/tool/requestUserInput" || string(got.RuntimeRequest.RequestID) != `"request-a"` {
		t.Fatalf("requestUserInput was changed or filtered: %+v", got.RuntimeRequest)
	}
	if got.RuntimeRequestActionable || len(got.Options) != 0 {
		t.Fatalf("historical request must stay read-only: %+v", got)
	}
	if got.RuntimeOptions != nil || got.DefaultModel != "" {
		t.Fatalf("runtime request must not advertise next-run configuration: %+v", got)
	}
}

func TestRuntimeRequestDecisionSeparatesWaitingRecordedAndUnknown(t *testing.T) {
	identity := agentruntime.Identity{ProjectID: "project-a", FeatureAreaID: "area-a", TaskID: "task-a", RunID: "run-a", ThreadID: "thread-a", TurnID: "turn-a", ConnectionID: "connection-a", AgentConversationID: "conversation-a", RuntimeEpoch: "epoch-a", OperationKind: "EXECUTION", OperationID: "execution-a"}
	base := agentruntime.Request{Identity: identity, ID: "runtime-request-a", Kind: "CODEX_REQUEST", AvailableActions: []string{"RESPOND", "STOP_AND_REVISE", "CANCEL_OPERATION"}, CreatedAt: time.Unix(30, 0), UpdatedAt: time.Unix(31, 0)}
	for _, tc := range []struct {
		state      string
		actionable bool
	}{
		{state: "WAITING_FOR_USER"},
		{state: "USER_DECISION_RECORDED"},
		{state: "UNKNOWN"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			request := base
			request.State = tc.state
			got := runtimeRequestPendingDecision(run.Run{ID: "run-a", TaskID: "task-a", FeatureAreaID: "area-a"}, request, task.Task{ID: "task-a", FeatureAreaID: "area-a"})
			if got.Kind != "CODEX_REQUEST" || got.SemanticState != tc.state || got.RuntimeRequestActionable != tc.actionable || got.RuntimeRequest == nil || got.RuntimeRequest.ID != request.ID {
				t.Fatalf("projection=%+v", got)
			}
			if tc.actionable {
				if len(got.Options) != 1 || got.Options[0].Action != "SUBMIT_AGENT_USER_DECISION" || got.Options[0].Target == nil || got.Options[0].Target.SubjectID != request.ID || got.Options[0].Target.AgentConversationID != identity.AgentConversationID {
					t.Fatalf("action target=%+v", got.Options)
				}
			} else if len(got.Options) != 0 {
				t.Fatalf("non-waiting request must not expose action: %+v", got.Options)
			}
		})
	}
}

func TestProjectPendingDecisionsPartialErrorIsAWarningOnly(t *testing.T) {
	err := &projectPendingDecisionsPartialError{featureAreaReads: 1, taskReads: 2, approvalRunReads: 1}
	if !err.hasFailures() {
		t.Fatal("partial detail failures must be surfaced")
	}
	if got := err.Error(); got != "Some pending-decision details could not be read (1 area, 2 task, 1 approval owner); refresh before resolving." {
		t.Fatalf("warning=%q", got)
	}
	if (&projectPendingDecisionsPartialError{}).hasFailures() {
		t.Fatal("empty partial result must not create a warning")
	}
}

func TestProjectPendingDecisionsRejectsForeignRunAndRequestProject(t *testing.T) {
	now := time.Unix(40, 0)
	facade := &Facade{deps: Dependencies{
		AISessions:   scopeSessions{"session-a": {ID: "session-a", ProjectID: "project-a"}},
		FeatureAreas: &inventoryAreas{values: []featurearea.FeatureArea{}},
		Tasks: scopeTasks{
			"task-a": {ID: "task-a", ProjectID: "project-a", FeatureAreaID: "area-a"},
			"task-b": {ID: "task-b", ProjectID: "project-b", FeatureAreaID: "area-b"},
		},
		Runs: scopeRuns{
			"run-a": {ID: "run-a", ProjectID: "project-a", TaskID: "task-a", FeatureAreaID: "area-a", AgentRuntime: &agentruntime.Snapshot{Requests: []agentruntime.Request{
				{Identity: agentruntime.Identity{ProjectID: "project-a", ConnectionID: "connection-a"}, RequestID: json.RawMessage(`"allowed"`), ItemID: "item-allowed", Method: "item/tool/requestUserInput", State: "PENDING", CreatedAt: now},
				{Identity: agentruntime.Identity{ProjectID: "project-b", ConnectionID: "connection-a"}, RequestID: json.RawMessage(`"mismatched"`), ItemID: "item-mismatched", Method: "item/tool/requestUserInput", State: "PENDING", CreatedAt: now},
			}}},
			"run-b": {ID: "run-b", ProjectID: "project-b", TaskID: "task-b", FeatureAreaID: "area-b", AgentRuntime: &agentruntime.Snapshot{Requests: []agentruntime.Request{{Identity: agentruntime.Identity{ProjectID: "project-b", ConnectionID: "connection-b"}, RequestID: json.RawMessage(`"foreign"`), ItemID: "item-foreign", Method: "item/tool/requestUserInput", State: "PENDING", CreatedAt: now}}}},
		},
		Approvals: &approvalServiceStub{},
	}}
	got, err := facade.ProjectPendingDecisionsForSession(context.Background(), "session-a")
	if _, ok := err.(*projectPendingDecisionsPartialError); !ok {
		t.Fatalf("detail skip must preserve inventory with partial warning: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "RUNTIME_REQUEST" || got[0].TaskID != "task-a" || got[0].RuntimeRequest == nil || string(got[0].RuntimeRequest.RequestID) != `"allowed"` {
		t.Fatalf("foreign run/request leaked into current project: %+v", got)
	}
}

func TestProjectPendingDecisionsPaginatesAllPendingApprovals(t *testing.T) {
	now := time.Unix(50, 0)
	approvals := make([]approval.Approval, 0, MaxListLimit+1)
	for i := 0; i <= MaxListLimit; i++ {
		approvals = append(approvals, approval.Approval{ID: fmt.Sprintf("approval-%03d", i), RunID: "run-a", Status: approval.StatusPending, Kind: approval.KindCommandExecution, AllowedDecisions: []approval.DecisionType{approval.DecisionApproveOnce}, CreatedAt: now.Add(time.Duration(i) * time.Second)})
	}
	facade := &Facade{deps: Dependencies{
		AISessions:   scopeSessions{"session-a": {ID: "session-a", ProjectID: "project-a"}},
		FeatureAreas: &inventoryAreas{values: []featurearea.FeatureArea{}},
		Tasks:        scopeTasks{"task-a": {ID: "task-a", ProjectID: "project-a", FeatureAreaID: "area-a"}},
		Runs:         scopeRuns{"run-a": {ID: "run-a", ProjectID: "project-a", TaskID: "task-a", FeatureAreaID: "area-a"}},
		Approvals:    &approvalServiceStub{values: approvals},
	}}
	got, err := facade.ProjectPendingDecisionsForSession(context.Background(), "session-a")
	if _, ok := err.(*projectPendingDecisionsPartialError); !ok {
		t.Fatalf("detail skip must preserve inventory with partial warning: %v", err)
	}
	if len(got) != MaxListLimit+1 {
		t.Fatalf("pending approval inventory was truncated: got %d want %d", len(got), MaxListLimit+1)
	}
	for _, item := range got {
		if item.Kind != "RUNTIME_APPROVAL" || item.TaskID != "task-a" || item.Owner != "TASK" {
			t.Fatalf("approval lost exact current-project owner: %+v", item)
		}
	}
}

func TestPendingFromProjectionDeduplicatesOnlyExactDecisionOptions(t *testing.T) {
	target := &presentation.ActionTarget{TaskID: "task", FeatureAreaID: "area", DecisionID: "decision", SubjectID: "task"}
	cap := presentation.Capability{Action: "APPROVE", Available: true, Target: target}
	wrong := *target
	wrong.TaskID = "other"
	value := presentation.Projection{PendingDecision: &presentation.Decision{Kind: "REVIEW_APPROVAL", ID: "decision", AllowedActions: []string{"APPROVE"}}, Capabilities: []presentation.Capability{cap, cap, {Action: "APPROVE", Available: true, Target: &wrong}, {Action: "RETIRE", Available: true, Target: target}, {Action: "RECOVER", Available: true, Target: target}, {Action: "CANCEL_EXECUTION", Available: true, Target: target}}}
	got := pendingFromProjection(value, "TASK", "task", "area")
	if len(got.Options) != 1 || got.Options[0].Target.TaskID != "task" || got.Options[0].Action != "APPROVE" {
		t.Fatalf("decision options=%+v", got.Options)
	}
}
