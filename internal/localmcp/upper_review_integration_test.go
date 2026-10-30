package localmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real Core, SQLite, managed Git worktree and both HTTP MCP adapters; fake
// backend is never invoked. All files and service sockets belong to t.TempDir.
func TestUpperReviewCrossAdapterClosure(t *testing.T) {
	f := newIntegrationFixture(t)
	defer func() {
		if t.Failed() {
			var reason string
			_ = f.plane.DB.SQL().QueryRow("SELECT COALESCE(preparation_error,'') FROM review_grants LIMIT 1").Scan(&reason)
			t.Logf("preparation failure: %s", reason)
		}
	}()
	backend := &closureFactory{entered: make(chan *closureSession, 4), message: reviewApprovalProposal(t)}
	f.plane.Coordinator.Codex = backend
	gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	area := closureArea(t, f, gpt, sid, "Upper review")
	// The frozen Review snapshot includes the whole authorized worktree.  Seed
	// forbidden fixture paths before admission so later scope checks exercise
	// authorization, rather than intentionally triggering snapshot-drift.
	if err := os.MkdirAll(filepath.Join(f.repoPath, "primary/private"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repoPath, "primary/private/secret.go"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repoPath, "primary/hidden_test.go"), []byte("test secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("private", filepath.Join(f.repoPath, "primary/indirect")); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, f.repoPath, "add", "primary/private/secret.go", "primary/hidden_test.go", "primary/indirect")
	fixtureGit(t, f.repoPath, "-c", "user.name=Dev Bridge Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "seed forbidden review fixture paths")
	args := closureTaskReviewArgs(t, f, sid, area)
	args["executorKind"] = "UPPER_LLM"
	args["forbiddenPaths"] = []string{"primary/private", "primary/hidden_test.go"}
	delete(args, "model")
	delete(args, "reasoningEffort")
	delete(args, "backend")
	invalid := map[string]any{}
	for k, v := range args {
		invalid[k] = v
	}
	invalid["model"] = "gpt-5.6-terra"
	// GPT starts Reviews through confirmation and deliberately does not expose
	// this retired direct-command shape.  Check the closed Local schema, which
	// owns the direct Upper Review admission contract.
	if text := callToolErrorText(t, f.ctx, local, "task_review_start", invalid); text == "" {
		t.Fatal("mixed executor branch accepted")
	}
	// The Local command owns the direct Upper Review input, including the
	// immutable forbidden-path scope.  The remaining reads/results below cross
	// both adapters against that single Core Review.
	first := callToolMap(t, f.ctx, local, "task_review_start", args)
	r := first["review"].(map[string]any)
	id := r["id"].(string)
	access := callToolMap(t, f.ctx, local, "review_access_get", map[string]any{"aiSessionId": sid, "reviewId": id})
	if access["state"] != "ACTIVE" {
		t.Fatalf("access=%v", access)
	}
	target := access["target"].(map[string]any)
	if text := callToolErrorText(t, f.ctx, gpt, "repository_search", map[string]any{"aiSessionId": sid, "repositoryId": f.repositoryID, "query": "package", "paths": []string{"primary"}}); !strings.Contains(text, "REVIEW_SCOPE_FORBIDDEN") {
		t.Fatalf("unscoped search bypass=%s", text)
	}
	var root string
	if err := f.plane.DB.SQL().QueryRow("SELECT path FROM worktrees WHERE id=?", access["worktreeId"]).Scan(&root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"primary/hidden_test.go", "primary/private/secret.go", "primary/indirect/secret.go", "../outside", ".git/config", ".git"} {
		if text := callToolErrorText(t, f.ctx, gpt, "file_read", map[string]any{"reviewTarget": target, "path": path}); text == "" {
			t.Fatalf("read accepted forbidden path %s", path)
		}
	}
	for _, path := range []string{"primary/private", "primary/hidden_test.go"} {
		if text := callToolErrorText(t, f.ctx, gpt, "file_list", map[string]any{"reviewTarget": target, "path": path}); text == "" {
			t.Fatalf("list accepted forbidden path %s", path)
		}
	}
	listing := callToolMap(t, f.ctx, gpt, "file_list", map[string]any{"reviewTarget": target, "path": "primary"})
	listed, _ := json.Marshal(listing)
	if strings.Contains(string(listed), "hidden_test") || strings.Contains(string(listed), "secret.go") || strings.Contains(string(listed), "private") {
		t.Fatalf("forbidden listing leak: %s", listed)
	}
	if text := callToolErrorText(t, f.ctx, gpt, "file_write", map[string]any{"reviewTarget": target, "path": "primary/main.go", "content": "bad"}); text == "" {
		t.Fatal("Review write accepted")
	}
	read := callToolMap(t, f.ctx, gpt, "file_read", map[string]any{"reviewTarget": target, "path": "primary/main.go"})
	if read["content"] != "package primary\n" || read["evidenceId"] == "" || read["digest"] == "" {
		t.Fatalf("read=%v", read)
	}
	var proposal review.ImplementationResult
	if err := json.Unmarshal([]byte(reviewApprovalProposal(t)), &proposal); err != nil {
		t.Fatal(err)
	}
	report := review.ReviewReport{SchemaVersion: 1, Summary: "Scoped source checked", Disposition: review.DispositionApprovalReady, Coverage: []string{"primary/main.go"}, Findings: []review.Finding{}, Limitations: []string{"Only scoped fixture source was read"}, RecommendedNextSteps: []string{"Request user approval"}, EvidenceRefs: []review.EvidenceRef{{EvidenceID: read["evidenceId"].(string), Path: "primary/main.go", Digest: read["digest"].(string), StartLine: 1, EndLine: 1}}, WorkSpec: &proposal.WorkSpec, VerificationSpec: &proposal.VerificationSpec}
	submit := map[string]any{}
	for k, v := range target {
		submit[k] = v
	}
	submit["requestId"] = "upper-report"
	submit["report"] = report
	// Forged content identity is rejected before any finalization mutation.
	forged := report
	forged.EvidenceRefs = []review.EvidenceRef{{EvidenceID: "forged", Path: "primary/main.go", Digest: read["digest"].(string)}}
	submit["report"] = forged
	if text := callToolErrorText(t, f.ctx, local, "review_result_submit", submit); !strings.Contains(text, "REVIEW_EVIDENCE_MISMATCH") {
		t.Fatalf("forged result=%s", text)
	}
	submit["report"] = report
	if err := os.WriteFile(filepath.Join(root, "primary/main.go"), []byte("package changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if text := callToolErrorText(t, f.ctx, local, "review_result_submit", submit); !strings.Contains(text, "REVIEW_EVIDENCE_MISMATCH") {
		t.Fatalf("snapshot drift=%s", text)
	}
	if err := os.WriteFile(filepath.Join(root, "primary/main.go"), []byte("package primary\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldBaseline := access["baseline"]
	if err := os.WriteFile(filepath.Join(f.repoPath, "tracked.txt"), []byte("development moved\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, f.repoPath, "add", "tracked.txt")
	fixtureGit(t, f.repoPath, "-c", "user.name=Dev Bridge Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "advance isolated development ref")
	receipt := callToolMap(t, f.ctx, local, "review_result_submit", submit)
	if receipt["status"] != "COMPLETED" {
		t.Fatalf("receipt=%v", receipt)
	}
	replay := callToolMap(t, f.ctx, gpt, "review_result_submit", submit)
	if replay["reportId"] != receipt["reportId"] {
		t.Fatal("response replay created report")
	}
	repeatedStart := callToolMap(t, f.ctx, local, "task_review_start", args)
	if repeatedStart["review"].(map[string]any)["id"] != id {
		t.Fatal("terminal start replay changed Review")
	}
	full := callToolMap(t, f.ctx, gpt, "review_result_get", map[string]any{"aiSessionId": sid, "reviewId": id})
	if full["report"].(map[string]any)["baseline"] != oldBaseline || full["report"].(map[string]any)["startSnapshot"] != access["startSnapshot"] {
		t.Fatal("report baseline/snapshot changed with development ref")
	}
	if full["report"].(map[string]any)["report"].(map[string]any)["summary"] != report.Summary {
		t.Fatalf("formal report=%v", full)
	}
	owner, err := f.plane.Tasks.Get(f.ctx, args["taskId"].(string))
	if err != nil || owner.Phase != task.PhaseReviewApprovalPending {
		t.Fatalf("task=%+v err=%v", owner, err)
	}
	if text := callToolErrorText(t, f.ctx, gpt, "file_read", map[string]any{"reviewTarget": target, "path": "primary/main.go"}); text == "" {
		t.Fatal("closed grant readable")
	}
	var claims, conversations, executions, reports int
	for table, out := range map[string]*int{"feature_area_agent_claims": &claims, "agent_conversations": &conversations, "execution_proposals": &executions, "review_reports": &reports} {
		if err := f.plane.DB.SQL().QueryRow("SELECT count(*) FROM " + table).Scan(out); err != nil {
			t.Fatal(err)
		}
	}
	if claims != 0 || conversations != 0 || executions != 0 || reports != 1 {
		t.Fatalf("claims=%d conversations=%d executions=%d reports=%d", claims, conversations, executions, reports)
	}
	select {
	case <-backend.entered:
		t.Fatal("Upper Review invoked Codex")
	default:
	}
}

func TestUpperReviewRestartExplicitRecovery(t *testing.T) {
	f := newIntegrationFixture(t)
	gpt := connectGPT(t, f.gptURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	area := closureArea(t, f, gpt, sid, "Recover Upper review")
	args := closureTaskReviewArgs(t, f, sid, area)
	args["executorKind"] = "UPPER_LLM"
	delete(args, "model")
	delete(args, "reasoningEffort")
	started := callToolMap(t, f.ctx, gpt, "task_review_start", args)
	id := started["review"].(map[string]any)["id"].(string)
	access := callToolMap(t, f.ctx, gpt, "review_access_get", map[string]any{"aiSessionId": sid, "reviewId": id})
	old := access["target"].(map[string]any)
	callToolMap(t, f.ctx, gpt, "file_read", map[string]any{"reviewTarget": old, "path": "primary/main.go"})
	root := f.plane.DataPaths.Root
	if err := f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := controlplane.Open(f.ctx, controlplane.Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	facade, err := controlapi.NewFromControlPlane(reopened)
	if err != nil {
		t.Fatal(err)
	}
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test-restarted", Ready: true}, reopened.AIContext, facade)
	if err != nil {
		t.Fatal(err)
	}
	_, _, url := startGPTTestServer(t, service)
	fresh := connectGPT(t, url)
	after := callToolMap(t, f.ctx, fresh, "review_access_get", map[string]any{"aiSessionId": sid, "reviewId": id})
	if after["state"] != "RECOVERY_REQUIRED" {
		t.Fatalf("restart=%v", after)
	}
	if text := callToolErrorText(t, f.ctx, fresh, "file_read", map[string]any{"reviewTarget": old, "path": "primary/main.go"}); text == "" {
		t.Fatal("old epoch read accepted")
	}
	recoverArgs := map[string]any{"aiSessionId": sid, "featureAreaId": area, "operationKind": "REVIEW", "operationId": id, "runId": old["runId"], "requestId": "recover-upper", "expectedUpperVersion": map[string]any{"authorityEpoch": reopened.DB.RuntimeEpoch(), "grantEpoch": old["runtimeEpoch"], "grantGeneration": old["grantGeneration"]}}
	recovered := callToolMap(t, f.ctx, fresh, "agent_operation_recover", recoverArgs)
	if recovered["state"] != "REAUTHORIZED" {
		t.Fatalf("recover=%v", recovered)
	}
	replay := callToolMap(t, f.ctx, fresh, "agent_operation_recover", recoverArgs)
	if replay["state"] != recovered["state"] || replay["backendDisposition"] != recovered["backendDisposition"] {
		t.Fatalf("recovery replay=%v", replay)
	}
	authorized := recovered["authorizedUpperVersion"].(map[string]any)
	resumeTarget := map[string]any{}
	for key, value := range old {
		resumeTarget[key] = value
	}
	resumeTarget["runtimeEpoch"] = authorized["grantEpoch"]
	resumeTarget["grantGeneration"] = authorized["grantGeneration"]
	callToolMap(t, f.ctx, fresh, "review_access_prepare", resumeTarget)
	active := callToolMap(t, f.ctx, fresh, "review_access_get", map[string]any{"aiSessionId": sid, "reviewId": id})
	target := active["target"].(map[string]any)
	if target["runId"] != old["runId"] || target["runtimeEpoch"] == old["runtimeEpoch"] || target["grantGeneration"].(float64) != old["grantGeneration"].(float64)+1 {
		t.Fatalf("target old=%v new=%v", old, target)
	}
	callToolMap(t, f.ctx, fresh, "file_read", map[string]any{"reviewTarget": target, "path": "primary/main.go"})
	report := review.ReviewReport{SchemaVersion: 1, Summary: "Needs additional review", Disposition: review.DispositionFurtherReviewRequired, Coverage: []string{"primary/main.go"}, Findings: []review.Finding{}, Limitations: []string{"Re-review after recovered access"}, RecommendedNextSteps: []string{}, EvidenceRefs: []review.EvidenceRef{}}
	in := map[string]any{}
	for k, v := range target {
		in[k] = v
	}
	in["requestId"] = "resumed-report"
	in["report"] = report
	receipt := callToolMap(t, f.ctx, fresh, "review_result_submit", in)
	if receipt["status"] != "COMPLETED" {
		t.Fatalf("result=%v", receipt)
	}
	var claims, conversations int
	_ = reopened.DB.SQL().QueryRow("SELECT count(*) FROM feature_area_agent_claims").Scan(&claims)
	_ = reopened.DB.SQL().QueryRow("SELECT count(*) FROM agent_conversations").Scan(&conversations)
	if claims != 0 || conversations != 0 {
		t.Fatalf("claims=%d conversations=%d", claims, conversations)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := controlplane.Open(f.ctx, controlplane.Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	encoded, _ := json.Marshal(in)
	var original review.SubmitInput
	if err := json.Unmarshal(encoded, &original); err != nil {
		t.Fatal(err)
	}
	replayed, err := third.UpperReviewResults.SubmitReviewResult(f.ctx, original)
	if err != nil || replayed.ReportID != receipt["reportId"] {
		t.Fatalf("old epoch completed receipt replay=%+v err=%v", replayed, err)
	}
}

type failUpperPreparationOnce struct {
	execution.UpperReviewAccess
	failed bool
}

func (p *failUpperPreparationOnce) Prepare(ctx context.Context, target review.UpperTarget) (review.UpperAccess, error) {
	if !p.failed {
		p.failed = true
		return review.UpperAccess{}, errors.New("injected access preparation interruption")
	}
	return p.UpperReviewAccess.Prepare(ctx, target)
}
func (p *failUpperPreparationOnce) RecordPreparationFailure(ctx context.Context, reviewID, runID, message string) error {
	return p.UpperReviewAccess.(interface {
		RecordPreparationFailure(context.Context, string, string, string) error
	}).RecordPreparationFailure(ctx, reviewID, runID, message)
}
func TestUpperReviewPreparationFailureRetainsAdmission(t *testing.T) {
	f := newIntegrationFixture(t)
	gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	area := closureArea(t, f, gpt, sid, "Upper prepare retry")
	args := upperReviewArgs(t, f, sid, area)
	f.plane.Coordinator.UpperReviews = &failUpperPreparationOnce{UpperReviewAccess: f.plane.Coordinator.UpperReviews}
	if text := callToolErrorText(t, f.ctx, local, "task_review_start", args); !strings.Contains(text, "REVIEW_PREPARATION_FAILED") {
		t.Fatalf("preparation error=%s", text)
	}
	var id, runID, worktreeID string
	if err := f.plane.DB.SQL().QueryRow("SELECT review_id,run_id,COALESCE(worktree_id,'') FROM review_grants").Scan(&id, &runID, &worktreeID); err != nil {
		t.Fatal(err)
	}
	access := callToolMap(t, f.ctx, gpt, "review_access_get", map[string]any{"aiSessionId": sid, "reviewId": id})
	if access["state"] != "PREPARING" || access["blocker"] == "" {
		t.Fatalf("failed preparation=%v", access)
	}
	result := callToolMap(t, f.ctx, local, "task_review_start", args)
	if result["review"].(map[string]any)["id"] != id || result["review"].(map[string]any)["runId"] != runID {
		t.Fatal("retry changed admission")
	}
	for _, table := range []string{"reviews", "runs", "workspaces", "worktrees", "feature_area_agent_claims"} {
		var count int
		if err := f.plane.DB.SQL().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	active := callToolMap(t, f.ctx, gpt, "review_access_get", map[string]any{"aiSessionId": sid, "reviewId": id})
	if active["state"] != "ACTIVE" || active["blocker"] != nil {
		t.Fatalf("recovered preparation=%v", active)
	}
}
