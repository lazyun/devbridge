package controlapi_test

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func waitUnknownRequest(t *testing.T, f *upperLifecycleFixture, original agentruntime.Request, terminal bool) agentruntime.Request {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		state, err := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, original.RunID)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range state.Requests {
			if q.ID == original.ID && agentruntime.ResponseDisposition(q) == "UNKNOWN" && (!terminal || state.TurnStatus == "completed") {
				return q
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	state, err := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, original.RunID)
	if err != nil {
		t.Fatalf("missing durable UNKNOWN with required terminal observation: read state: %v", err)
	}
	if state.Observation != nil {
		value, _ := f.plane.Runs.Get(f.ctx, original.RunID)
		t.Fatalf("missing durable UNKNOWN with required terminal observation: turn=%q connection=%q thread=%q reason=%q run=%q failure=%+v", state.TurnStatus, state.Observation.ConnectionState, state.Observation.ThreadStatus, state.Observation.FailureReason, value.Status, value.Failure)
	}
	t.Fatalf("missing durable UNKNOWN with required terminal observation: turn=%q without runtime observation", state.TurnStatus)
	return agentruntime.Request{}
}

func assertUnknownContract(t *testing.T, f *upperLifecycleFixture, q agentruntime.Request, phase task.Phase) {
	t.Helper()
	if q.State != "RESPONDING" || q.UserDecision == nil || q.UserDecision.Action != "RESPOND" {
		t.Fatalf("lost immutable answer/lifecycle: %+v", q)
	}
	p := q.Projection
	if p == nil || p.ResponseDisposition != "UNKNOWN" || p.ReasonCode != "CODEX_RESPONSE_RESOLUTION_READBACK_UNAVAILABLE" || p.ResendAllowed || !reflect.DeepEqual(p.AvailableActions, []string{"CONTINUE_WAITING", "CANCEL_OPERATION", "STOP_AND_REVISE"}) {
		t.Fatalf("unknown projection: %+v", p)
	}
	if len(q.DispatchAttempts) != 1 || q.DispatchAttempts[0].UserDecisionID != q.UserDecision.ID || q.DispatchAttempts[0].RuntimeRequestID != q.ID || !bytes.Equal(q.DispatchAttempts[0].Payload, q.Resolution) {
		t.Fatalf("missing exact durable dispatch intent: %+v", q.DispatchAttempts)
	}
	var attempts, finalizations int
	if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runtime_response_dispatch_attempts WHERE runtime_request_id=?`, q.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempt ledger count=%d err=%v", attempts, err)
	}
	if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM agent_finalization_intents WHERE run_id=?`, q.RunID).Scan(&finalizations); err != nil || finalizations != 0 {
		t.Fatalf("unknown finalization count=%d err=%v", finalizations, err)
	}
	original, _ := json.Marshal(q.UserDecision)
	for i := 0; i < 4; i++ {
		state, err := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, q.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if !agentruntime.HasUnresolvedResponse(state) {
			t.Fatal("read inferred consumption")
		}
		current, err := f.plane.Runs.Get(f.ctx, q.RunID)
		if err != nil || current.Status != run.StatusRunning {
			t.Fatalf("unknown terminalized: %+v %v", current, err)
		}
		if phase != "" {
			currentTask, err := f.facade.GetTask(f.ctx, q.TaskID)
			if err != nil || currentTask.Phase != phase {
				t.Fatalf("gate advanced: %+v %v", currentTask, err)
			}
			claim, err := f.plane.AgentConversations.GetClaim(f.ctx, f.area)
			if err != nil || claim.RunID != q.RunID {
				t.Fatalf("claim released: %+v %v", claim, err)
			}
		}
		read, err := f.facade.RecoverAgentRequestForSession(f.ctx, f.session, controlapi.AgentRequestRecoveryInput{Identity: q.Identity, RuntimeRequestID: q.ID, CommandRequestID: "wait-" + q.ID, Action: "CONTINUE_WAITING"})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, v := range read.Runtime.Requests {
			if v.ID == q.ID {
				found = true
				b, _ := json.Marshal(v.UserDecision)
				if !bytes.Equal(b, original) {
					t.Fatal("waiting changed decision")
				}
			}
		}
		if !found {
			t.Fatal("waiting lost request")
		}
		time.Sleep(20 * time.Millisecond)
	}
	resend := q.UserDecision.UserDecisionInput
	resend.SubmittedBy = ""
	changed := resend
	changed.Identity = q.Identity
	changed.RawText += " changed answer"
	changed.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(changed)
	if _, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, changed); err == nil || controlapi.MapError(err).Code != "REQUEST_CONFLICT" {
		t.Fatalf("changed command payload: %v", err)
	}
	if _, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, resend); err == nil {
		t.Fatal("ordinary resubmission accepted UNKNOWN")
	}
	if _, err := f.facade.ResolveAgentRequestForSession(f.ctx, f.session, agentruntime.Resolution{Identity: q.Identity, RuntimeRequestID: q.ID, RequestID: q.RequestID, ItemID: q.ItemID, UserDecisionID: q.UserDecision.ID, PayloadSHA256: q.UserDecision.PayloadSHA256}); err == nil {
		t.Fatal("ordinary response accepted UNKNOWN")
	}
	var count int
	if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runtime_user_decisions WHERE run_id=?`, q.RunID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("decision count=%d err=%v", count, err)
	}
}

func TestRuntimeUnknownProductionTerminalResultDoesNotAdvance(t *testing.T) {
	for _, tc := range []struct {
		name, turn string
		phase      task.Phase
	}{{"Implementation", "turn-implementation", task.PhaseImplementing}, {"Verification", "turn-verification", task.PhaseVerifying}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", tc.turn)
			t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED", "1")
			runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
				request, _ := waitUserRequest(t, f, f.task, tc.turn)
				if _, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, request, "RESPOND", "yes")); err != nil {
					t.Fatal(err)
				}
				q := waitUnknownRequest(t, f, request, true)
				assertUnknownContract(t, f, q, tc.phase)
				cancelTerminalUnknown(t, f, q)
				data, err := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG"))
				if err != nil || bytes.Count(data, []byte("user-response/"+tc.turn)) != 1 {
					t.Fatalf("response effects: %s %v", data, err)
				}
				return false
			})
		})
	}
}

func TestRuntimeUnknownProductionReviewResultDoesNotAdvance(t *testing.T) {
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-review")
	t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED", "1")
	runCodingReviewFixture(t, func(f *upperLifecycleFixture, taskID, reviewID, runID string) bool {
		request, _ := waitUserRequest(t, f, taskID, "turn-review")
		if _, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, request, "RESPOND", "no")); err != nil {
			t.Fatal(err)
		}
		q := waitUnknownRequest(t, f, request, true)
		assertUnknownContract(t, f, q, task.PhaseReviewing)
		cancelTerminalUnknown(t, f, q)
		return false
	})
}

func TestRuntimeUnknownProductionCancelAndSuccessor(t *testing.T) {
	for _, action := range []string{"CANCEL_OPERATION", "STOP_AND_REVISE"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
			t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED", "1")
			t.Setenv("DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "1")
			runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
				request, _ := waitUserRequest(t, f, f.task, "turn-implementation")
				if _, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, request, "RESPOND", "yes")); err != nil {
					t.Fatal(err)
				}
				q := waitUnknownRequest(t, f, request, false)
				assertUnknownContract(t, f, q, task.PhaseImplementing)
				before, _ := json.Marshal(q.UserDecision)
				out, err := f.facade.RecoverAgentRequestForSession(f.ctx, f.session, controlapi.AgentRequestRecoveryInput{Identity: q.Identity, RuntimeRequestID: q.ID, CommandRequestID: "unknown-cancel-" + q.ID, Action: action})
				if err != nil || out.Cancellation == nil {
					t.Fatalf("canonical cancel %+v %v", out, err)
				}
				deadline := time.Now().Add(15 * time.Second)
				var current run.Run
				for time.Now().Before(deadline) {
					current, err = f.plane.Runs.Get(f.ctx, q.RunID)
					if err != nil {
						t.Fatal(err)
					}
					if current.Status != run.StatusRunning {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				if current.Status == run.StatusRunning || current.AgentRuntime == nil {
					t.Fatalf("cancel did not quiesce %+v", current)
				}
				for _, r := range current.AgentRuntime.Requests {
					if r.ID == q.ID {
						after, _ := json.Marshal(r.UserDecision)
						if !bytes.Equal(before, after) {
							t.Fatal("cancel rewrote answer")
						}
						if r.State != "CANCELED" {
							t.Fatalf("request not canceled: %+v", r)
						}
					}
				}
				data, _ := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG"))
				if bytes.Count(data, []byte("user-response/turn-implementation")) != 1 {
					t.Fatalf("cancel resent answer: %s", data)
				}
				if action == "STOP_AND_REVISE" {
					t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED", "")
					t.Setenv("DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "")
					verifyUserDecisionSuccessor(t, f, q, executionID)
				}
				return false
			})
		})
	}
}

func TestRuntimeUnknownProductionLateExactResolved(t *testing.T) {
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
	t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED", "1")
	signal := t.TempDir() + "/resolved"
	t.Setenv("DEVBRIDGE_CODING_V1_LATE_RESOLVED_FILE", signal)
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		request, _ := waitUserRequest(t, f, f.task, "turn-implementation")
		if _, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, request, "RESPOND", "no")); err != nil {
			t.Fatal(err)
		}
		q := waitUnknownRequest(t, f, request, true)
		assertUnknownContract(t, f, q, task.PhaseImplementing)
		if err := os.WriteFile(signal+".wrong", []byte("wrong identity"), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		q = waitUnknownRequest(t, f, request, true)
		if err := os.WriteFile(signal, []byte("exact receipt"), 0600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			state, err := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, q.RunID)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range state.Requests {
				if v.ID == q.ID && agentruntime.ResponseDisposition(v) == "CONSUMED" {
					if v.State != "RESPONDED" || v.UserDecision.ID != q.UserDecision.ID || len(v.DispatchAttempts) != 1 {
						t.Fatalf("late receipt changed identity/attempt %+v", v)
					}
					return true
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("late exact resolved did not converge")
		return false
	})
}

func TestRuntimeUnknownProductionIndependentSameTextRequests(t *testing.T) {
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
	t.Setenv("DEVBRIDGE_CODING_V1_MULTI_QUESTION", "1")
	t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED", "1")
	t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED_ID", "question-turn-implementation")
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		first, _ := waitUserRequest(t, f, f.task, "turn-implementation")
		deadline := time.Now().Add(10 * time.Second)
		var state agentruntime.Snapshot
		for time.Now().Before(deadline) {
			state, _ = f.facade.AgentRuntimeStateForSession(f.ctx, f.session, first.RunID)
			if len(state.Requests) == 2 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if len(state.Requests) != 2 {
			t.Fatal("missing two independent same-text requests")
		}
		first, second := state.Requests[0], state.Requests[1]
		if _, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, first, "RESPOND", "yes")); err != nil {
			t.Fatal(err)
		}
		first = waitUnknownRequest(t, f, first, false)
		state, _ = f.facade.AgentRuntimeStateForSession(f.ctx, f.session, first.RunID)
		for _, q := range state.Requests {
			if q.ID == second.ID && (q.State != "WAITING_FOR_USER" || q.UserDecision != nil || q.Projection == nil || !reflect.DeepEqual(q.Projection.AvailableActions, []string{"RESPOND", "STOP_AND_REVISE", "CANCEL_OPERATION"})) {
				t.Fatalf("unknown answered otherquestion %+v", q)
			}
		}
		if _, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, second, "RESPOND", "no")); err != nil {
			t.Fatal(err)
		}
		waitUnknownRequest(t, f, first, true)
		deadline = time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			state, _ = f.facade.AgentRuntimeStateForSession(f.ctx, f.session, first.RunID)
			for _, q := range state.Requests {
				if q.ID == second.ID && agentruntime.ResponseDisposition(q) == "CONSUMED" {
					for _, other := range state.Requests {
						if other.ID == first.ID && agentruntime.ResponseDisposition(other) != "UNKNOWN" {
							t.Fatal("resolved consumedwrongrequest")
						}
					}
					return false
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("second exactrequest notresolved")
		return false
	})
}

func TestRuntimeUnknownProductionDifferentRunsRemainIsolated(t *testing.T) {
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
	t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED", "1")
	t.Setenv("DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "1")
	runCodingDeliveryFixture(t, func(first *upperLifecycleFixture, firstExecution string) bool {
		q, _ := waitUserRequest(t, first, first.task, "turn-implementation")
		if _, err := first.facade.SubmitAgentUserDecisionForSession(first.ctx, first.session, explicitDecision(t, first, q, "RESPOND", "yes")); err != nil {
			t.Fatal(err)
		}
		q = waitUnknownRequest(t, first, q, false)
		t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED", "")
		t.Setenv("DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "")
		runCodingDeliveryFixture(t, func(second *upperLifecycleFixture, secondExecution string) bool {
			other, _ := waitUserRequest(t, second, second.task, "turn-implementation")
			if other.RunID == q.RunID || other.ID == q.ID || other.UserDecision != nil {
				t.Fatal("request identity not independent")
			}
			if _, err := second.facade.SubmitAgentUserDecisionForSession(second.ctx, second.session, explicitDecision(t, second, other, "RESPOND", "no")); err != nil {
				t.Fatal(err)
			}
			return true
		})
		q = waitUnknownRequest(t, first, q, false)
		assertUnknownContract(t, first, q, task.PhaseImplementing)
		out, err := first.facade.RecoverAgentRequestForSession(first.ctx, first.session, controlapi.AgentRequestRecoveryInput{Identity: q.Identity, RuntimeRequestID: q.ID, CommandRequestID: "independent-cancel", Action: "CANCEL_OPERATION"})
		if err != nil || out.Cancellation == nil {
			t.Fatalf("cancel: %+v %v", out, err)
		}
		return false
	})
}

func cancelTerminalUnknown(t *testing.T, f *upperLifecycleFixture, q agentruntime.Request) {
	t.Helper()
	before, _ := json.Marshal(q.UserDecision)
	out, err := f.facade.RecoverAgentRequestForSession(f.ctx, f.session, controlapi.AgentRequestRecoveryInput{Identity: q.Identity, RuntimeRequestID: q.ID, CommandRequestID: "terminal-cancel-" + q.ID, Action: "CANCEL_OPERATION"})
	if err != nil || out.Cancellation == nil {
		t.Fatalf("terminal unknown cancellation: %+v %v", out, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		current, e := f.plane.Runs.Get(f.ctx, q.RunID)
		if e != nil {
			t.Fatal(e)
		}
		if current.AgentRuntime != nil {
			for _, r := range current.AgentRuntime.Requests {
				if r.ID == q.ID && r.State == "CANCELED" {
					after, _ := json.Marshal(r.UserDecision)
					if !bytes.Equal(before, after) || len(r.DispatchAttempts) != 1 {
						t.Fatal("cancellation changed answer/attempt")
					}
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("terminal UNKNOWN cancellation failed to quiesce")
}
