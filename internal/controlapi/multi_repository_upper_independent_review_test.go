package controlapi_test

import (
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

// This reviewer-owned test exercises the production facade and durable stores.
// Two repositories intentionally expose the same relative path; evidence from
// one immutable member must never satisfy a report reference for the other.
func TestMultiRepositoryUpperReportRejectsCrossMemberEvidenceSubstitution(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	reads := make([]review.EvidenceRef, 0, len(f.targets))
	implementation := workset.CloneTargets(f.targets)
	checks := make([]task.VerificationCheck, 0, len(f.targets))
	for i, target := range f.targets {
		got, err := f.facade.UpperReviewRead(f.ctx, f.inputFor(target, "same.txt"))
		if err != nil {
			t.Fatal(err)
		}
		reads = append(reads, review.EvidenceRef{EvidenceID: got.EvidenceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, Path: "same.txt", Digest: got.Digest})
		implementation[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
		checks = append(checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: target.RepositoryID, RepositoryIDs: []string{target.RepositoryID}, WorkingDirectory: "."})
	}
	reads[0].MemberID = f.targets[1].MemberID
	work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Update both exact members", Constraints: []string{"preserve identity"}, CodexInstruction: "Update same.txt in both repositories", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "both repositories", Checks: checks, AcceptanceCriteria: []string{"both checks pass"}, CodexInstruction: "Run both checks"}
	in := review.SubmitInput{UpperTarget: f.access.Target, RequestID: "cross-member-substitution", Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: implementation, PrimaryMemberID: implementation[0].MemberID, Summary: "Attempt a cross-member evidence substitution", Disposition: review.DispositionApprovalReady, Coverage: []string{"both repositories"}, Findings: []review.Finding{}, Limitations: []string{"source review only"}, RecommendedNextSteps: []string{"approve only after exact evidence"}, EvidenceRefs: reads, WorkSpec: &work, VerificationSpec: &verification}}
	if _, err := f.facade.SubmitReviewResult(f.ctx, in); err == nil {
		t.Fatal("cross-member evidence substitution was accepted")
	}
	var reports int
	if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM review_reports WHERE review_id=?`, f.review).Scan(&reports); err != nil || reports != 0 {
		t.Fatalf("rejected report persisted: count=%d err=%v", reports, err)
	}
}

func TestMultiRepositoryUpperBlocksGenericRepositoryReadDuringGrant(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	for _, repositoryID := range []string{f.targets[0].RepositoryID, f.repos["backend"]} {
		if _, err := f.facade.ReadRepositoryFileForSession(f.ctx, f.session, repositoryID, repository.FileReadInput{Path: "same.txt"}); err == nil {
			t.Fatalf("generic repository read bypassed active v2 grant for %s", repositoryID)
		}
	}
}

func TestMultiRepositoryUpperApprovalRejectsChangedFormalProposal(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	implementation, work, verification := submitIndependentApprovalReadyReport(t, f)

	changed := work
	changed.Goal = "A different goal that was not in the formal Review result"
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "changed-proposal", Purpose: c.Execution, Action: "APPROVE", Execution: exactReviewApprovalBody(t, f.ctx, f.facade, f.task, f.area, f.review, "APPROVE"), Variants: []c.Variant{{ID: "changed", Summary: "changed after formal result", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: implementation, PrimaryMemberID: implementation[0].MemberID, WorkSpec: &changed, VerificationSpec: &verification}}})
	if err != nil {
		t.Fatalf("prepare explicit changed proposal: %v", err)
	}
	_, err = f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "commit-changed-proposal", ContentHash: prepared.ContentHash, VariantID: "changed"})
	if err == nil {
		t.Fatal("approval accepted specs that differed from the formal Review result")
	}
	var revisions int
	if queryErr := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_definition_revisions WHERE task_id=?`, f.task).Scan(&revisions); queryErr != nil || revisions != 1 {
		t.Fatalf("rejected approval changed the sole authored draft: count=%d err=%v", revisions, queryErr)
	}
}

func TestMultiRepositoryUpperPrepareRejectsBaselineFromDifferentRepositoryWithoutAdmission(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	before := map[string]int{}
	for _, table := range []string{"reviews", "runs", "review_grants", "workspaces", "worktrees", "run_repository_bindings"} {
		var count int
		if err := f.plane.DB.SQL().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		before[table] = count
	}
	invalidTargets := workset.CloneTargets(f.targets)
	invalidTargets[0].OriginCommit = f.heads["backend"]
	invalidTargets[0].MemberID, invalidTargets[0].MemberVersion = "", 0
	_, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "foreign-baseline", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, RepositoryTargets: invalidTargets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "invalid baseline", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "must not be admitted"}}})
	if err == nil {
		t.Fatal("target accepted a commit that does not exist in its repository")
	}
	for table, want := range before {
		var got int
		if queryErr := f.plane.DB.SQL().QueryRow("SELECT count(*) FROM " + table).Scan(&got); queryErr != nil || got != want {
			t.Fatalf("prepare changed %s: got=%d want=%d err=%v", table, got, want, queryErr)
		}
	}
}

func submitIndependentApprovalReadyReport(t *testing.T, f *upperLifecycleFixture) ([]workset.Target, task.WorkSpec, task.VerificationSpec) {
	t.Helper()
	implementation := workset.CloneTargets(f.targets)
	refs := make([]review.EvidenceRef, 0, len(f.targets))
	checks := make([]task.VerificationCheck, 0, len(f.targets))
	for i, target := range f.targets {
		got, err := f.facade.UpperReviewRead(f.ctx, f.inputFor(target, "same.txt"))
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, review.EvidenceRef{EvidenceID: got.EvidenceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, Path: "same.txt", Digest: got.Digest})
		implementation[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
		checks = append(checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: target.RepositoryID, RepositoryIDs: []string{target.RepositoryID}, WorkingDirectory: "."})
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Update both exact members", Constraints: []string{"preserve identity"}, CodexInstruction: "Update same.txt in both repositories", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "both repositories", Checks: checks, AcceptanceCriteria: []string{"both checks pass"}, CodexInstruction: "Run both checks"}
	report := review.SubmitInput{UpperTarget: f.access.Target, RequestID: "independent-formal-result", Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: implementation, PrimaryMemberID: implementation[0].MemberID, Summary: "Formal exact-member proposal", Disposition: review.DispositionApprovalReady, Coverage: []string{"both repositories"}, Findings: []review.Finding{}, Limitations: []string{"source review only"}, RecommendedNextSteps: []string{"approve without starting"}, EvidenceRefs: refs, WorkSpec: &work, VerificationSpec: &verification}}
	if _, err := f.facade.SubmitReviewResult(f.ctx, report); err != nil {
		t.Fatal(err)
	}
	return implementation, work, verification
}
