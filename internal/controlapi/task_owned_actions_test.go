package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"testing"
)

type taskActionExecutions struct {
	ExecutionProposalService
	values []executionproposal.Proposal
}

type taskActionTasks struct{ TaskService }

func (taskActionTasks) GetDefinitionRevision(context.Context, string) (task.DefinitionRevision, error) {
	return task.DefinitionRevision{ID: "revision", TaskID: "task", Definition: task.Definition{ProjectID: "project", FeatureAreaID: "area", WorkSpec: task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent}}}, nil
}

func (s taskActionExecutions) ListByTask(context.Context, string) ([]executionproposal.Proposal, error) {
	return s.values, nil
}

func TestTaskActionsFollowPhaseAndAuthoritativeGuidance(t *testing.T) {
	cases := []struct {
		phase   task.Phase
		next    string
		actions []string
	}{
		{task.PhaseReviewRequired, "START_TASK_REVIEW", []string{"START_TASK_REVIEW"}},
		{task.PhaseReviewing, "WAIT_FOR_REVIEW_COMPLETION", nil},
		{task.PhaseReviewApprovalPending, "RESOLVE_REVIEW_APPROVAL", []string{"APPROVE", "REVISE", "REJECT"}},
		{task.PhaseImplementationReady, "START_EXECUTION", []string{"START_EXECUTION"}},
		{task.PhaseImplementing, "WAIT_FOR_IMPLEMENTATION", nil},
		{task.PhaseVerificationReady, "START_VERIFICATION", []string{"START_VERIFICATION"}},
		{task.PhaseVerifying, "WAIT_FOR_VERIFICATION", nil},
		// Acceptance actions require the exact durable decision and Run evidence;
		// phase and guidance alone deliberately authorize nothing.
		{task.PhaseAcceptancePending, "RESOLVE_TASK_ACCEPTANCE", nil},
	}
	for _, tc := range cases {
		t.Run(string(tc.phase), func(t *testing.T) {
			owner := task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Status: task.StatusActive, Phase: tc.phase}
			if tc.phase == task.PhaseReviewApprovalPending {
				owner.CurrentReviewID = "review"
			}
			if tc.phase == task.PhaseImplementationReady || tc.phase == task.PhaseImplementing || tc.phase == task.PhaseVerificationReady || tc.phase == task.PhaseVerifying || tc.phase == task.PhaseAcceptancePending {
				owner.CurrentExecutionID, owner.CurrentDefinitionRevisionID = "execution", "revision"
				owner.Work.ExecutionProposalID = "execution"
				owner.WorkSpec.ImplementationMode = task.ImplementationCodingAgent
			}
			g := wp.Guidance{ProjectID: owner.ProjectID, TaskID: owner.ID, FeatureAreaID: owner.FeatureAreaID, TaskPhase: tc.phase, WorkflowMode: "CURRENT", CurrentTask: &wp.TaskFact{ID: owner.ID, Status: "ACTIVE", Phase: tc.phase}, LatestTask: &wp.TaskFact{ID: owner.ID, Status: "ACTIVE", Phase: tc.phase}, ReviewID: owner.CurrentReviewID, ExecutionID: owner.CurrentExecutionID, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, DecisionID: "decision", NextAction: wp.Action{Code: tc.next}}
			if tc.phase == task.PhaseReviewApprovalPending {
				g.Approval = &wp.Approval{Status: "PENDING", TaskID: owner.ID, ReviewID: owner.CurrentReviewID, DecisionID: "decision", AllowedActions: tc.actions}
			}
			projection := p.Projection{Details: p.Details{ProjectID: owner.ProjectID, FeatureAreaID: owner.FeatureAreaID, TaskID: owner.ID}}
			applyProgressionPresentation(&projection, g)
			facade := Facade{deps: Dependencies{Tasks: taskActionTasks{}, Runs: approvalRunStub{}, ExecutionProposals: taskActionExecutions{values: []executionproposal.Proposal{{ID: "execution", TaskID: "task", ProjectID: "project", TaskDefinitionRevisionID: "revision", WorkSpec: task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "Explicitly waived"}}}}}}}
			if err := facade.attachTaskView(context.Background(), &projection, owner); err != nil {
				t.Fatal(err)
			}
			assembleTaskActions(&projection, owner)
			got := map[string]bool{}
			for _, cap := range projection.TaskView.Actions {
				if cap.Available {
					got[cap.Action] = true
					if cap.Target == nil || cap.Target.TaskID != owner.ID || cap.Target.FeatureAreaID != owner.FeatureAreaID {
						t.Fatalf("wrong action owner: %+v", cap)
					}
				}
			}
			if len(got) != len(tc.actions) {
				t.Fatalf("actions=%v want=%v all=%+v status=%s error=%s view=%+v caps=%+v", got, tc.actions, projection.TaskView.Actions, projection.TaskView.ActionsStatus, projection.TaskView.ActionsReadError, projection.TaskView, projection.Capabilities)
			}
			for _, a := range tc.actions {
				if !got[a] {
					t.Fatalf("missing action %s: %v", a, got)
				}
			}
			owner.Status = task.StatusCanceled
			if err := facade.attachTaskView(context.Background(), &projection, owner); err != nil {
				t.Fatal(err)
			}
			if len(projection.TaskView.Actions) != 0 {
				t.Fatalf("terminal task retains workflow actions: %+v", projection.TaskView.Actions)
			}
		})
	}
}

func TestTaskStartRequiresAuthoritativeCapabilityDespiteReadyPhase(t *testing.T) {
	g := wp.Guidance{TaskID: "task", TaskPhase: task.PhaseImplementationReady, CurrentTask: &wp.TaskFact{ID: "task", Status: "ACTIVE"}, NextAction: wp.Action{Code: "WAIT_FOR_RETIREMENT"}}
	projection := p.Projection{}
	applyProgressionPresentation(&projection, g)
	for _, c := range projection.Capabilities {
		if c.Action == "START_EXECUTION" || c.Action == "START_TASK_REVIEW" {
			t.Fatal("phase bypassed blocked Core capability")
		}
	}
}

func TestTaskPhaseRejectsMismatchedObservedAdvancement(t *testing.T) {
	facade := Facade{deps: Dependencies{Runs: approvalRunStub{}}}
	projection := p.Projection{Capabilities: []p.Capability{{Action: "START_EXECUTION", Available: true, Target: &p.ActionTarget{TaskID: "task"}}}}
	if err := facade.attachTaskView(context.Background(), &projection, task.Task{ID: "task", Type: task.TypeDevelopment, Status: task.StatusActive, Phase: task.PhaseReviewing}); err != nil {
		t.Fatal(err)
	}
	if len(projection.TaskView.Actions) != 0 {
		t.Fatal("Reviewing Task exposed implementation despite phase mismatch")
	}
}

func TestInterruptedExecutionDispositionPreservesTaskPhase(t *testing.T) {
	owner := task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Status: task.StatusActive, Phase: task.PhaseVerifying, CurrentExecutionID: "execution", CurrentDefinitionRevisionID: "revision"}
	g := wp.Guidance{ProjectID: owner.ProjectID, TaskID: owner.ID, FeatureAreaID: owner.FeatureAreaID, TaskPhase: owner.Phase, WorkflowMode: "CURRENT", ExecutionID: "execution", TaskDefinitionRevisionID: "revision", DecisionID: "TASK_ACCEPTANCE:execution", CurrentTask: &wp.TaskFact{ID: owner.ID, Status: "ACTIVE", Phase: owner.Phase}, NextAction: wp.Action{Code: "RESOLVE_TASK_ACCEPTANCE"}}
	projection := p.Projection{Details: p.Details{ProjectID: owner.ProjectID, FeatureAreaID: owner.FeatureAreaID, TaskID: owner.ID}}
	applyProgressionPresentation(&projection, g)
	facade := Facade{deps: Dependencies{Tasks: taskActionTasks{}, Runs: approvalRunStub{value: run.Run{ID: "implementation", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", ExecutionProposalID: "execution", Purpose: run.PurposeImplementation, Status: run.StatusSucceeded}}, ExecutionProposals: taskActionExecutions{values: []executionproposal.Proposal{{ID: "execution", TaskID: "task", ProjectID: "project", TaskDefinitionRevisionID: "revision", Outcome: executionproposal.OutcomeFailed, ImplementationRunID: "implementation", WorkSpec: task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeRun}}}}}}
	if err := facade.attachTaskView(context.Background(), &projection, owner); err != nil {
		t.Fatal(err)
	}
	assembleTaskActions(&projection, owner)
	v := projection.TaskView
	if v.Phase != task.PhaseVerifying || v.Status != "ACTIVE" || v.CurrentWorkKind != "EXECUTION" || v.CurrentExecutionAcceptance.Status != "WAITING_DISPOSITION" {
		t.Fatalf("runtime disposition rewrote Task authority: %+v", v)
	}
	got := map[string]bool{}
	for _, c := range v.Actions {
		got[c.Action] = true
	}
	if got["ACCEPT"] || !got["REJECT"] || !got["REQUEST_CHANGES"] || !got["REQUEST_REVERIFICATION"] || len(got) != 3 {
		t.Fatalf("invalid disposition actions: %v", got)
	}
	projection.PendingDecision = nil
	projection.Capabilities = nil
	if err := facade.attachTaskView(context.Background(), &projection, owner); err != nil {
		t.Fatal(err)
	}
	assembleTaskActions(&projection, owner)
	if len(projection.TaskView.Actions) != 0 {
		t.Fatal("child failure independently authorized disposition")
	}
}
