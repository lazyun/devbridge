package localmcp_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/runanalysis"

	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func reviewApprovalProposal(t *testing.T) string {
	t.Helper()
	b, e := json.Marshal(review.ImplementationResult{ReviewDisposition: review.DispositionApprovalReady, Version: 1, Summary: "Hot-reload Codex runtime model configuration for future Runs; preserve active Runs.", WorkSpec: task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Hot-reload Codex runtime model configuration", Constraints: []string{"Preserve active Run settings"}, CodexInstruction: "Inspect the fixture's tracked.txt without modifying active Run settings.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopePaths, Paths: []string{"tracked.txt"}}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "tracked.txt", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "test -r tracked.txt", Required: true}}, AcceptanceCriteria: []string{"The fixture's tracked.txt remains readable"}, CodexInstruction: "Verify the bounded model configuration proposal."}})
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func awaitApprovalState(t *testing.T, f *integrationFixture, sid, area, state string) wp.Guidance {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for {
		g, e := f.control.WorkflowGuidance(f.ctx, sid, area)
		if e == nil && time.Now().Before(deadline) && (state == "REVIEW_APPROVAL_PENDING" || state == "ACCEPTANCE_PENDING") && g.NextAction.Code == "ANALYZE_RUN_RESULT" {
			submitLocalFixtureAnalysis(t, f, sid, g.RunID)
			continue
		}
		if e == nil && g.CurrentState == state {
			// A Run can publish its phase evidence before the fake supervisor finishes
			// the Execution. Match the existing acceptance eligibility fence as well.
			ready := true
			if state == "ACCEPTANCE_PENDING" {
				execution, err := f.plane.ExecutionProposals.Get(f.ctx, sid, g.ExecutionID)
				ready = err == nil && (execution.Outcome == executionproposal.OutcomeSucceeded || execution.Outcome == executionproposal.OutcomeFailed || execution.Outcome == executionproposal.OutcomeCanceled)
			}
			if ready {
				return g
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("want %s got=%+v err=%v", state, g, e)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// Keep the test's explicit GPT step separate from the fake Codex turn. The
// payload comes from authored fixture data, never the native final message.
func submitLocalFixtureAnalysis(t *testing.T, f *integrationFixture, sid, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var claims int
		if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_agent_claims WHERE run_id=?`, id).Scan(&claims); err != nil {
			t.Fatal(err)
		}
		if claims == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture native worker retained its claim")
		}
		time.Sleep(time.Millisecond)
	}
	source, err := f.control.GetRunAnalysis(f.ctx, sid, id)
	if err != nil {
		t.Fatal(err)
	}
	var report json.RawMessage
	if source.Kind == "REVIEW" {
		switch backend := f.plane.Coordinator.Codex.(type) {
		case *closureFactory:
			report = []byte(backend.message)
		case *continuityBackend:
			report = []byte(backend.message)
		default:
			t.Fatalf("missing explicit GPT fixture for %T", backend)
		}
	} else {
		checks, criteria := []map[string]any{}, []map[string]any{}
		for i := range source.VerificationSpec.Checks {
			checks = append(checks, map[string]any{"index": i, "status": "PASSED", "summary": "Fixture analysis of observed evidence", "evidence": "captured command", "reason": ""})
		}
		for i := range source.VerificationSpec.AcceptanceCriteria {
			criteria = append(criteria, map[string]any{"index": i, "status": "PASSED", "summary": "Fixture criterion met", "evidence": "bounded fixture", "reason": ""})
		}
		report, _ = json.Marshal(map[string]any{"version": 2, "summary": "Separate GPT fixture analysis", "checks": checks, "acceptanceCriteria": criteria})
	}
	if _, err = f.control.SubmitRunAnalysis(f.ctx, runanalysis.Input{AISessionID: sid, RunID: id, RequestID: "fixture-analysis-" + id, SourceHash: source.SourceHash, Report: report}); err != nil {
		t.Fatal(err)
	}
}
func TestReviewApprovalV2CrossAdapterExecutionWithoutSecondApproval(t *testing.T) {
	f := newIntegrationFixture(t)
	backend := &closureFactory{entered: make(chan *closureSession, 4), message: reviewApprovalProposal(t)}
	f.plane.Coordinator.Codex = backend
	gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	created := callToolMap(t, f.ctx, gpt, "featureArea_create", map[string]any{"aiSessionId": sid, "title": "Hot-reload Codex runtime model configuration", "problemStatement": "Use new model settings only for future Runs"})
	area := created["id"].(string)
	// Confirmation creation returns the closed creation receipt. Current
	// workflow guidance is read separately rather than being smuggled into the
	// mutation response.
	if created["id"] == "" {
		t.Fatalf("creation guidance=%+v", created)
	}
	start := callToolMap(t, f.ctx, gpt, "task_review_start", closureTaskReviewArgs(t, f, sid, area))
	if start["currentState"] != "REVIEWING" {
		t.Fatalf("start=%+v", start)
	}
	running := awaitClosure(t, backend)
	g := awaitApprovalState(t, f, sid, area, "REVIEWING")
	count := func() int {
		t.Helper()
		var n int
		if e := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM tasks WHERE feature_area_id=? AND task_type='DEVELOPMENT'`, area).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	if count() != 1 {
		t.Fatal("missing semantic Task before Approval")
	}
	running.finish()
	g = awaitApprovalState(t, f, sid, area, "REVIEW_APPROVAL_PENDING")
	projection, e := f.control.FeatureAreaPresentationForSession(f.ctx, sid, area)
	if e != nil || projection.PendingDecision == nil || projection.PendingDecision.Kind != wp.KindReviewApproval {
		t.Fatalf("Card=%+v err=%v", projection, e)
	}
	args := map[string]any{"aiSessionId": sid, "taskId": g.TaskID, "decisionId": g.DecisionID, "requestId": "v2-user-intent-approval", "option": "APPROVE"}
	reviewValue, e := f.plane.Reviews.Get(f.ctx, g.ReviewID)
	if e != nil {
		t.Fatal(e)
	}
	args["backendSelection"] = reviewValue.BackendSelection.Input()
	args["nextRunRuntimeConfig"] = map[string]any{"model": reviewValue.RuntimeConfig.Model, "reasoningEffort": reviewValue.RuntimeConfig.ReasoningEffort, "codexFastMode": reviewValue.RuntimeConfig.CodexFastMode}
	approved := callToolMap(t, f.ctx, local, "decision_respond", args)
	resolved := approved["reviewApproval"].(map[string]any)
	if resolved["currentState"] != "IMPLEMENTATION_READY" {
		t.Fatalf("approval=%+v", approved)
	}
	taskID := resolved["taskId"].(string)
	if count() != 1 {
		t.Fatal("Approval cardinality")
	}
	// Fresh transport after response loss returns the same receipt and Task.
	replay := callToolMap(t, f.ctx, connectLocalMCP(t, f.libreURL), "decision_respond", args)
	if replay["reviewApproval"].(map[string]any)["taskId"] != taskID || replay["reviewApproval"].(map[string]any)["replay"] != true {
		t.Fatalf("replay=%+v", replay)
	}
	var receipts int
	_ = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM decision_resolution_receipts WHERE project_id=?`, f.projectID).Scan(&receipts)
	if receipts != 1 {
		t.Fatalf("fake second receipt count=%d", receipts)
	}
	projection, e = f.control.FeatureAreaPresentationForSession(f.ctx, sid, area)
	if e != nil || projection.PendingDecision != nil || projection.WorkflowStatus == "TASK_AWAITING_CONFIRMATION" {
		t.Fatalf("second Decision=%+v err=%v", projection, e)
	}
	callToolMap(t, f.ctx, local, "execution_start", map[string]any{"aiSessionId": sid, "taskId": taskID})
	implementation := awaitClosure(t, backend)
	awaitApprovalState(t, f, sid, area, "IMPLEMENTING")
	implementation.finish()
	verification := awaitClosure(t, backend)
	awaitApprovalState(t, f, sid, area, "VERIFYING")
	backend.message = `{"version":2,"summary":"Verification evidence delivered","checks":[{"index":0,"status":"PASSED","summary":"custom check reported complete","evidence":"fixture evidence","reason":""}],"acceptanceCriteria":[{"index":0,"status":"PASSED","summary":"tracked file remains readable","evidence":"fixture evidence","reason":""}]}`
	zero := 0
	backend.commands = []execution.ObservedCommand{{Command: "test -r tracked.txt", ExitCode: &zero, Output: "readable"}}
	verification.finish()
	acceptance := awaitApprovalState(t, f, sid, area, "ACCEPTANCE_PENDING")
	callToolMap(t, f.ctx, local, "decision_respond", map[string]any{"aiSessionId": sid, "decisionId": acceptance.DecisionID, "taskId": acceptance.TaskID, "featureAreaId": acceptance.FeatureAreaID, "executionId": acceptance.ExecutionID, "taskDefinitionRevisionId": acceptance.TaskDefinitionRevisionID, "option": "ACCEPT", "requestId": "v2-user-acceptance"})
	terminal := awaitApprovalState(t, f, sid, area, "SUCCEEDED")
	replay = callToolMap(t, f.ctx, local, "decision_respond", args)
	if replay["reviewApproval"].(map[string]any)["taskId"] != taskID || replay["reviewApproval"].(map[string]any)["replay"] != true {
		t.Fatalf("terminal replay=%+v", replay)
	}
	t.Logf("REAL_CODEX_NOT_RUN: deterministic backend; exact Review=%s Task=%s Execution=%s; %s", g.ReviewID, taskID, g.ExecutionID, terminal.NextAction.Code)
}
