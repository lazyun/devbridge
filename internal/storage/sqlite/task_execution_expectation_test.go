package sqlite

import (
	"testing"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestV77ExecutionExpectationRequiresExactFrozenLayout(t *testing.T) {
	proposal := executionproposal.Proposal{PrimaryMemberID: "member-app", ApprovedWorksetHash: "frozen-workset"}
	exact := wp.TaskExecutionExpectation{Phase: task.PhaseImplementationReady, PrimaryMemberID: "member-app", ApprovedWorksetHash: "frozen-workset"}
	if !matchesExecutionLayout(exact, proposal) {
		t.Fatal("exact v77 expectation rejected")
	}
	for _, changed := range []wp.TaskExecutionExpectation{
		{Phase: task.PhaseImplementationReady},
		{Phase: task.PhaseImplementationReady, PrimaryMemberID: "member-sdk", ApprovedWorksetHash: "frozen-workset"},
		{Phase: task.PhaseImplementationReady, PrimaryMemberID: "member-app", ApprovedWorksetHash: "other"},
	} {
		if matchesExecutionLayout(changed, proposal) {
			t.Fatalf("stale layout accepted: %+v", changed)
		}
	}
	if !matchesExecutionLayout(wp.TaskExecutionExpectation{}, executionproposal.Proposal{}) {
		t.Fatal("legacy empty expectation changed")
	}
}
