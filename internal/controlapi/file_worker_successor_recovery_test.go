package controlapi_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Genuine admitted Core bootstrap is typed. After a real close/reopen, all
// recovery/confirmation commands use GPT MCP, and worker IO uses Local MCP.
// Confirmation submissions are deterministic explicit TEST USER choices, not
// assistant authority or evidence of an actual human UI/native model.
func TestFileWorkerExplicitSuccessorAfterRestart(t *testing.T) {
	f, current, old, oldClaim := fileWorkerHTTPFixture(t)
	before := upperClientTrees(t, f)
	member := current.RepositoryTargets[0]
	var root, originalWorktree string
	if err := f.plane.DB.SQL().QueryRow(`SELECT canonical_path,worktree_id FROM direct_file_task_members WHERE workflow_id=? AND member_id=?`, old.WorkflowID, member.MemberID).Scan(&root, &originalWorktree); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "same.txt")
	ctx, local, closeLocal := workerHTTPClient(t, f)
	originalClaim := callUpperMCP(t, ctx, local, "file_worker_claim", mcpObject(t, oldClaim))["result"].(map[string]any)
	args := mcpObject(t, directInput(f, old, member))
	read := callUpperMCP(t, ctx, local, "file_read", args)["result"].(map[string]any)
	args["action"], args["protocolVersion"], args["requestId"], args["expectedDigest"] = "REPLACE", float64(2), "partial-write", read["digest"]
	args["workerClaim"], args["content"] = mcpObject(t, oldClaim.WorkerClaimIdentity), "preserved partial worker evidence\n"
	oldWrite := callUpperMCP(t, ctx, local, "file_write", args)["result"]
	partial, err := snapshotTCPMutation(path)
	if err != nil {
		t.Fatal(err)
	}
	closeLocal()
	reopenDirectDeliveryFixture(t, f)
	ctx, local, _ = workerHTTPClient(t, f)
	gctx, gpt := compositionGPTClient(t, f)
	localCall := func(name string, in any) map[string]any {
		t.Helper()
		return callUpperMCP(t, ctx, local, name, mcpObject(t, in))
	}
	gptCall := func(name string, in any) map[string]any {
		t.Helper()
		return callUpperMCP(t, gctx, gpt, name, mcpObject(t, in))
	}
	decode := func(value any, out any) {
		t.Helper()
		raw, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(raw, out); e != nil {
			t.Fatal(e)
		}
	}
	intent := func(value map[string]any) c.Intent {
		t.Helper()
		var out c.Result
		decode(value, &out)
		if out.Intent == nil {
			t.Fatalf("missing confirmation: %+v", value)
		}
		return *out.Intent
	}
	confirm := func(p c.Intent, request string, selection c.Selection) c.Intent {
		t.Helper()
		got := intent(gptCall("confirmation_commit", c.CommitInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, RequestID: request, ContentHash: p.ContentHash, VariantID: p.Content.Variants[0].ID, Selection: selection}))
		if got.State != "COMPLETED" {
			t.Fatalf("confirmation %s: %+v", p.Action, got)
		}
		return got
	}
	assertOld := func() {
		t.Helper()
		for _, tool := range []string{"file_worker_get", "file_worker_claim"} {
			got := localCall(tool, oldClaim)["result"].(map[string]any)
			if got["state"] != directfile.WorkerOutcomeUnknown || got["executionPayload"] != nil || got["resultReceipt"] != nil || !reflect.DeepEqual(got["receipt"], originalClaim["receipt"]) {
				t.Fatalf("old worker redispatched %s: %+v", tool, got)
			}
		}
	}
	assertOld()
	controls := []string{"direct_file_task_authorizations", "direct_file_worker_claims", "direct_file_operation_receipts"}
	frozen := definitionFormalSnapshot(t, f, controls)
	executionPrepare := func(action, request string, state directfile.State, owner task.Task) c.PrepareInput {
		return c.PrepareInput{AISessionID: f.session, RequestID: request, Purpose: c.Execution, Action: action, Execution: &c.ExecutionBody{Action: action, TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: state.DecisionID, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}, Variants: []c.Variant{{ID: "exact", Label: "Explicit isolated action", Summary: "Explicit isolated test user " + action, ImplementationMode: task.ImplementationDirectFile, RepositoryTargets: owner.RepositoryTargets, WorkSpec: &owner.WorkSpec, VerificationSpec: &owner.VerificationSpec}}}
	}
	recovery := executionPrepare("RECOVER_DIRECT_FILE", "forbidden-same-worker-recovery", old, current)
	failed, e := gpt.CallTool(gctx, &sdkmcp.CallToolParams{Name: "execution_prepare", Arguments: mcpObject(t, recovery)})
	if e != nil {
		t.Fatal(e)
	}
	failureJSON, _ := json.Marshal(failed)
	if !strings.Contains(string(failureJSON), string(directfile.WorkerContinuationUnsupported)) {
		t.Fatalf("wrong recovery refusal: %s", failureJSON)
	}
	if !failed.IsError {
		t.Fatalf("enrolled recovery unexpectedly prepared: %+v", failed)
	}
	if !reflect.DeepEqual(frozen, definitionFormalSnapshot(t, f, controls)) {
		t.Fatal("refused recovery changed old authority")
	}
	cancel := intent(gptCall("execution_prepare", executionPrepare("CANCEL_DIRECT_FILE", "cancel-old-prepare", old, current)))
	confirm(cancel, "cancel-old-confirm", c.Selection{})
	assertOld()
	var closed string
	if err = f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_task_authorizations WHERE workflow_id=?`, old.WorkflowID).Scan(&closed); err != nil || closed != "CLOSED" {
		t.Fatalf("old grant %s %v", closed, err)
	}
	// Copy the capability's observed five-field target from the real GPT projection.
	projection := gptCall("workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
	var observed *wp.TaskReviewRequestTarget
	var find func(any)
	find = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if x["action"] == "REQUEST_TASK_REVIEW" && x["available"] == true {
				if target, ok := x["target"].(map[string]any); ok && target["expectedReviewRequest"] != nil {
					var exact wp.TaskReviewRequestTarget
					decode(target["expectedReviewRequest"], &exact)
					observed = &exact
				}
			}
			for _, child := range x {
				find(child)
			}
		case []any:
			for _, child := range x {
				find(child)
			}
		}
	}
	find(projection)
	if observed == nil {
		t.Fatalf("missing available supplemental capability: %+v", projection)
	}
	supplemental := c.PrepareInput{AISessionID: f.session, RequestID: "successor-supplemental-prepare", Purpose: c.Review, Action: "REQUEST_SUPPLEMENTAL_REVIEW", Review: &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, RepositoryTargets: workset.CloneTargets(f.targets), ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}, SupplementalPurpose: "Review preserved partial worker evidence after explicit authority cancellation; author a separately approved completion plan", ExpectedTarget: observed}, Variants: []c.Variant{{ID: "upper", Label: "Review partial evidence", Summary: "Review existing partial files without resetting", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt in both existing members. Preserve and assess the partial worker evidence. Propose a separately approved Direct File completion; do not modify files."}}}
	supplementalArgs := mcpObject(t, supplemental)
	// The public schema requires explicit empty scalar repositoryId for v2.
	supplementalArgs["review"].(map[string]any)["repositoryId"] = ""
	supplementalArgs["review"].(map[string]any)["reviewPlan"] = map[string]any{"mode": "REPOSITORY"}
	prepared := intent(gptCall("review_prepare", supplementalArgs))
	nextReview := confirm(prepared, "successor-supplemental-confirm", c.Selection{})
	oldReview, oldRun := f.review, f.run
	for _, step := range nextReview.Steps {
		if step.Kind == "START_REVIEW" {
			f.review, f.run = step.ReviewID, step.RunID
		}
	}
	if f.review == oldReview || f.run == oldRun || f.review == "" || f.run == "" {
		t.Fatal("supplemental did not create independent Review/Run")
	}
	access := localCall("review_access_get", map[string]any{"aiSessionId": f.session, "reviewId": f.review})
	f.access = review.UpperAccess{}
	decode(access["result"], &f.access)
	if f.access.State != "ACTIVE" || f.access.Target.ReviewID != f.review || f.access.Target.RunID != f.run {
		t.Fatalf("new review access %+v", f.access)
	}
	rc := review.WorkerClaimInput{UpperTarget: f.access.Target, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "successor-review-claim", WorkerID: "new-review-worker"}}
	localCall("review_worker_claim", rc)
	targets := workset.CloneTargets(f.targets)
	refs := []review.EvidenceRef{}
	checks := []task.VerificationCheck{}
	for i, target := range targets {
		got := localCall("file_read", map[string]any{"reviewTarget": f.access.Target, "repositoryId": target.RepositoryID, "memberId": target.MemberID, "memberVersion": target.MemberVersion, "path": "same.txt"})["result"].(map[string]any)
		if target.MemberID == member.MemberID && (got["content"] != "preserved partial worker evidence\n" || got["digest"] != oldWrite.(map[string]any)["digest"] || got["worktreeId"] != originalWorktree || got["workspaceId"] != old.WorkspaceID) {
			t.Fatalf("new review lost dirty partial file: %+v", got)
		}
		refs = append(refs, review.EvidenceRef{EvidenceID: got["evidenceId"].(string), RepositoryID: target.RepositoryID, MemberID: target.MemberID, Path: "same.txt", Digest: got["digest"].(string)})
		targets[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
		checks = append(checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: target.RepositoryID, RepositoryIDs: []string{target.RepositoryID}, WorkingDirectory: "."})
	}
	now, err := snapshotTCPMutation(path)
	if err != nil || !sameTCPMutation(partial, now) {
		t.Fatalf("supplemental reset or rewrote partial file: %v", err)
	}
	t.Log("reopened Core, refused same-worker recovery, explicitly canceled, and new MCP Review read unchanged dirty partial evidence")
	work := current.WorkSpec
	work.CodexInstruction = "Complete the preserved partial same.txt evidence in the first member; preserve all other bytes."
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "both members", Checks: checks, AcceptanceCriteria: []string{"complete evidence"}, CodexInstruction: "Run the declared checks"}
	report := review.ReviewReport{SchemaVersion: 2, RepositoryTargets: targets, Summary: "Completion plan based on preserved partial evidence", Disposition: review.DispositionApprovalReady, Coverage: []string{"same.txt in both members"}, Findings: []review.Finding{}, Limitations: []string{"read-only review; no commands executed"}, RecommendedNextSteps: []string{"separate approval then separate START"}, EvidenceRefs: refs, WorkSpec: &work, VerificationSpec: &verification}
	localCall("review_result_submit", review.SubmitInput{UpperTarget: f.access.Target, RequestID: "successor-review-result", Report: report, WorkerClaim: &rc.WorkerClaimIdentity})
	approval := c.PrepareInput{AISessionID: f.session, RequestID: "successor-approval-prepare", Purpose: c.Execution, Action: "APPROVE", Execution: exactReviewApprovalBody(t, f.ctx, f.facade, f.task, f.area, f.review, "APPROVE"), Variants: []c.Variant{{ID: "direct", Label: "Approve completion", Summary: "Approve completion only; do not start", ImplementationMode: task.ImplementationDirectFile, RepositoryTargets: targets, WorkSpec: &work, VerificationSpec: &verification}}}
	p := intent(gptCall("execution_prepare", approval))
	confirm(p, "successor-approval-confirm", c.Selection{VerificationWaiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "ISOLATED TEST USER waives command verification; no model or native execution"}})
	if fileClientCount(t, f, "direct_file_task_authorizations") != 1 {
		t.Fatal("approval automatically created successor authority")
	}
	owner, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	if owner.CurrentDefinitionRevisionID == old.TaskDefinitionRevisionID || owner.ReviewApprovalID == old.DecisionID {
		t.Fatal("successor approval reused old revision/decision")
	}
	start := executionPrepare("START_DIRECT_FILE", "successor-start-prepare", directfile.State{DecisionID: owner.ReviewApprovalID}, owner)
	p = intent(gptCall("execution_prepare", start))
	confirm(p, "successor-start-confirm", c.Selection{})
	fresh, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || fresh.State != "ACTIVE" || fresh.WorkflowID == old.WorkflowID {
		t.Fatalf("successor grant %+v %v", fresh, err)
	}
	var successorRoot, successorWorktree string
	if e := f.plane.DB.SQL().QueryRow(`SELECT canonical_path,worktree_id FROM direct_file_task_members WHERE workflow_id=? AND member_id=?`, fresh.WorkflowID, member.MemberID).Scan(&successorRoot, &successorWorktree); e != nil || successorRoot != root || successorWorktree != originalWorktree {
		t.Fatalf("successor changed managed root/worktree: %s %s %v", successorRoot, successorWorktree, e)
	}
	beforeNewWrite, e := snapshotTCPMutation(path)
	if e != nil || !sameTCPMutation(partial, beforeNewWrite) {
		t.Fatal("approval/start changed partial bytes")
	}
	if fresh.WorkspaceID != old.WorkspaceID {
		t.Fatal("successor replaced workspace")
	}
	nc := oldClaim
	nc.WorkerTarget = directfile.WorkerTarget{AISessionID: f.session, FeatureAreaID: f.area, TaskID: f.task, TaskDefinitionRevisionID: fresh.TaskDefinitionRevisionID, DecisionID: fresh.DecisionID, WorkflowID: fresh.WorkflowID, ProtocolVersion: 2, RuntimeEpoch: fresh.RuntimeEpoch, GrantGeneration: fresh.GrantGeneration}
	nc.RequestID = "successor-file-claim"
	nc.WorkerID = "successor-file-worker"
	newClaim := localCall("file_worker_claim", nc)["result"].(map[string]any)
	if newClaim["state"] != directfile.WorkerDispatchIntent || newClaim["executionPayload"] == nil {
		t.Fatalf("new delivery %+v", newClaim)
	}
	replayClaim := localCall("file_worker_claim", nc)["result"].(map[string]any)
	if replayClaim["state"] != directfile.WorkerOutcomeUnknown || replayClaim["executionPayload"] != nil {
		t.Fatal("successor claim redispatched")
	}
	assertOld()
	freshArgs := mcpObject(t, directInput(f, fresh, owner.RepositoryTargets[0]))
	observedFile := localCall("file_read", freshArgs)["result"].(map[string]any)
	freshArgs["action"], freshArgs["protocolVersion"], freshArgs["requestId"], freshArgs["expectedDigest"] = "REPLACE", float64(2), "partial-write", observedFile["digest"]
	freshArgs["content"], freshArgs["workerClaim"] = "completed successor evidence\n", mcpObject(t, oldClaim.WorkerClaimIdentity)
	beforeMixed := definitionFormalSnapshot(t, f, controls)
	denied, denyErr := local.CallTool(ctx, &sdkmcp.CallToolParams{Name: "file_write", Arguments: freshArgs})
	if denyErr != nil {
		t.Fatal(denyErr)
	}
	deniedJSON, _ := json.Marshal(denied)
	if !denied.IsError || !strings.Contains(string(deniedJSON), directfile.NotEnabled) {
		t.Fatalf("wrong mixed-claim refusal: %s", deniedJSON)
	}
	if !reflect.DeepEqual(beforeMixed, definitionFormalSnapshot(t, f, controls)) {
		t.Fatal("mixed old claim changed authority/receipts")
	}
	freshArgs["workerClaim"] = mcpObject(t, nc.WorkerClaimIdentity)
	newWrite := localCall("file_write", freshArgs)["result"]
	if reflect.DeepEqual(newWrite, oldWrite) {
		t.Fatal("new workflow borrowed old mutation receipt")
	}
	completed, err := snapshotTCPMutation(path)
	if err != nil {
		t.Fatal(err)
	}
	if replay := localCall("file_write", args)["result"]; !reflect.DeepEqual(replay, oldWrite) {
		t.Fatal("old completed proof unavailable or changed")
	}
	after, err := snapshotTCPMutation(path)
	if err != nil || !sameTCPMutation(completed, after) {
		t.Fatal("old exact replay overwrote successor file")
	}
	result := fileWorkerResultInput(f, fresh, &nc.WorkerClaimIdentity)
	result.RequestID = "successor-result"
	result.Summary = "Completed preserved partial evidence under separately authorized successor; no commands executed"
	localCall("file_implementation_result_submit", result)
	recorded := localCall("file_worker_get", nc)["result"].(map[string]any)
	if recorded["state"] != directfile.WorkerResultRecorded || recorded["executionPayload"] != nil {
		t.Fatalf("successor result %+v", recorded)
	}
	assertOld()
	if err = f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_task_authorizations WHERE workflow_id=?`, old.WorkflowID).Scan(&closed); err != nil || closed != "CLOSED" {
		t.Fatal("old authority revived")
	}
	for _, workflow := range []string{old.WorkflowID, fresh.WorkflowID} {
		var n int
		if e := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id='partial-write' AND state='COMPLETED'`, workflow).Scan(&n); e != nil || n != 1 {
			t.Fatalf("wrong workflow receipt count %d %v", n, e)
		}
	}
	for table, want := range map[string]int{"tasks": 1, "reviews": 2, "runs": 2, "direct_file_task_authorizations": 2, "direct_file_worker_claims": 2, "direct_file_operation_receipts": 2, "direct_file_implementation_results": 1, "direct_file_implementation_acceptances": 0, "execution_proposals": 0, "task_commit_receipts": 0} {
		if got := fileClientCount(t, f, table); got != want {
			t.Fatalf("%s=%d want%d", table, got, want)
		}
	}
	for key, now := range upperClientTrees(t, f) {
		oldTree := before[key]
		if (key != "managed:"+member.MemberID && now != oldTree) || now.HEAD != oldTree.HEAD {
			t.Fatalf("source/untouched member/HEAD changed %s", key)
		}
	}
	t.Log("same Task/workspace, independent Review/revision/workflow/claim; old receipt stays UNKNOWN, old exact write replay does not overwrite successor, result recorded without acceptance")
}
