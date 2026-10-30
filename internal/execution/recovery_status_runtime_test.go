package execution

import (
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/run"
)

func TestRecoveryStatusRequestAdmissionSeparatesOldUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request agentruntime.Request
		blocked bool
	}{
		{"explicit-question", agentruntime.Request{Kind: "CODEX_REQUEST", State: "WAITING_FOR_USER", Delivery: "NOT_DISPATCHED"}, true},
		{"recorded-not-sent", agentruntime.Request{Kind: "CODEX_REQUEST", State: "USER_DECISION_RECORDED", Delivery: "NOT_DISPATCHED", UserDecision: &agentruntime.UserDecision{UserDecisionInput: agentruntime.UserDecisionInput{Action: "RESPOND"}}}, true},
		{"dispatch-in-flight", agentruntime.Request{Kind: "CODEX_REQUEST", State: "RESPONDING", Delivery: "DISPATCHING", UserDecision: &agentruntime.UserDecision{UserDecisionInput: agentruntime.UserDecisionInput{Action: "RESPOND"}}}, true},
		{"runtime-approval", agentruntime.Request{Kind: "RUNTIME_APPROVAL", State: "PENDING"}, true},
		{"terminal-old-unknown", agentruntime.Request{Kind: "CODEX_REQUEST", State: "RESPONDING", Delivery: "UNKNOWN", UserDecision: &agentruntime.UserDecision{UserDecisionInput: agentruntime.UserDecisionInput{Action: "RESPOND"}}}, false},
		{"unknown-without-decision", agentruntime.Request{Kind: "CODEX_REQUEST", State: "RESPONDING", Delivery: "UNKNOWN"}, true},
		{"consumed", agentruntime.Request{Kind: "CODEX_REQUEST", State: "RESPONDED", Delivery: "CONSUMED", UserDecision: &agentruntime.UserDecision{UserDecisionInput: agentruntime.UserDecisionInput{Action: "RESPOND"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := tc.request
			source := run.Run{AgentRuntime: &agentruntime.Snapshot{Requests: []agentruntime.Request{tc.request}}}
			if got := recoveryPending(source); got != tc.blocked {
				t.Fatalf("blocked=%v want=%v", got, tc.blocked)
			}
			if !reflect.DeepEqual(original, source.AgentRuntime.Requests[0]) {
				t.Fatal("admission rewrote source request")
			}
		})
	}
}
