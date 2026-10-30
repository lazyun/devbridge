package localmcp_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

// These cases intentionally drive the public MCP adapters over a real Core,
// temporary SQLite database, and managed Git worktree.  They exercise the
// durable Upper Review fences rather than substituting a mock review store.

func upperReviewArgs(t *testing.T, f *integrationFixture, sid, area string) map[string]any {
	t.Helper()
	args := closureTaskReviewArgs(t, f, sid, area)
	args["executorKind"] = "UPPER_LLM"
	delete(args, "model")
	delete(args, "reasoningEffort")
	// A Local MCP Upper Review is a read-only Core operation.  Backend
	// selection is only a Coding Review input; passing the seeded Coding
	// fixture selection across adapters is intentionally rejected by the
	// closed Local command schema.
	delete(args, "backend")
	return args
}

// upperVersion copies the exact Upper authority observed from the access
// target.  It is a fencing value, not a caller-selected version.
func upperVersion(t *testing.T, f *integrationFixture, target map[string]any) map[string]any {
	t.Helper()
	grantEpoch, _ := target["runtimeEpoch"].(string)
	generation, ok := target["grantGeneration"].(float64)
	if grantEpoch == "" || !ok || generation < 1 {
		t.Fatalf("invalid Upper target for authority version: %#v", target)
	}
	return map[string]any{
		"authorityEpoch":  f.plane.DB.RuntimeEpoch(),
		"grantEpoch":      grantEpoch,
		"grantGeneration": int64(generation),
	}
}

func upperSubmit(t *testing.T, target map[string]any, requestID string) map[string]any {
	t.Helper()
	in := make(map[string]any, len(target)+2)
	for key, value := range target {
		in[key] = value
	}
	in["requestId"] = requestID
	in["report"] = review.ReviewReport{
		SchemaVersion:        1,
		Summary:              "Additional review is required.",
		Disposition:          review.DispositionFurtherReviewRequired,
		Coverage:             []string{"primary/main.go"},
		Findings:             []review.Finding{},
		Limitations:          []string{"This negative fixture submits no approval."},
		RecommendedNextSteps: []string{"Perform the required follow-up review."},
		EvidenceRefs:         []review.EvidenceRef{},
	}
	return in
}

func TestUpperReviewNegativeCancelFencesLateReport(t *testing.T) {
	f := newIntegrationFixture(t)
	gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	area := closureArea(t, f, gpt, sid, "Upper cancel fence")
	started := callToolMap(t, f.ctx, gpt, "task_review_start", upperReviewArgs(t, f, sid, area))
	reviewID := started["review"].(map[string]any)["id"].(string)
	access := callToolMap(t, f.ctx, local, "review_access_get", map[string]any{"aiSessionId": sid, "reviewId": reviewID})
	target := access["target"].(map[string]any)
	runID := target["runId"].(string)

	canceled := callToolMap(t, f.ctx, gpt, "operation_cancel", map[string]any{
		"aiSessionId": sid, "ownerType": "REVIEW", "ownerId": reviewID, "runId": runID,
		"requestId": "cancel-upper-before-report", "reason": "User withdrew this review.", "expectedUpperVersion": upperVersion(t, f, target),
	})
	if canceled["state"] != "CANCELED" || canceled["runId"] != runID {
		t.Fatalf("cancel receipt=%+v", canceled)
	}
	if text := callToolErrorText(t, f.ctx, local, "review_result_submit", upperSubmit(t, target, "late-report-after-cancel")); !strings.Contains(text, "REVIEW_TARGET_STALE") {
		t.Fatalf("late report was not fenced as stale: %s", text)
	}
	grant := callToolMap(t, f.ctx, gpt, "review_access_get", map[string]any{"aiSessionId": sid, "reviewId": reviewID})
	if grant["state"] != "CANCELED" {
		t.Fatalf("grant remained usable after cancel: %+v", grant)
	}
	value, err := f.plane.Runs.Get(f.ctx, runID)
	if err != nil || value.Status != run.StatusCanceled {
		t.Fatalf("run=%+v err=%v", value, err)
	}
	owner, err := f.plane.Tasks.Get(f.ctx, target["taskId"].(string))
	if err != nil || owner.Phase != task.PhaseReviewRequired {
		t.Fatalf("task=%+v err=%v", owner, err)
	}
	var reports, claims int
	if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM review_reports WHERE review_id=?`, reviewID).Scan(&reports); err != nil {
		t.Fatal(err)
	}
	if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM feature_area_agent_claims WHERE run_id=?`, runID).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if reports != 0 || claims != 0 {
		t.Fatalf("late report changed terminal history: reports=%d claims=%d", reports, claims)
	}
}

func TestUpperReviewNegativeCompletedReportCannotBeCanceled(t *testing.T) {
	f := newIntegrationFixture(t)
	gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	area := closureArea(t, f, gpt, sid, "Upper completed history")
	started := callToolMap(t, f.ctx, gpt, "task_review_start", upperReviewArgs(t, f, sid, area))
	reviewID := started["review"].(map[string]any)["id"].(string)
	access := callToolMap(t, f.ctx, local, "review_access_get", map[string]any{"aiSessionId": sid, "reviewId": reviewID})
	target := access["target"].(map[string]any)
	runID := target["runId"].(string)
	completed := callToolMap(t, f.ctx, local, "review_result_submit", upperSubmit(t, target, "complete-before-cancel"))
	if completed["status"] != "COMPLETED" {
		t.Fatalf("completion=%+v", completed)
	}
	before, err := f.plane.Runs.Get(f.ctx, runID)
	if err != nil || before.Status != run.StatusSucceeded {
		t.Fatalf("completed run=%+v err=%v", before, err)
	}
	cancel := callToolMap(t, f.ctx, gpt, "operation_cancel", map[string]any{
		"aiSessionId": sid, "ownerType": "REVIEW", "ownerId": reviewID, "runId": runID,
		"requestId": "cancel-after-report", "reason": "Too late.", "expectedUpperVersion": upperVersion(t, f, target),
	})
	if cancel["state"] != "ALREADY_TERMINAL" || cancel["runStatus"] != "SUCCEEDED" {
		t.Fatalf("completed review cancellation was destructive: %+v", cancel)
	}
	after, err := f.plane.Runs.Get(f.ctx, runID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("cancel changed completed run: before=%+v after=%+v err=%v", before, after, err)
	}
	stored := callToolMap(t, f.ctx, gpt, "review_result_get", map[string]any{"aiSessionId": sid, "reviewId": reviewID})
	if stored["report"].(map[string]any)["report"].(map[string]any)["summary"] != "Additional review is required." {
		t.Fatalf("cancel changed formal report: %+v", stored)
	}
	var reports int
	if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM review_reports WHERE review_id=?`, reviewID).Scan(&reports); err != nil {
		t.Fatal(err)
	}
	if reports != 1 {
		t.Fatalf("completed history mutated: reports=%d", reports)
	}
}

func TestUpperReviewNegativeBusyAreaAndWorkflowCompleteClaim(t *testing.T) {
	f := newIntegrationFixture(t)
	gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	areaA := closureArea(t, f, gpt, sid, "Upper busy area")
	areaB := closureArea(t, f, gpt, sid, "Upper independent area")
	firstArgs := upperReviewArgs(t, f, sid, areaA)
	first := callToolMap(t, f.ctx, gpt, "task_review_start", firstArgs)
	firstID := first["review"].(map[string]any)["id"].(string)
	busyArgs := upperReviewArgs(t, f, sid, areaA)
	busyArgs["requestId"] = "upper-busy-same-area"
	if text := callToolErrorText(t, f.ctx, local, "task_review_start", busyArgs); !strings.Contains(text, "FEATURE_AREA_BUSY") || !strings.Contains(text, firstID) {
		t.Fatalf("same-area Upper Review was not rejected as busy: %s", text)
	}
	second := callToolMap(t, f.ctx, local, "task_review_start", upperReviewArgs(t, f, sid, areaB))
	if second["review"].(map[string]any)["id"] == firstID {
		t.Fatalf("different FeatureArea reused Upper Review: %+v", second)
	}

	access := callToolMap(t, f.ctx, gpt, "review_access_get", map[string]any{"aiSessionId": sid, "reviewId": firstID})
	target := access["target"].(map[string]any)
	read := callToolMap(t, f.ctx, local, "file_read", map[string]any{"reviewTarget": target, "path": "primary/main.go", "workflowComplete": true})
	if read["content"] != "package primary\n" || read["workflowComplete"] != true {
		t.Fatalf("workflow completion read=%+v", read)
	}
	after := callToolMap(t, f.ctx, gpt, "review_access_get", map[string]any{"aiSessionId": sid, "reviewId": firstID})
	if after["state"] != "AWAITING_REPORT" {
		t.Fatalf("workflowComplete did not await report: %+v", after)
	}
	var claims int
	if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM feature_area_agent_claims WHERE run_id=?`, target["runId"].(string)).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	secondID := second["review"].(map[string]any)["id"].(string)
	secondAccess := callToolMap(t, f.ctx, gpt, "review_access_get", map[string]any{"aiSessionId": sid, "reviewId": secondID})
	secondTarget := secondAccess["target"].(map[string]any)
	foreignRead := callToolMap(t, f.ctx, gpt, "file_read", map[string]any{"reviewTarget": secondTarget, "path": "primary/main.go"})
	in := upperSubmit(t, target, "cross-review-evidence")
	report := in["report"].(review.ReviewReport)
	report.EvidenceRefs = []review.EvidenceRef{{EvidenceID: foreignRead["evidenceId"].(string), Path: "primary/main.go", Digest: foreignRead["digest"].(string)}}
	in["report"] = report
	if text := callToolErrorText(t, f.ctx, local, "review_result_submit", in); !strings.Contains(text, "REVIEW_EVIDENCE_MISMATCH") {
		t.Fatalf("cross Review evidence accepted: %s", text)
	}
	mixed := map[string]any{}
	for k, v := range secondTarget {
		mixed[k] = v
	}
	mixed["featureAreaId"] = areaA
	if text := callToolErrorText(t, f.ctx, gpt, "file_read", map[string]any{"reviewTarget": mixed, "path": "primary/main.go"}); !strings.Contains(text, "REVIEW_TARGET_STALE") {
		t.Fatalf("cross area target=%s", text)
	}
	if claims != 1 {
		t.Fatalf("workflowComplete released active Upper Review claim: claims=%d", claims)
	}
}
