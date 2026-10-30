package controlapi

import (
	"context"
	"fmt"
	"github.com/lazyun/devbridge/internal/task"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
)

type capabilityConversationStub struct {
	cancel, recover agentconversation.Eligibility
}

type cancellationLiveRunner struct {
	Runner
	available bool
}

func (r cancellationLiveRunner) ExecutionCancellationAvailable(string) bool { return r.available }

type recoveredConversationStub struct {
	capabilityConversationStub
	receipts []agentconversation.RecoveryReceipt
}

func (s recoveredConversationStub) ListByFeatureArea(context.Context, string) ([]agentconversation.Conversation, error) {
	return nil, nil
}
func (s recoveredConversationStub) ListRecoveryReceipts(context.Context, string) ([]agentconversation.RecoveryReceipt, error) {
	return s.receipts, nil
}

func TestRecoveredCurrentContextPreservesHistoricalUnknown(t *testing.T) {
	receipt := agentconversation.RecoveryReceipt{FeatureAreaID: "area", OperationKind: agentconversation.Execution, OperationID: "execution", RunID: "old-run", NewConversationID: "new-conversation", CreatedAt: time.Unix(3, 0)}
	f := &Facade{deps: Dependencies{AgentConversations: recoveredConversationStub{receipts: []agentconversation.RecoveryReceipt{receipt}}}}
	for _, conversation := range []string{"new-conversation", "different-conversation"} {
		out := presentation.Projection{WorkflowStatus: "INFRASTRUCTURE_FAILED", CurrentPhase: "IMPLEMENTATION", Details: presentation.Details{ExecutionID: "execution"}, Progress: []presentation.PhaseProgress{{Phase: "IMPLEMENTATION", Status: "UNKNOWN", SourceID: "old-run"}}}
		current := CurrentWorkflow{Topic: &featurearea.FeatureArea{ID: "area"}, Execution: &executionproposal.Proposal{ID: "execution", ImplementationRunID: "old-run"}}
		err := f.projectRecoveredOperation(context.Background(), current, agentconversation.State{Status: "READY", AgentConversationID: conversation}, &out)
		if err != nil {
			t.Fatal(err)
		}
		if conversation == "new-conversation" {
			if out.WorkflowStatus != "READY" || out.Recovery == nil || out.Recovery.Required || out.Recovery.Status != "RECOVERED" {
				t.Fatalf("current recovery=%+v", out)
			}
		} else if out.WorkflowStatus != "INFRASTRUCTURE_FAILED" || out.Recovery != nil {
			t.Fatal("foreign recovery changed current state")
		}
		if out.Progress[0].Status != "UNKNOWN" || out.Progress[0].SourceID != "old-run" {
			t.Fatal("historical UNKNOWN was rewritten")
		}
	}
}

func TestRecoveredOperationCannotOverrideNewDecisionOrDifferentRun(t *testing.T) {
	receipt := agentconversation.RecoveryReceipt{FeatureAreaID: "area", OperationKind: agentconversation.Execution, OperationID: "execution", RunID: "old-run", NewConversationID: "new-conversation"}
	f := &Facade{deps: Dependencies{AgentConversations: recoveredConversationStub{receipts: []agentconversation.RecoveryReceipt{receipt}}}}
	current := CurrentWorkflow{Topic: &featurearea.FeatureArea{ID: "area"}, Execution: &executionproposal.Proposal{ID: "execution", ImplementationRunID: "new-run"}}
	out := presentation.Projection{WorkflowStatus: "INFRASTRUCTURE_FAILED", CurrentPhase: "IMPLEMENTATION", Details: presentation.Details{ExecutionID: "execution"}}
	if err := f.projectRecoveredOperation(context.Background(), current, agentconversation.State{Status: "READY", AgentConversationID: "new-conversation"}, &out); err != nil || out.WorkflowStatus != "INFRASTRUCTURE_FAILED" {
		t.Fatalf("different run recovery=%+v err=%v", out, err)
	}
	current.Execution.ImplementationRunID = "old-run"
	out.PendingDecision = &presentation.Decision{ID: "new-decision"}
	if err := f.projectRecoveredOperation(context.Background(), current, agentconversation.State{Status: "READY", AgentConversationID: "new-conversation"}, &out); err != nil || out.WorkflowStatus != "INFRASTRUCTURE_FAILED" {
		t.Fatalf("pending decision recovery=%+v err=%v", out, err)
	}
}

func TestResolvedUnknownCancellationIsHistoricalAfterRecovery(t *testing.T) {
	resolved := time.Unix(4, 0)
	store := &cancellationReadStub{receipts: []executioncancel.Receipt{{ProjectID: "project", ExecutionID: "execution", RunID: "old-run", State: "RECOVERY_REQUIRED", RecoveryRequired: true, ResolvedAt: &resolved}}}
	f := &Facade{deps: Dependencies{AISessions: scopeSessions{"session": aisession.Session{ID: "session", ProjectID: "project"}}, ExecutionCancellations: store}}
	var out presentation.Projection
	if err := f.projectCancellationReceipt(context.Background(), "session", CurrentWorkflow{Execution: &executionproposal.Proposal{ID: "execution"}}, agentconversation.State{Status: "READY"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Cancellation != nil {
		t.Fatalf("resolved historical unknown still blocks current card: %+v", out.Cancellation)
	}
	if !store.receipts[0].RecoveryRequired || store.receipts[0].State != "RECOVERY_REQUIRED" {
		t.Fatal("read changed historical receipt")
	}
}

func (s capabilityConversationStub) Reset(context.Context, string, string) (agentconversation.Conversation, error) {
	return agentconversation.Conversation{}, nil
}
func (s capabilityConversationStub) State(context.Context, string) (agentconversation.State, error) {
	return agentconversation.State{}, nil
}
func (s capabilityConversationStub) Recover(context.Context, agentconversation.RecoverInput) (agentconversation.RecoveryResult, error) {
	return agentconversation.RecoveryResult{}, nil
}
func (s capabilityConversationStub) CancelEligibility(context.Context, string) (agentconversation.Eligibility, error) {
	return s.cancel, nil
}
func (s capabilityConversationStub) RecoveryEligibility(context.Context, string) (agentconversation.Eligibility, error) {
	return s.recover, nil
}

func TestEnrichFeatureAreaActionCapabilitiesMapsOnlyExactAvailableTargets(t *testing.T) {
	target := &agentconversation.ActionTarget{FeatureAreaID: "area", ExecutionID: "execution", OperationKind: agentconversation.Execution, OperationID: "execution", RunID: "run-exact", AgentConversationID: "conversation"}
	f := &Facade{deps: Dependencies{AgentConversations: capabilityConversationStub{cancel: agentconversation.Eligibility{Available: true, Target: target}, recover: agentconversation.Eligibility{Available: true, Target: target}}}}
	out := presentation.Projection{Recovery: &presentation.Recovery{Required: true}, Capabilities: []presentation.Capability{{Action: "RECOVER", Available: false}}}
	current := CurrentWorkflow{Topic: &featurearea.FeatureArea{ID: "area"}}
	op := agentconversation.State{Claim: &agentconversation.Claim{Kind: agentconversation.Execution, OperationID: "execution", RunID: "run-exact"}, AgentConversationID: "conversation"}
	// No current Execution in this mapping fixture: cancellation must fail
	// closed even if a newer eligibility read reports an active target.
	if err := f.enrichFeatureAreaActionCapabilities(context.Background(), "session", current, op, &out); err != nil {
		t.Fatal(err)
	}
	var cancel, recover presentation.Capability
	for _, capability := range out.Capabilities {
		switch capability.Action {
		case "CANCEL_EXECUTION":
			cancel = capability
		case "RECOVER":
			recover = capability
		}
	}
	if cancel.Available || cancel.Target != nil {
		t.Fatalf("cancel capability=%+v", cancel)
	}
	if !recover.Available || recover.Target == nil || recover.Target.OperationKind != "EXECUTION" {
		t.Fatalf("recovery capability=%+v", recover)
	}
	f.deps.AISessions = scopeSessions{"session": aisession.Session{ID: "session", ProjectID: "project"}}
	f.deps.ExecutionCancellations = &cancellationReadStub{}
	f.deps.Runner = cancellationLiveRunner{available: true}
	current.Execution = &executionproposal.Proposal{ID: "execution"}
	out = presentation.Projection{}
	if err := f.enrichFeatureAreaActionCapabilities(context.Background(), "session", current, op, &out); err != nil {
		t.Fatal(err)
	}
	for _, cap := range out.Capabilities {
		if cap.Action == "CANCEL_EXECUTION" && (!cap.Available || cap.Target == nil || cap.Target.RunID != "run-exact") {
			t.Fatalf("exact cancel target lost: %+v", cap)
		}
	}
	f.deps.Runner = cancellationLiveRunner{available: false}
	out = presentation.Projection{}
	if err := f.enrichFeatureAreaActionCapabilities(context.Background(), "session", current, op, &out); err != nil {
		t.Fatal(err)
	}
	for _, cap := range out.Capabilities {
		if cap.Action == "CANCEL_EXECUTION" && cap.Available {
			t.Fatal("durable RUNNING alone granted cancellation without runtime owner")
		}
	}
}

func TestActionEligibilityCannotReplaceObservedOperation(t *testing.T) {
	current := CurrentWorkflow{Topic: &featurearea.FeatureArea{ID: "area"}}
	op := agentconversation.State{Claim: &agentconversation.Claim{Kind: agentconversation.Review, OperationID: "review-old", RunID: "run-old"}, AgentConversationID: "conversation-old"}
	target := &agentconversation.ActionTarget{FeatureAreaID: "area", OperationKind: agentconversation.Review, OperationID: "review-new", RunID: "run-new", AgentConversationID: "conversation-new"}
	f := &Facade{deps: Dependencies{AgentConversations: capabilityConversationStub{recover: agentconversation.Eligibility{Available: true, Target: target}}}}
	out := presentation.Projection{Recovery: &presentation.Recovery{Required: true}}
	if err := f.enrichFeatureAreaActionCapabilities(context.Background(), "session", current, op, &out); err != nil {
		t.Fatal(err)
	}
	for _, cap := range out.Capabilities {
		if cap.Action == "RECOVER" && (cap.Available || cap.Target != nil) {
			t.Fatalf("mismatched recovery target=%+v", cap)
		}
	}
}

func TestEnrichFeatureAreaActionCapabilitiesFailsClosed(t *testing.T) {
	f := &Facade{deps: Dependencies{AgentConversations: capabilityConversationStub{cancel: agentconversation.Eligibility{Reason: "stale run"}, recover: agentconversation.Eligibility{Reason: "wrong epoch"}}}}
	out := presentation.Projection{Recovery: &presentation.Recovery{Required: true}}
	if err := f.enrichFeatureAreaActionCapabilities(context.Background(), "session", CurrentWorkflow{Topic: &featurearea.FeatureArea{ID: "area"}}, agentconversation.State{}, &out); err != nil {
		t.Fatal(err)
	}
	for _, capability := range out.Capabilities {
		if (capability.Action == "CANCEL_EXECUTION" || capability.Action == "RECOVER") && (capability.Available || capability.Target != nil || capability.Reason == "") {
			t.Fatalf("ineligible capability=%+v", capability)
		}
	}
}

func TestEnrichFeatureAreaActionCapabilitiesProjectsReceiptTruth(t *testing.T) {
	for _, tc := range []struct {
		name, receipt, want string
		required            bool
	}{
		{"requested", "CANCEL_REQUESTED", "CANCEL_REQUESTED", false},
		{"unknown", "RECOVERY_REQUIRED", "OUTCOME_UNKNOWN", true},
		{"terminal", "CANCELED", "CANCELED", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &cancellationReadStub{receipts: []executioncancel.Receipt{{ProjectID: "project", ExecutionID: "execution", RunID: "run", State: tc.receipt, RecoveryRequired: tc.required}}}
			f := &Facade{deps: Dependencies{AISessions: scopeSessions{"session": aisession.Session{ID: "session", ProjectID: "project"}}, AgentConversations: capabilityConversationStub{}, ExecutionCancellations: store}}
			out := presentation.Projection{}
			current := CurrentWorkflow{Topic: &featurearea.FeatureArea{ID: "area"}, Execution: &executionproposal.Proposal{ID: "execution"}}
			op := agentconversation.State{Claim: &agentconversation.Claim{Kind: agentconversation.Execution, RunID: "run"}}
			if err := f.enrichFeatureAreaActionCapabilities(context.Background(), "session", current, op, &out); err != nil {
				t.Fatal(err)
			}
			if out.Cancellation == nil || out.Cancellation.State != tc.want || out.Cancellation.RunID != "run" || out.Cancellation.RecoveryRequired != tc.required {
				t.Fatalf("receipt projection=%+v", out.Cancellation)
			}
		})
	}
}

func TestWorkerUnsupportedRecoveryProjectsAcrossSelectedOtherTask(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(fmt.Sprintf("historical-%t", historical), func(t *testing.T) {
			const reason = "Same-Run worker continuation is unsupported; inspect the current owner and use explicit cancellation."
			f := &Facade{deps: Dependencies{AgentConversations: capabilityConversationStub{recover: agentconversation.Eligibility{ReasonCode: agentconversation.ReasonUpperWorkerContinuationUnsupported, Reason: reason}}}}
			f.deps.ExecutionCancellations = &preWorkCancellationRead{available: !historical}
			selected := &task.Task{Type: task.TypeDevelopment, ID: "other-task", ProjectID: "project", FeatureAreaID: "area", Status: task.StatusActive, Phase: task.PhaseReviewRequired}
			if historical {
				selected.Status = task.StatusSucceeded
			}
			current := CurrentWorkflow{Topic: &featurearea.FeatureArea{ID: "area"}, Task: selected}
			op := agentconversation.State{Status: "RECOVERY_REQUIRED", Claim: &agentconversation.Claim{Kind: agentconversation.Review, OperationID: "worker-review", RunID: "worker-run"}}
			out := presentation.Projection{Recovery: &presentation.Recovery{Required: true, Status: "RECOVERY_REQUIRED", Reason: "GPT_REVIEW_AUTHORIZATION_REQUIRED", Summary: "reauthorize"}, CurrentOperation: &presentation.Operation{Kind: "REVIEW", ID: "worker-review", RunID: "worker-run"}}
			if err := f.enrichFeatureAreaActionCapabilities(context.Background(), "session", current, op, &out); err != nil {
				t.Fatal(err)
			}
			if out.Recovery.Reason != agentconversation.ReasonUpperWorkerContinuationUnsupported || out.Recovery.Summary != reason || !out.Recovery.Required {
				t.Fatalf("recovery=%+v", out.Recovery)
			}
			if !historical {
				found := false
				for _, cap := range out.Capabilities {
					if cap.Action == "CANCEL_EXECUTION" && cap.Available && cap.Target != nil && cap.Target.OperationKind == "TASK" && cap.Target.OperationID == selected.ID {
						found = true
					}
				}
				if !found {
					t.Fatal("pre-work selected Task cancellation capability was lost")
				}
			}
			if action := presentation.CurrentAction(out); action.Code != "REFRESH" {
				t.Fatalf("foreign selected Task action=%+v", action)
			}
		})
	}
}
