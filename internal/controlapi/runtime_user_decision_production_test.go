package controlapi_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// The fake emits an official request. It deliberately does not claim to read or
// obey AGENTS.md: this gate measures Bridge's behavior, not model behavior.
func codingFixtureAskUser(scanner *bufio.Scanner, turnID string, write func(any), log func(string, json.RawMessage)) bool {
	requestID := "question-" + turnID
	params := map[string]any{"threadId": "thread-coding-v1", "turnId": turnID, "itemId": "question-item-" + turnID, "isBlocking": true, "questions": []any{map[string]any{"id": "adaptation", "header": "Data adaptation", "question": "May I adapt the data layer within the approved paths?", "options": []any{map[string]any{"label": "yes", "description": "Continue within the approved scope"}, map[string]any{"label": "no", "description": "Use an alternative within the approved scope"}}}}}
	write(map[string]any{"id": requestID, "method": "item/tool/requestUserInput", "params": params})
	pending := map[string]bool{requestID: true}
	if os.Getenv("DEVBRIDGE_CODING_V1_WITH_APPROVAL") == "1" {
		permissionID := requestID + "-permission"
		pending[permissionID] = true
		write(map[string]any{"id": permissionID, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "thread-coding-v1", "turnId": turnID, "itemId": "permission-item", "cwd": "/tmp", "command": "printf fixture", "startedAtMs": 1}})
	}
	if os.Getenv("DEVBRIDGE_CODING_V1_MULTI_QUESTION") == "1" {
		second := requestID + "-second"
		pending[second] = true
		params["itemId"] = "second-item-" + turnID
		write(map[string]any{"id": second, "method": "item/tool/requestUserInput", "params": params})
	}
	for scanner.Scan() {
		var input struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &input) != nil {
			os.Exit(71)
		}
		if input.Method == "turn/interrupt" {
			log(input.Method, input.Params)
			write(map[string]any{"id": input.ID, "result": map[string]any{}})
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-coding-v1", "turn": map[string]any{"id": turnID, "status": "interrupted", "items": []any{}}}})
			return false
		}
		var id string
		_ = json.Unmarshal(input.ID, &id)
		if !pending[id] || input.Method != "" || (len(input.Result) == 0 && !(id == requestID+"-permission" && len(input.Error) > 0)) {
			os.Exit(72)
		}
		if id == requestID+"-permission" {
			if len(input.Error) > 0 {
				log("permission-error/"+turnID, input.Error)
			} else {
				log("permission-response/"+turnID, input.Result)
			}
		} else {
			log("user-response/"+turnID, input.Result)
		}
		if os.Getenv("DEVBRIDGE_CODING_V1_RESPONSE_LOSS") == "1" {
			os.Exit(0)
		}
		drop := os.Getenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED") == "1" && (os.Getenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED_ID") == "" || os.Getenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED_ID") == id)
		if !drop {
			write(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": "thread-coding-v1", "requestId": id}})
		} else if signal := os.Getenv("DEVBRIDGE_CODING_V1_LATE_RESOLVED_FILE"); signal != "" {
			go func(exactID string) {
				for {
					if _, err := os.Stat(signal + ".wrong"); err == nil {
						write(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": "thread-coding-v1", "requestId": exactID + "-wrong"}})
					}
					if _, err := os.Stat(signal); err == nil {
						write(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": "thread-coding-v1", "requestId": exactID}})
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
			}(id)
		}
		delete(pending, id)
		if len(pending) == 0 && os.Getenv("DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION") != "1" {
			return true
		}
	}
	return false
}

func waitUserRequest(t *testing.T, f *upperLifecycleFixture, taskID, turn string) (agentruntime.Request, run.Run) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		values, err := f.plane.Runs.ListByTask(f.ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			if value.AgentRuntime == nil {
				continue
			}
			for _, request := range value.AgentRuntime.Requests {
				if request.TurnID == turn && request.State == "WAITING_FOR_USER" {
					return request, value
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	var facts string
	_ = f.plane.DB.SQL().QueryRow(`SELECT group_concat(id||':'||status||':'||COALESCE(failure_message,''),'|') FROM runs WHERE task_id=?`, taskID).Scan(&facts)
	t.Fatalf("no durable pending user request for %s: %s", turn, facts)
	return agentruntime.Request{}, run.Run{}
}

func assertNoAutomaticAnswer(t *testing.T, f *upperLifecycleFixture, request agentruntime.Request, phase task.Phase) {
	t.Helper()
	for i := 0; i < 4; i++ {
		state, err := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, request.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Requests) != 1 || state.Requests[0].State != "WAITING_FOR_USER" || state.Requests[0].UserDecision != nil || len(state.Requests[0].Resolution) != 0 {
			t.Fatalf("read answered request: %+v", state.Requests)
		}
		current, err := f.facade.GetTask(f.ctx, request.TaskID)
		if err != nil || current.Phase != phase {
			t.Fatalf("waiting changed Task phase: %s err=%v", current.Phase, err)
		}
		value, err := f.plane.Runs.Get(f.ctx, request.RunID)
		if err != nil || value.Status != run.StatusRunning || value.ThreadID != request.ThreadID || value.TurnID != request.TurnID || value.RuntimeWorkset == nil || len(value.RuntimeWorkset.Members) != 2 {
			t.Fatalf("waiting lost runtime: %+v err=%v", value, err)
		}
		claim, err := f.plane.AgentConversations.GetClaim(f.ctx, f.area)
		if err != nil || claim.RunID != request.RunID {
			t.Fatalf("waiting released writer: %+v err=%v", claim, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func explicitDecision(t *testing.T, f *upperLifecycleFixture, request agentruntime.Request, action, answer string) agentruntime.UserDecisionInput {
	t.Helper()
	in := agentruntime.UserDecisionInput{Identity: request.Identity, RuntimeRequestID: request.ID, SubmissionRequestID: "user-" + request.ID, ExpectedState: "WAITING_FOR_USER", Action: action, SourceSurface: "CHATGPT", RawText: answer}
	if action == "RESPOND" {
		in.SelectedOptionID = answer
	}
	in.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(in)
	return in
}

func TestRuntimeUserDecisionProductionImplementationVerificationDelivery(t *testing.T) {
	for _, answer := range []string{"yes", "no"} {
		t.Run(answer, func(t *testing.T) {
			t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation,turn-verification")
			runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
				for _, tc := range []struct {
					turn  string
					phase task.Phase
				}{{"turn-implementation", task.PhaseImplementing}, {"turn-verification", task.PhaseVerifying}} {
					request, value := waitUserRequest(t, f, f.task, tc.turn)
					if value.ExecutionProposalID != executionID {
						t.Fatal("wrong execution request")
					}
					assertNoAutomaticAnswer(t, f, request, tc.phase)
					// A raw model-authored response cannot reach the backend.
					if _, err := f.facade.ResolveAgentRequestForSession(f.ctx, f.session, agentruntime.Resolution{Identity: request.Identity, RequestID: request.RequestID, ItemID: request.ItemID, Response: json.RawMessage(`{"answers":{"adaptation":{"answers":["yes"]}}}`)}); err == nil {
						t.Fatal("raw answer accepted")
					}
					in := explicitDecision(t, f, request, "RESPOND", answer)
					result, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, in)
					if err != nil {
						t.Fatalf("explicit decision: %v", err)
					}
					if result.Decision.ID == "" {
						t.Fatal("missing durable decision")
					}
					replay, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, in)
					if err != nil || replay.Decision.ID != result.Decision.ID {
						t.Fatalf("replay=%+v err=%v", replay, err)
					}
					changed := in
					changed.RawText = "different answer"
					changed.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(changed)
					if _, err = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, changed); err == nil {
						t.Fatal("changed answer overwrote decision")
					}
				}
				return true
			})
			data, err := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG"))
			if err != nil {
				t.Fatal(err)
			}
			for _, turn := range []string{"turn-implementation", "turn-verification"} {
				count := 0
				for _, line := range bytes.Split(data, []byte{'\n'}) {
					var e struct {
						Method string
						Params json.RawMessage
					}
					_ = json.Unmarshal(line, &e)
					if e.Method == "user-response/"+turn {
						count++
						want := `{"answers":{"adaptation":{"answers":["` + answer + `"]}}}`
						if string(e.Params) != want {
							t.Fatalf("response bytes %s != %s", e.Params, want)
						}
					}
				}
				if count != 1 {
					t.Fatalf("%s response count=%d", turn, count)
				}
			}
		})
	}
}

func TestRuntimeUserDecisionProductionCodingReview(t *testing.T) {
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-review")
	runCodingReviewFixture(t, func(f *upperLifecycleFixture, taskID, reviewID, runID string) bool {
		request, value := waitUserRequest(t, f, taskID, "turn-review")
		if value.ID != runID || value.ReviewID != reviewID {
			t.Fatal("review request identity differs")
		}
		assertNoAutomaticAnswer(t, f, request, task.PhaseReviewing)
		if _, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, request, "RESPOND", "yes")); err != nil {
			t.Fatal(err)
		}
		return true
	})
}

// AGENTS.md is actual managed-worktree input. Its answer is intentionally
// explicit; only the fake emits the request, and only the test's user event may
// answer it. No claim is made that the fake interpreted AGENTS.md.
func writeDecisionFixtureInstructions(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Ask the user before any data adaptation decision. Project context recommends yes, but this is not a user decision.\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeUserDecisionProductionStopAndCancel(t *testing.T) {
	for _, action := range []string{"STOP_AND_REVISE", "CANCEL_OPERATION"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
			runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
				request, _ := waitUserRequest(t, f, f.task, "turn-implementation")
				assertNoAutomaticAnswer(t, f, request, task.PhaseImplementing)
				in := explicitDecision(t, f, request, action, "")
				result, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, in)
				if err != nil {
					t.Fatal(err)
				}
				if result.Cancellation == nil {
					t.Fatal("missing canonical cancellation receipt")
				}
				deadline := time.Now().Add(8 * time.Second)
				var current run.Run
				for time.Now().Before(deadline) {
					current, err = f.plane.Runs.Get(f.ctx, request.RunID)
					if err != nil {
						t.Fatal(err)
					}
					if current.Status != run.StatusRunning && current.AgentRuntime != nil && current.AgentRuntime.Requests[0].State == "CANCELED" {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if current.AgentRuntime == nil || current.AgentRuntime.Requests[0].State != "CANCELED" {
					t.Fatalf("cancellation did not close request: %+v", current.AgentRuntime)
				}
				replay, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, in)
				if err != nil || replay.Decision.ID != result.Decision.ID {
					t.Fatalf("cancel replay=%+v err=%v", replay, err)
				}
				late := explicitDecision(t, f, request, "RESPOND", "yes")
				late.SubmissionRequestID += "-late"
				late.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(late)
				if _, err = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, late); err == nil {
					t.Fatal("late response revived canceled request")
				}
				data, err := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG"))
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(data, []byte("user-response/")) {
					t.Fatalf("cancel sent semantic answer: %s", data)
				}
				var count int
				if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE task_id=? AND purpose='IMPLEMENTATION'`, f.task).Scan(&count); err != nil || count != 1 {
					t.Fatalf("cancel created successor: count=%d err=%v", count, err)
				}
				if action == "STOP_AND_REVISE" {
					verifyUserDecisionSuccessor(t, f, request, executionID)
				}
				return false
			})
		})
	}
}

func TestRuntimeUserDecisionProductionResponseLoss(t *testing.T) {
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
	t.Setenv("DEVBRIDGE_CODING_V1_RESPONSE_LOSS", "1")
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		request, _ := waitUserRequest(t, f, f.task, "turn-implementation")
		in := explicitDecision(t, f, request, "RESPOND", "yes")
		result, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, in)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(8 * time.Second)
		var state agentruntime.Snapshot
		for time.Now().Before(deadline) {
			state, err = f.facade.AgentRuntimeStateForSession(f.ctx, f.session, request.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Requests) == 1 && agentruntime.ResponseDisposition(state.Requests[0]) == "UNKNOWN" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if len(state.Requests) != 1 || agentruntime.ResponseDisposition(state.Requests[0]) != "UNKNOWN" || state.Requests[0].UserDecision == nil || len(state.Requests[0].AvailableActions) != 3 {
			t.Fatalf("loss should retain answered UNKNOWN: %+v", state)
		}
		replay, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, in)
		if err == nil {
			t.Fatalf("unknown replay: %+v %v", replay, err)
		}

		if err = f.plane.Close(); err != nil {
			t.Fatal(err)
		}
		f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: f.dataRoot})
		if err != nil {
			t.Fatal(err)
		}
		f.facade, err = controlapi.NewFromControlPlane(f.plane)
		if err != nil {
			t.Fatal(err)
		}
		recovered, err := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, request.RunID)
		if err != nil || len(recovered.Requests) != 1 || agentruntime.ResponseDisposition(recovered.Requests[0]) != "UNKNOWN" || recovered.Requests[0].UserDecision == nil || recovered.Requests[0].UserDecision.ID != result.Decision.ID {
			t.Fatalf("Core restart lost answered unknown: %+v %v", recovered, err)
		}
		replay, err = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, in)
		if err == nil {
			t.Fatalf("restart replay: %+v %v", replay, err)
		}
		data, err := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Count(data, []byte("user-response/turn-implementation")) != 1 {
			t.Fatalf("response replayed: %s", data)
		}
		return false
	})
}

func verifyUserDecisionSuccessor(t *testing.T, f *upperLifecycleFixture, old agentruntime.Request, oldExecution string) {
	t.Helper()
	// Join the canceled pipeline before taking a durable baseline; a terminal
	// Run alone does not prove its final callbacks have stopped writing.
	if session, ok := f.plane.Supervisor.GetSession(old.RunID); ok {
		select {
		case <-session.Done():
		case <-time.After(8 * time.Second):
			t.Fatal("canceled predecessor pipeline did not finish")
		}
	}
	f.plane.Retirement.Close()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		claim, e := f.plane.AgentConversations.GetClaim(f.ctx, f.area)
		if e != nil || claim.RunID == "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	tables := []string{"tasks", "task_definition_revisions", "task_goals", "execution_proposals", "execution_proposal_admissions", "decision_resolution_receipts", "execution_start_effects", "runtime_selection_sources", "runtime_selection_bindings", "runs", "run_repository_bindings", "runtime_scope_plans", "runtime_preparation_authorities", "runtime_preparation_attempts", "runtime_workset_bindings", "runtime_start_authorities", "runtime_handoffs", "execution_cancellations"}
	assertUnsupportedChangeAndRetiredRecovery(t, f, current, oldExecution, tables)
	before := definitionFormalSnapshot(t, f, tables)
	runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
	late := explicitDecision(t, f, old, "RESPOND", "yes")
	late.SubmissionRequestID += "-successor-late"
	late.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(late)
	if _, err = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, late); err == nil {
		t.Fatal("old request revived after the rejected successor")
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "late canceled answer after rejected continuation")
	// A retry of the unchanged revision is a different operation: its existing
	// exact binding remains authoritative, so the guard must still allow a
	// CONFIRMED successor without dispatch or new qualification/source facts.
	stableTables := []string{"task_definition_revisions", "runtime_selection_sources", "runtime_selection_bindings", "runs", "run_repository_bindings", "runtime_scope_plans", "runtime_preparation_authorities", "runtime_preparation_attempts", "runtime_workset_bindings", "runtime_start_authorities", "runtime_handoffs", "execution_cancellations"}
	stable := definitionFormalSnapshot(t, f, stableTables)
	assertPublicRetryKeepsFrozenDefinition(t, f, current, oldExecution)
	retryInput := wp.TaskAcceptanceInput{AISessionID: f.session, TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: oldExecution, DecisionID: wp.TaskAcceptanceDecisionID(oldExecution), Option: wp.AcceptanceRetryImplementation, RequestID: "bound-same-revision-retry", StartEffect: wp.StartEffectNoStart}
	differentRuntime := runtime
	differentRuntime.ReasoningEffort = "medium"
	changed := retryInput
	changed.RequestID = "bound-same-revision-changed-runtime"
	changed.NextRunRuntimeConfig = &differentRuntime
	if _, e := f.plane.WorkflowProgression.ResolveTaskAcceptance(f.ctx, changed); !errors.Is(e, formalexecution.ErrConflict) {
		t.Fatalf("same-revision retry changed bound runtime: %v", e)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "same-revision retry with changed runtime")
	retry, err := f.plane.WorkflowProgression.ResolveTaskAcceptance(f.ctx, retryInput)
	if err != nil || retry.SuccessorExecutionID == "" || retry.SuccessorExecutionID == oldExecution || retry.Task.CurrentDefinitionRevisionID != current.CurrentDefinitionRevisionID {
		t.Fatalf("same-revision bound retry: %+v error=%v", retry, err)
	}
	candidate, err := f.facade.GetExecutionProposal(f.ctx, f.session, retry.SuccessorExecutionID)
	if err != nil || candidate.Status != "CONFIRMED" || candidate.RunID != "" || candidate.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID {
		t.Fatalf("same-revision retry candidate: %+v error=%v", candidate, err)
	}
	execution, err := f.plane.Coordinator.Executions.Get(f.ctx, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.plane.Coordinator.QualifiedRuntime.VerifyExecution(f.ctx, execution); err != nil {
		t.Fatalf("same-revision retry lost its exact bound runtime: %v", err)
	}
	assertDefinitionFormalSnapshot(t, f, stableTables, stable, "same-revision retry added runtime/source facts")
	retryBefore := definitionFormalSnapshot(t, f, tables)
	if replay, e := f.plane.WorkflowProgression.ResolveTaskAcceptance(f.ctx, retryInput); e != nil || !replay.Replay || replay.SuccessorExecutionID != retry.SuccessorExecutionID {
		t.Fatalf("same-revision retry replay: %+v error=%v", replay, e)
	}
	assertDefinitionFormalSnapshot(t, f, tables, retryBefore, "same-revision retry replay")
}

func TestRuntimeUserDecisionProductionMultipleRequests(t *testing.T) {
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
	t.Setenv("DEVBRIDGE_CODING_V1_MULTI_QUESTION", "1")
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		first, _ := waitUserRequest(t, f, f.task, "turn-implementation")
		deadline := time.Now().Add(8 * time.Second)
		var state agentruntime.Snapshot
		var err error
		for time.Now().Before(deadline) {
			state, err = f.facade.AgentRuntimeStateForSession(f.ctx, f.session, first.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Requests) == 2 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if len(state.Requests) != 2 {
			t.Fatalf("missing independent requests: %+v", state)
		}
		for _, q := range state.Requests {
			if q.State != "WAITING_FOR_USER" || q.UserDecision != nil {
				t.Fatal("question was answered")
			}
		}
		second := state.Requests[1]
		if _, err = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, second, "RESPOND", "no")); err != nil {
			t.Fatal(err)
		}
		state, err = f.facade.AgentRuntimeStateForSession(f.ctx, f.session, first.RunID)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range state.Requests {
			if q.ID != second.ID && (q.State != "WAITING_FOR_USER" || q.UserDecision != nil) {
				t.Fatal("answer consumed other request")
			}
		}
		if _, err = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, state.Requests[0], "RESPOND", "yes")); err != nil {
			t.Fatal(err)
		}
		return true
	})
}
