package localmcp_test

import (
	"encoding/json"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"sync"
	"testing"
)

func gateArgs(sid string, current task.Task, source, option, request string) map[string]any {
	return map[string]any{"kind": "REVIEW_APPROVAL", "aiSessionId": sid, "taskId": current.ID, "decisionId": wp.DecisionID(source), "option": option, "requestId": request}
}

func reviewGateArgs(sid string, current task.Task, source review.Review, option, request string) map[string]any {
	args := gateArgs(sid, current, source.ID, option, request)
	if option == "REVISE" {
		args["reviewFeedback"] = "Re-review the exact formal result and address the unresolved findings."
	}
	if option == "APPROVE" {
		// The Local public schema intentionally excludes the Core-derived service
		// tier. Preserve only the closed client selection fields.
		args["nextRunRuntimeConfig"] = map[string]any{"model": source.RuntimeConfig.Model, "reasoningEffort": source.RuntimeConfig.ReasoningEffort, "codexFastMode": source.RuntimeConfig.CodexFastMode}
		if source.BackendSelection == nil {
			panic("Task-gate fixture omitted frozen backend selection")
		}
		args["backendSelection"] = map[string]any{"configId": source.BackendSelection.ConfigID, "configFingerprint": source.BackendSelection.ConfigFingerprint}
	}
	return args
}

// reviewApprovalConfirmation is the GPT-side replacement for the retired
// decision_respond RPC. REVISE and REJECT use a neutral REVIEW confirmation;
// APPROVE uses an EXECUTION confirmation that copies only the existing
// immutable Review result and selects one current backend/runtime option.
func reviewApprovalConfirmation(t *testing.T, f *integrationFixture, session *sdkmcp.ClientSession, source review.Review, args map[string]any) map[string]any {
	t.Helper()
	option := args["option"].(string)
	sid := args["aiSessionId"].(string)
	decisionID := args["decisionId"].(string)
	reviewID := strings.TrimPrefix(decisionID, string(wp.KindReviewApproval)+":")
	requestID := args["requestId"].(string)
	current, err := f.plane.Tasks.Get(f.ctx, args["taskId"].(string))
	if err != nil {
		t.Fatal(err)
	}
	purpose, tool := "REVIEW", "review_prepare"
	body := map[string]any{
		"taskId":        args["taskId"],
		"featureAreaId": current.FeatureAreaID,
		"reviewId":      reviewID,
		"decisionId":    decisionID,
	}
	if option == "REVISE" {
		body["reviewFeedback"] = args["reviewFeedback"]
	}
	variant := map[string]any{"id": "decision", "label": "Resolve review decision", "summary": "Resolve the exact pending review decision."}
	if option == "APPROVE" {
		recorded, readErr := f.plane.Runs.Get(f.ctx, source.RunID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var proposal review.ImplementationResult
		if err := json.Unmarshal([]byte(recorded.Result.AgentClaim.Message), &proposal); err != nil {
			t.Fatal(err)
		}
		purpose, tool = "EXECUTION", "execution_prepare"
		body["action"] = "APPROVE"
		body["executionId"] = ""
		body["taskDefinitionRevisionId"] = ""
		variant["implementationMode"] = string(proposal.WorkSpec.ImplementationMode)
		variant["workSpec"] = proposal.WorkSpec
		variant["verificationSpec"] = proposal.VerificationSpec
	}
	prepared, err := rawToolMap(t, f.ctx, session, tool, map[string]any{
		"aiSessionId": sid,
		"requestId":   requestID + "-prepare",
		"purpose":     purpose,
		"action":      option,
		map[string]string{"REVIEW": "review", "EXECUTION": "execution"}[purpose]: body,
		"variants": []any{variant},
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, _ := prepared["intent"].(map[string]any)
	if intent == nil {
		t.Fatal("review decision preparation omitted intent")
	}
	// APPROVE is a definition-only v2 operation. Its existing Review snapshot
	// is inherited in Core; this neutral confirmation must not inject a backend
	// or runtime selection.
	selection := map[string]any{}
	committed, err := rawToolMap(t, f.ctx, session, "confirmation_commit", map[string]any{
		"aiSessionId": sid,
		"intentId":    intent["intentId"],
		"version":     intent["version"],
		"purpose":     purpose,
		"action":      option,
		"requestId":   requestID + "-commit",
		"contentHash": intent["contentHash"],
		"variantId":   "decision",
		"selection":   selection,
	})
	if err != nil {
		t.Fatal(err)
	}
	return committed
}
func TestTaskReviewGateCrossAdapterReceiptAndNoSecondAuthorization(t *testing.T) {
	f := newIntegrationFixture(t)
	gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	other := callToolMap(t, f.ctx, local, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	current, source := f.pendingReviewGate(t, sid)
	args := reviewGateArgs(other, current, source, "APPROVE", "cross-approve")
	first := callToolMap(t, f.ctx, local, "review_approval_respond", args)
	replay := callToolMap(t, f.ctx, connectLocalMCP(t, f.libreURL), "review_approval_respond", args)
	if first["taskId"] != current.ID || first["currentState"] != "IMPLEMENTATION_READY" || replay["replay"] != true {
		t.Fatalf("receipt: %+v / %+v", first, replay)
	}
	var tasks, executions, revisions int
	_ = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM tasks`).Scan(&tasks)
	_ = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM execution_proposals`).Scan(&executions)
	_ = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_definition_revisions`).Scan(&revisions)
	if tasks != 1 || executions != 1 || revisions != 2 {
		t.Fatalf("approval cardinality: %d %d %d", tasks, executions, revisions)
	}
	for _, id := range []string{"TASK_CONFIRMATION:old", "EXECUTION_AUTHORIZATION:old"} {
		callToolErrorText(t, f.ctx, local, "decision_respond", map[string]any{"aiSessionId": other, "decisionId": id, "option": "CONFIRM", "requestId": "old"})
	}
}
func TestTaskReviewGateConcurrentResolveExactlyOneRevision(t *testing.T) {
	f := newIntegrationFixture(t)
	gpt := connectGPT(t, f.gptURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	current, source := f.pendingReviewGate(t, sid)
	clients := []*sdkmcp.ClientSession{connectLocalMCP(t, f.libreURL), connectLocalMCP(t, f.libreURL)}
	var wg sync.WaitGroup
	successes := make([]bool, 2)
	for i, c := range clients {
		wg.Add(1)
		go func(i int, c *sdkmcp.ClientSession) {
			defer wg.Done()
			r, e := c.CallTool(f.ctx, &sdkmcp.CallToolParams{Name: "decision_respond", Arguments: reviewGateArgs(sid, current, source, "APPROVE", []string{"one", "two"}[i])})
			successes[i] = e == nil && !r.IsError
		}(i, c)
	}
	wg.Wait()
	if !successes[0] && !successes[1] {
		t.Fatal("both concurrent transitions failed")
	}
	var revisions, executions int
	_ = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_definition_revisions WHERE task_id=?`, current.ID).Scan(&revisions)
	_ = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM execution_proposals`).Scan(&executions)
	if revisions != 2 || executions != 1 {
		t.Fatalf("concurrency: revisions=%d executions=%d", revisions, executions)
	}
}
func TestTaskReviewGateRejectReviseAndFaultRollback(t *testing.T) {
	for _, option := range []string{"REJECT", "REVISE", "FAULT"} {
		t.Run(option, func(t *testing.T) {
			f := newIntegrationFixture(t)
			gpt := connectGPT(t, f.gptURL)
			sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
			current, source := f.pendingReviewGate(t, sid)
			decision := option
			if option == "FAULT" {
				decision = "APPROVE"
				if _, e := f.plane.DB.SQL().Exec(`CREATE TRIGGER fail_gate_receipt BEFORE INSERT ON review_approval_receipt_context BEGIN SELECT RAISE(ABORT,'fixture receipt failure'); END`); e != nil {
					t.Fatal(e)
				}
			}
			args := reviewGateArgs(sid, current, source, decision, "resolve")
			if option == "FAULT" {
				callToolErrorText(t, f.ctx, connectLocalMCP(t, f.libreURL), "review_approval_respond", args)
			} else {
				// REJECT/REVISE have a neutral GPT confirmation variant.  The
				// adapter commits that exact intent; it never revives decision_respond.
				reviewApprovalConfirmation(t, f, gpt, source, args)
				reviewApprovalConfirmation(t, f, connectGPT(t, f.gptURL), source, args)
			}
			got, e := f.plane.Tasks.Get(f.ctx, current.ID)
			if e != nil {
				t.Fatal(e)
			}
			if option == "REJECT" && got.Status != task.StatusCanceled || option == "REVISE" && (got.Phase != task.PhaseReviewing || got.CurrentReviewID == source.ID) || option == "FAULT" && got.Phase != task.PhaseReviewApprovalPending {
				t.Fatalf("transition: %+v", got)
			}
			if got.CurrentDefinitionRevisionID != current.CurrentDefinitionRevisionID || got.ReviewApprovalID != "" || got.CurrentExecutionID != "" {
				t.Fatalf("partial authorization: %+v", got)
			}
		})
	}
}
func TestTaskReviewGateRequestCollisionAndFreshTransportReadback(t *testing.T) {
	f := newIntegrationFixture(t)
	gpt := connectGPT(t, f.gptURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	a, ra := f.pendingReviewGate(t, sid)
	b, rb := f.pendingReviewGate(t, sid)
	first := callToolMap(t, f.ctx, connectLocalMCP(t, f.libreURL), "review_approval_respond", reviewGateArgs(sid, a, ra, "APPROVE", "collision"))
	// A same requestId on a distinct confirmation payload is rejected before
	// its own Review gate can mutate.
	callToolErrorText(t, f.ctx, connectLocalMCP(t, f.libreURL), "review_approval_respond", reviewGateArgs(sid, b, rb, "APPROVE", "collision"))
	unchanged, e := f.plane.Tasks.Get(f.ctx, b.ID)
	if e != nil || unchanged.Phase != task.PhaseReviewApprovalPending || unchanged.CurrentDefinitionRevisionID != b.CurrentDefinitionRevisionID || unchanged.ReviewApprovalID != "" || unchanged.CurrentExecutionID != "" {
		t.Fatalf("collision mutated B: %+v %v", unchanged, e)
	}
	repeat := callToolMap(t, f.ctx, connectLocalMCP(t, f.libreURL), "review_approval_respond", reviewGateArgs(sid, a, ra, "APPROVE", "collision"))
	if repeat["taskId"] != first["taskId"] || repeat["replay"] != true {
		t.Fatal(repeat)
	}
	callToolMap(t, f.ctx, connectLocalMCP(t, f.libreURL), "review_approval_respond", reviewGateArgs(sid, b, rb, "APPROVE", "collision-rotated"))
	// An optional Review identity is an assertion, never permission to select history.
	args := reviewGateArgs(sid, b, rb, "APPROVE", "history")
	args["reviewId"] = ra.ID
	text := callToolErrorText(t, f.ctx, connectLocalMCP(t, f.libreURL), "review_approval_respond", args)
	if !strings.Contains(text, "STALE_REVIEW_APPROVAL") {
		t.Fatal(text)
	}
	data, _ := json.Marshal(first)
	if len(data) == 0 {
		t.Fatal("missing durable receipt")
	}
}

func TestTaskCreateReplaySurvivesChangedRuntimePrerequisites(t *testing.T) {
	f := newIntegrationFixture(t)
	gpt := connectGPT(t, f.gptURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	area := closureArea(t, f, gpt, sid, "Replay intent")
	args := map[string]any{"aiSessionId": sid, "featureAreaId": area, "repositoryId": f.repositoryID, "requestId": "create-stable", "title": "Durable intent", "problemStatement": "Preserve the original intent."}
	first := callToolMap(t, f.ctx, gpt, "task_create", args)
	if _, e := f.plane.DB.SQL().Exec(`UPDATE codex_configs SET enabled=0`); e != nil {
		t.Fatal(e)
	}
	// GPT confirmation replays its exact prepared/commit receipt; a Local
	// direct submission is a different public command and must not be treated
	// as the original confirmation request.
	replay := callToolMap(t, f.ctx, gpt, "task_create", args)
	if replay["id"] != first["id"] {
		t.Fatal(replay)
	}
	args["title"] = "Changed intent"
	callToolErrorText(t, f.ctx, gpt, "task_create", args)
	var tasks, reviews, revisions, executions int
	_ = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM tasks`).Scan(&tasks)
	_ = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM reviews`).Scan(&reviews)
	_ = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_definition_revisions`).Scan(&revisions)
	_ = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM execution_proposals`).Scan(&executions)
	if tasks != 1 || reviews != 0 || revisions != 1 || executions != 0 {
		t.Fatalf("intent replay created authorization: %d %d %d %d", tasks, reviews, revisions, executions)
	}
}
