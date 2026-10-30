package sqlite

import (
	"context"
	"testing"

	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestContinuationRejectsUnconfirmedRepositoryExpansionAndReplay(t *testing.T) {
	f, _, first := acceptedExecutionFixture(t, "-target-injection")
	first = terminalExecutionWithReport(t, f, first)
	work := task.CloneWorkSpec(first.WorkSpec)
	verification := task.CloneVerificationSpec(first.VerificationSpec)
	in := wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(first.ID), ExecutionID: first.ID, Option: wp.AcceptanceRequestChanges, RequestID: "target-injection", NextWorkSpec: &work, NextVerificationSpec: &verification}
	target := workset.Target{RepositoryID: "unapproved-repository", MemberID: "unapproved-member"}
	in.NextRepositoryTargets = []workset.Target{target}
	in.NextPrimaryMemberID = target.MemberID
	if _, err := f.store.ResolveTaskAcceptance(context.Background(), in); err == nil {
		t.Fatal("unconfirmed repository expansion accepted")
	}
	in.NextRepositoryTargets = nil
	in.NextPrimaryMemberID = ""
	out, err := f.store.ResolveTaskAcceptance(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.NextRepositoryTargets = []workset.Target{target}
	in.NextPrimaryMemberID = target.MemberID
	if _, err = f.store.ResolveTaskAcceptance(context.Background(), in); err == nil {
		t.Fatal("receipt replay ignored injected target set")
	}
	in.NextRepositoryTargets = nil
	in.NextPrimaryMemberID = ""
	replay, err := f.store.ResolveTaskAcceptance(context.Background(), in)
	if err != nil || !replay.Replay || replay.SuccessorExecutionID != out.SuccessorExecutionID {
		t.Fatalf("exact replay=%+v %v", replay, err)
	}
}
