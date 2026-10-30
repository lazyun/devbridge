package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// This adapter fixture tests actual SDK dispatch and scope admission. Canonical
// SQLite cancellation/reopen is covered by controlapi production fixtures.
type unknownAdapterService struct {
	*surfaceReadFixture
	snapshot      agentruntime.Snapshot
	actions       []string
	responseCalls int
}

func (f *unknownAdapterService) InitializeSession(_ context.Context, _ aiprojectselection.ClientBindingReference, _ string, id string) (aicontext.InitializationResult, error) {
	if id != "session" {
		return aicontext.InitializationResult{}, errors.New("wrong session")
	}
	p := "project"
	return aicontext.InitializationResult{AISessionID: id, State: "PROJECT_BOUND", BoundProjectID: &p}, nil
}
func (f *unknownAdapterService) BindProject(context.Context, aiprojectselection.ClientBindingReference, string, string, string) (aicontext.ProjectBindingResult, error) {
	panic("read must not bind")
}
func (f *unknownAdapterService) AgentRuntimeStateForSession(_ context.Context, s, r string) (agentruntime.Snapshot, error) {
	if s != "session" || r != "run" {
		return agentruntime.Snapshot{}, errors.New("scope mismatch")
	}
	return f.snapshot, nil
}
func (f *unknownAdapterService) ResolveAgentRequestForSession(_ context.Context, s string, in agentruntime.Resolution) (agentruntime.Snapshot, error) {
	f.responseCalls++
	if s != "session" || in.Identity != f.snapshot.Identity || in.RuntimeRequestID != "request" {
		return agentruntime.Snapshot{}, errors.New("scope mismatch")
	}
	return f.snapshot, controlapi.MapError(agentruntime.ErrResponseDispositionUnknown)
}
func (f *unknownAdapterService) RecoverAgentRequestForSession(_ context.Context, s string, in controlapi.AgentRequestRecoveryInput) (controlapi.AgentRequestRecoveryResult, error) {
	if s != "session" || in.Identity != f.snapshot.Identity || in.RuntimeRequestID != "request" {
		return controlapi.AgentRequestRecoveryResult{}, errors.New("scope mismatch")
	}
	f.actions = append(f.actions, in.Action)
	return controlapi.AgentRequestRecoveryResult{Runtime: f.snapshot, SuccessorRequiresConfirmation: in.Action == "STOP_AND_REVISE"}, nil
}
func TestUnknownDispositionActualSDKAdapters(t *testing.T) {
	q := runtimePayloadRequest()
	q.ID = "request"
	q.Kind = "CODEX_REQUEST"
	q.OperationKind = "REVIEW"
	q.OperationID = "review"
	q.RuntimeEpoch = "epoch"
	q.State = "RESPONDING"
	q.Delivery = "UNKNOWN"
	q.UserDecision = &agentruntime.UserDecision{ID: "decision", UserDecisionInput: agentruntime.UserDecisionInput{Identity: q.Identity, Action: "RESPOND", RawText: "原始答案", PayloadSHA256: "original-hash"}}
	f := &unknownAdapterService{surfaceReadFixture: newSurfaceReadFixture(), snapshot: agentruntime.Snapshot{Identity: q.Identity, Availability: "AVAILABLE", Requests: []agentruntime.Request{q}}}
	agentruntime.ApplyRequestProjections(&f.snapshot)
	f.review = review.Review{ID: "review", ProjectID: "project", FeatureAreaID: "area", TaskID: "task", RunID: "run"}
	f.run = run.Run{ID: "run", ProjectID: "project", FeatureAreaID: "area", TaskID: "task", ReviewID: "review", OperationKind: run.OperationReview, OperationID: "review", AgentRuntime: &f.snapshot}
	before, _ := json.Marshal(f.snapshot)
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "unknown-contract", Version: "1"}, nil)
	RegisterSurfaceBusinessTools(server, f)
	client := connectPresentationTest(t, context.Background(), server)
	call := func(name string, args any) *sdkmcp.CallToolResult {
		t.Helper()
		out, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	read := func() {
		t.Helper()
		out := call("operation_get", map[string]any{"aiSessionId": "session", "target": map[string]any{"kind": "REVIEW", "operationId": "review", "ownerTaskId": "task", "runId": "run"}, "include": []string{"RUNTIME_REQUESTS"}})
		if out.IsError {
			t.Fatalf("read %+v", out)
		}
		b, _ := json.Marshal(out.StructuredContent)
		var value struct {
			Result struct {
				RuntimeRequests []agentruntime.Request `json:"runtimeRequests"`
			} `json:"result"`
		}
		if err := json.Unmarshal(b, &value); err != nil {
			t.Fatal(err)
		}
		if len(value.Result.RuntimeRequests) != 1 {
			t.Fatalf("missing request %s", b)
		}
		p := value.Result.RuntimeRequests[0].Projection
		if p == nil || p.ResponseDisposition != "UNKNOWN" || p.ResendAllowed || p.UserDecisionSummary != "原始答案" || !reflect.DeepEqual(p.AvailableActions, []string{"CONTINUE_WAITING", "CANCEL_OPERATION", "STOP_AND_REVISE"}) {
			t.Fatalf("projection %+v", p)
		}
	}
	read()
	resolutionRaw, _ := json.Marshal(agentruntime.Resolution{Identity: q.Identity, RuntimeRequestID: q.ID, UserDecisionID: "decision", ExpectedState: "RESPONDING", PayloadSHA256: "original-hash", RequestID: q.RequestID, ItemID: q.ItemID})
	var resolution map[string]any
	_ = json.Unmarshal(resolutionRaw, &resolution)
	delete(resolution, "response")
	out := call("runtime_request_respond", map[string]any{"kind": "CODEX_REQUEST", "aiSessionId": "session", "resolution": resolution})
	raw, _ := json.Marshal(out)
	if !out.IsError || !strings.Contains(string(raw), "RESPONSE_DISPOSITION_UNKNOWN") || f.responseCalls != 1 {
		t.Fatalf("rejection %s calls=%d", raw, f.responseCalls)
	}
	for _, action := range []string{"CONTINUE_WAITING", "CANCEL_OPERATION", "STOP_AND_REVISE"} {
		out := call("runtime_request_recovery", AgentRequestRecoveryInput{AISessionID: "session", Recovery: controlapi.AgentRequestRecoveryInput{Identity: q.Identity, RuntimeRequestID: q.ID, CommandRequestID: "command-" + action, Action: action}})
		if out.IsError {
			t.Fatalf("recovery %+v", out)
		}
		read()
	}
	if !reflect.DeepEqual(f.actions, []string{"CONTINUE_WAITING", "CANCEL_OPERATION", "STOP_AND_REVISE"}) {
		t.Fatalf("actions %v", f.actions)
	}
	// A rejected scope cannot mutate the original snapshot or trigger a response.
	wrong, wrongErr := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "runtime_request_recovery", Arguments: AgentRequestRecoveryInput{AISessionID: "wrong-session", Recovery: controlapi.AgentRequestRecoveryInput{Identity: q.Identity, RuntimeRequestID: q.ID, CommandRequestID: "wrong", Action: "CONTINUE_WAITING"}}})
	if wrongErr == nil && (wrong == nil || !wrong.IsError) {
		t.Fatal("scope bypass")
	}
	read()
	after, _ := json.Marshal(f.snapshot)
	if !reflect.DeepEqual(before, after) || f.responseCalls != 1 {
		t.Fatal("adapter read/recovery changed answer or resent")
	}
}
