package localmcp_test

import (
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workflowtest"
	"testing"
)

func (f *integrationFixture) pendingReviewGate(t *testing.T, sid string) (task.Task, review.Review) {
	t.Helper()
	area := closureArea(t, f, connectGPT(t, f.gptURL), sid, "Improve onboarding")
	// This fixture supplies a canonical Task for the Review-gate state machine.
	// The removed ChatGPT task_create RPC cannot provide caller scope to this
	// direct helper, so create through the current shared Task fixture instead.
	current := f.seedTask(t, map[string]any{"aiSessionId": sid, "featureAreaId": area, "goal": "Make first-run setup clear."})
	work := task.WorkSpec{Goal: "Make first-run setup clear.", Constraints: []string{"Preserve existing behavior"}, CodexInstruction: "Update tracked.txt with concise first-run guidance.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopePaths, Paths: []string{"tracked.txt"}}}
	verify := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "tracked.txt", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "test -f tracked.txt", Required: true}}, AcceptanceCriteria: []string{"Onboarding is clear"}, CodexInstruction: "Verify read-only"}
	source := workflowtest.ReviewGate(t, f.plane.DB, current, sid, work, verify)
	current, err := f.plane.Tasks.Get(f.ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	return current, source
}

func (f *integrationFixture) admitScalarExecution(t *testing.T, sid string) (task.Task, executionproposal.Proposal) {
	t.Helper()
	current, source := f.pendingReviewGate(t, sid)
	runtimeConfig := source.RuntimeConfig
	out, err := f.plane.WorkflowProgression.Resolve(f.ctx, wp.ResolveInput{AISessionID: sid, TaskID: current.ID, DecisionID: wp.DecisionID(source.ID), RequestID: "approve-" + current.ID, Option: "APPROVE", BackendSelection: source.BackendSelection, NextRunRuntimeConfig: &runtimeConfig})
	if err != nil {
		t.Fatal(err)
	}
	proposal, reserved, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).ReserveTaskExecution(f.ctx, sid, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Status != executionproposal.StatusAdmitted || reserved.ID != "" || reserved.RuntimeEpoch != "" {
		t.Fatalf("admission created a Run: proposal=%+v run=%+v", proposal, reserved)
	}
	assertScalarLayer(t, f, proposal.ID, 0, 0)
	return *out.Task, proposal
}
