package controlapi_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

// Actual Local MCP/Core/SQLite/Git. Reports are explicitly authored by the test;
// no native/model runtime is invoked or qualified by this readback regression.
func TestReviewResultEnvelopeRetainsOriginalReviewAfterRevise(t *testing.T) {
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	implementation := workset.CloneTargets(f.targets)
	refs := make([]review.EvidenceRef, 0, len(f.targets))
	checks := make([]task.VerificationCheck, 0, len(f.targets))
	for i, target := range f.targets {
		got, err := f.facade.UpperReviewRead(f.ctx, f.inputFor(target, "same.txt"))
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, review.EvidenceRef{EvidenceID: got.EvidenceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, Path: got.Path, Digest: got.Digest})
		implementation[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
		checks = append(checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: target.RepositoryID, WorkingDirectory: ".", RepositoryIDs: []string{target.RepositoryID}})
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Update both contracts", Constraints: []string{"preserve compatibility"}, CodexInstruction: "Change both exact files", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "Both repositories", Checks: checks, AcceptanceCriteria: []string{"Both repositories pass"}, CodexInstruction: "Run both checks"}
	submission := review.SubmitInput{UpperTarget: f.access.Target, RequestID: "report-before-revise", Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: implementation, PrimaryMemberID: implementation[0].MemberID, Summary: "Test-authored exact proposal", Disposition: review.DispositionApprovalReady, Coverage: []string{"app/same.txt", "sdk/same.txt"}, Findings: []review.Finding{}, Limitations: []string{"test-authored report, no model"}, RecommendedNextSteps: []string{"resolve the review decision"}, EvidenceRefs: refs, WorkSpec: &work, VerificationSpec: &verification}}
	client := newApprovedStartLocalClient(t, f)
	type envelope struct {
		Result          map[string]any `json:"result"`
		Context         map[string]any `json:"context"`
		Workflow        map[string]any `json:"workflow"`
		RefreshRequired bool           `json:"refreshRequired"`
	}
	read := func(name string, wire map[string]any) envelope {
		t.Helper()
		var out envelope
		if err := json.Unmarshal(client.call(name, wire, false), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	wire := mcpObject(t, submission)
	first := read("review_result_submit", wire)
	get := map[string]any{"aiSessionId": f.session, "reviewId": f.review, "requestId": submission.RequestID}
	firstGet := read("review_result_get", get)
	fullGet := map[string]any{"aiSessionId": f.session, "reviewId": f.review}
	firstFull := read("review_result_get", fullGet)
	if first.Result["reviewId"] != f.review || first.Result["runId"] != f.run || first.Context["reviewId"] != f.review || first.Context["runId"] != f.run || first.RefreshRequired {
		t.Fatalf("initial report exact-current control failed: %+v", first)
	}
	_, commit := prepareLifecycleReviewDecision(t, f, "REVISE", "revise-result-envelope")
	if completed, err := f.facade.CommitConfirmation(f.ctx, commit); err != nil || completed.State != "COMPLETED" {
		t.Fatalf("REVISE: %+v %v", completed, err)
	}
	current, err := f.plane.Tasks.Get(f.ctx, f.task)
	if err != nil || current.CurrentReviewID == f.review || current.CurrentReviewID == "" {
		t.Fatalf("no successor Review: %+v %v", current, err)
	}
	// Explicit preparation serializes with the admission continuation. Snapshot
	// only after that sanctioned successor preparation has reached a stable state.
	successorAccess, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, current.CurrentReviewID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.PrepareUpperReview(f.ctx, successorAccess.Target); err != nil {
		t.Fatal(err)
	}
	tables := []string{"tasks", "reviews", "runs", "review_reports", "review_result_receipts", "review_approval_decisions", "review_grants", "upper_review_worker_claims"}
	for _, cold := range []bool{false, true} {
		if cold {
			client.close()
			reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
			client = newApprovedStartLocalClient(t, f)
		}
		before := definitionFormalSnapshot(t, f, tables)
		for _, tc := range []struct {
			name, tool string
			args       map[string]any
			original   envelope
		}{
			{"submit-replay", "review_result_submit", wire, first},
			{"receipt-read", "review_result_get", get, firstGet},
			{"report-read", "review_result_get", fullGet, firstFull},
		} {
			out := read(tc.tool, tc.args)
			if !reflect.DeepEqual(out.Result, tc.original.Result) {
				t.Errorf("cold=%v %s immutable result changed", cold, tc.name)
			}
			if out.Context["reviewId"] != f.review || out.Context["runId"] != f.run || out.Context["executionId"] != nil || out.Context["decisionId"] != nil {
				t.Errorf("cold=%v %s borrowed new Review identity: old=%s new=%s context=%+v", cold, tc.name, f.review, current.CurrentReviewID, out.Context)
			}
			if !out.RefreshRequired || out.Workflow["stale"] != true || out.Workflow["nextAction"] != nil || out.Workflow["pendingDecisions"] != nil {
				t.Errorf("cold=%v %s borrowed fresh successor actions: %+v", cold, tc.name, out.Workflow)
			}
		}
		changed := submission
		changed.Report.Summary = "Different payload cannot replace original report"
		client.call("review_result_submit", mcpObject(t, changed), true)
		assertDefinitionFormalSnapshot(t, f, tables, before, "old Review receipt/read and changed-payload refusal")
	}
}
