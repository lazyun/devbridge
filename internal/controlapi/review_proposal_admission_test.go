package controlapi_test

import (
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
	"strings"
	"testing"
)

func TestUnexecutableReviewPublicationCanBeCorrectedOnSameReviewBeforeApproval(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	targets := workset.CloneTargets(f.targets)
	refs := []review.EvidenceRef{}
	for i, target := range targets {
		got, e := f.facade.UpperReviewRead(f.ctx, f.inputFor(target, "same.txt"))
		if e != nil {
			t.Fatal(e)
		}
		refs = append(refs, review.EvidenceRef{EvidenceID: got.EvidenceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, Path: got.Path, Digest: got.Digest})
		targets[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Update bounded contracts", Constraints: []string{"preserve scope"}, CodexInstruction: "Update same.txt", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "frozen targets", CodexInstruction: "Verify contracts", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Required: true, Instruction: "run tests", RepositoryID: targets[0].RepositoryID, WorkingDirectory: ".", RepositoryIDs: []string{targets[0].RepositoryID}}}, AcceptanceCriteria: []string{"contracts pass"}}
	verification.Checks = append(verification.Checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Required: true, Command: "test -f same.txt", RepositoryID: targets[1].RepositoryID, WorkingDirectory: ".", RepositoryIDs: []string{targets[1].RepositoryID}})
	in := review.SubmitInput{UpperTarget: f.access.Target, RequestID: "proposal-preflight", Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, Summary: "Bounded proposal", Disposition: review.DispositionApprovalReady, Coverage: []string{"both targets"}, Findings: []review.Finding{}, Limitations: []string{"source only"}, RecommendedNextSteps: []string{"approve"}, EvidenceRefs: refs, WorkSpec: &work, VerificationSpec: &verification}}
	tables := []string{"tasks", "reviews", "runs", "review_reports", "review_result_receipts", "review_grants", "confirmation_intents"}
	before := definitionFormalSnapshot(t, f, tables)
	if _, err := f.facade.SubmitReviewResult(f.ctx, in); err == nil || !strings.Contains(err.Error(), "UNOBSERVABLE_REQUIRED_CHECK") {
		t.Fatalf("invalid proposal published: %v", err)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "rejected report admission")
	verification.Checks[0].Instruction = ""
	verification.Checks[0].Command = "test -f same.txt"
	receipt, err := f.facade.SubmitReviewResult(f.ctx, in)
	if err != nil || receipt.ReviewID != f.review || receipt.RunID != f.run {
		t.Fatalf("same Review cannot be corrected before publication: %+v %v", receipt, err)
	}
}
