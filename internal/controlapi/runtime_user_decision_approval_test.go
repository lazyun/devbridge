package controlapi_test

import (
	"bytes"
	"encoding/json"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"os"
	"testing"
	"time"
)

func TestRuntimeUserDecisionProductionApprovalSeparation(t *testing.T) {
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
	t.Setenv("DEVBRIDGE_CODING_V1_WITH_APPROVAL", "1")
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		question, _ := waitUserRequest(t, f, f.task, "turn-implementation")
		deadline := time.Now().Add(8 * time.Second)
		var permission agentruntime.Request
		for time.Now().Before(deadline) {
			state, err := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, question.RunID)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range state.Requests {
				if q.Kind == "RUNTIME_APPROVAL" {
					permission = q
				}
			}
			if permission.ID != "" && permission.State == "REJECTED" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if permission.ID == "" || permission.ID == question.ID || permission.State != "REJECTED" || permission.UserDecision != nil || permission.Delivery != "JSONRPC_ERROR" {
			t.Fatal("approval not independently persisted")
		}
		if _, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, permission, "RESPOND", "yes")); err == nil {
			t.Fatal("permission consumed as semantic decision")
		}
		if _, err := f.facade.ResolveAgentRequestForSession(f.ctx, f.session, agentruntime.Resolution{Identity: question.Identity, ItemID: question.ItemID, RequestID: question.RequestID, Response: json.RawMessage(`{"decision":"accept"}`)}); err == nil {
			t.Fatal("semantic question consumed as permission")
		}
		if _, err := f.facade.ResolveAgentRequestForSession(f.ctx, f.session, agentruntime.Resolution{Identity: permission.Identity, RuntimeRequestID: permission.ID, ExpectedState: "PENDING", PayloadSHA256: permission.PayloadSHA256, ItemID: permission.ItemID, RequestID: permission.RequestID, Response: json.RawMessage(`{"decision":"accept"}`)}); err == nil {
			t.Fatal("sandbox refusal consumed by later approval")
		}
		for i := 0; i < 4; i++ {
			state, err := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, question.RunID)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range state.Requests {
				if q.ID == question.ID && (q.State != "WAITING_FOR_USER" || q.UserDecision != nil) {
					t.Fatal("permission policy answered semantic question")
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		data, err := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("user-response/")) {
			t.Fatal("question auto-answered")
		}
		if _, err = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, question, "RESPOND", "yes")); err != nil {
			t.Fatal(err)
		}
		return true
	})
}
