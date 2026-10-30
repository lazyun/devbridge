package localmcp_test

import (
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"testing"
	"time"
)

func TestTaskOwnedCrossAdapterIntentReviewExecutionAcceptance(t *testing.T) {
	f := newIntegrationFixture(t)
	backend := &closureFactory{entered: make(chan *closureSession, 4), message: reviewApprovalProposal(t)}
	f.plane.Coordinator.Codex = backend
	gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	area := closureArea(t, f, gpt, sid, "Runtime model configuration")
	intentArgs := map[string]any{"aiSessionId": sid, "featureAreaId": area, "repositoryId": f.repositoryID, "requestId": "owned-create", "title": "Reload model configuration", "problemStatement": "Apply new configuration only to future Runs", "constraints": []string{"Preserve active Run settings"}}
	intent := callToolMap(t, f.ctx, gpt, "task_create", intentArgs)
	id := intent["id"].(string)
	if intent["id"] == "" {
		t.Fatalf("intent=%+v", intent)
	}
	// Readback of the original confirmation is the GPT replay contract. A
	// Local direct task_create is a separate request identity.
	replay := callToolMap(t, f.ctx, gpt, "task_create", intentArgs)
	if replay["id"] != id {
		t.Fatal("duplicate Task on response loss")
	}
	count := func(table string) int {
		var n int
		if err := f.plane.DB.SQL().QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("reviews") != 0 || count("execution_proposals") != 0 || count("task_definition_revisions") != 1 {
		t.Fatal("intent created child authorization")
	}
	args := closureReviewArgs(sid, area)
	args["taskId"] = id
	config, err := f.plane.CodexConfigs.Get(f.ctx, codexconfig.DefaultConfigID)
	if err != nil {
		t.Fatal(err)
	}
	selection := config.Selection("localmcp-task-owned-fixture")
	args["backend"] = map[string]any{"configId": selection.ConfigID, "configFingerprint": selection.ConfigFingerprint}
	callToolMap(t, f.ctx, local, "task_review_start", args)
	running := awaitClosure(t, backend)
	g := awaitApprovalState(t, f, sid, area, "REVIEWING")
	if g.TaskID != id || g.TaskPhase != task.PhaseReviewing {
		t.Fatalf("review owner=%+v", g)
	}
	running.finish()
	g = awaitApprovalState(t, f, sid, area, "REVIEW_APPROVAL_PENDING")
	projection, err := f.control.FeatureAreaPresentationForSession(f.ctx, sid, area)
	if err != nil || projection.PendingDecision == nil || projection.PendingDecision.Kind != wp.KindReviewApproval || projection.TaskStages == nil || projection.TaskStages.TaskID != id || projection.TaskStages.Title != "Reload model configuration" {
		t.Fatalf("Task Card=%+v %v", projection, err)
	}
	approval := map[string]any{"aiSessionId": sid, "taskId": id, "decisionId": g.DecisionID, "requestId": "owned-approval", "option": "APPROVE"}
	reviewValue, err := f.plane.Reviews.Get(f.ctx, g.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	approval["backendSelection"] = reviewValue.BackendSelection.Input()
	approval["nextRunRuntimeConfig"] = map[string]any{"model": reviewValue.RuntimeConfig.Model, "reasoningEffort": reviewValue.RuntimeConfig.ReasoningEffort, "codexFastMode": reviewValue.RuntimeConfig.CodexFastMode}
	approved := callToolMap(t, f.ctx, local, "review_approval_respond", approval)
	if approved["taskId"] != id || approved["taskPhase"] != "IMPLEMENTATION_READY" || count("tasks") != 1 || count("execution_proposals") != 1 || count("task_definition_revisions") != 2 {
		t.Fatalf("approval did not reserve the current authorized Execution: %+v", approved)
	}
	callToolMap(t, f.ctx, local, "review_approval_respond", approval)
	callToolMap(t, f.ctx, local, "execution_start", map[string]any{"aiSessionId": sid, "taskId": id})
	implementation := awaitClosure(t, backend)
	awaitApprovalState(t, f, sid, area, "IMPLEMENTING")
	callToolMap(t, f.ctx, local, "execution_start", map[string]any{"aiSessionId": sid, "taskId": id})
	if count("execution_proposals") != 1 || count("runs") != 2 {
		t.Fatal("Execution replay allocated another candidate or Run")
	}
	implementation.finish()
	verification := awaitClosure(t, backend)
	awaitApprovalState(t, f, sid, area, "VERIFYING")
	backend.message = `{"version":2,"summary":"Verification evidence delivered","checks":[{"index":0,"status":"PASSED","summary":"custom check reported complete","evidence":"fixture evidence","reason":""}],"acceptanceCriteria":[{"index":0,"status":"PASSED","summary":"tracked file remains readable","evidence":"fixture evidence","reason":""}]}`
	zero := 0
	backend.commands = []execution.ObservedCommand{{Command: "test -r tracked.txt", ExitCode: &zero, Output: "readable"}}
	verification.finish()
	acceptance := awaitApprovalState(t, f, sid, area, "ACCEPTANCE_PENDING")
	callToolMap(t, f.ctx, local, "decision_respond", map[string]any{"aiSessionId": sid, "decisionId": acceptance.DecisionID, "taskId": acceptance.TaskID, "featureAreaId": acceptance.FeatureAreaID, "executionId": acceptance.ExecutionID, "taskDefinitionRevisionId": acceptance.TaskDefinitionRevisionID, "option": "ACCEPT", "requestId": "owned-accept"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		v, e := f.plane.Tasks.Get(f.ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		if v.Status == task.StatusSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Task did not accept: %+v", v)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count("tasks") != 1 || count("reviews") != 1 || count("execution_proposals") != 1 || count("runs") != 3 {
		t.Fatal("wrong ontology cardinality")
	}
}
