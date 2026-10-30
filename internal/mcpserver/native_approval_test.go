package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/lazyun/devbridge/internal/agentruntime"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"
)

type nativeApprovalAdapter struct {
	*unknownAdapterService
	delivered agentruntime.Resolution
}

func (f *nativeApprovalAdapter) ResolveAgentRequestForSession(_ context.Context, _ string, in agentruntime.Resolution) (agentruntime.Snapshot, error) {
	f.responseCalls++
	f.delivered = in
	v := f.snapshot
	v.Requests[0].State = "INTENDED"
	return v, nil
}
func TestNativeApprovalActualSDKKeepsPermissionSeparateAndExact(t *testing.T) {
	q := runtimePayloadRequest()
	q.Kind = "RUNTIME_APPROVAL"
	q.ID = "request"
	q.PayloadSHA256 = strings.Repeat("a", 64)
	q.RuntimeEpoch = "epoch"
	q.OperationKind = "EXECUTION"
	q.OperationID = "execution"
	q.TaskDefinitionRevisionID = "revision"
	q.RequestID = json.RawMessage(`0`)
	f := &nativeApprovalAdapter{unknownAdapterService: &unknownAdapterService{surfaceReadFixture: newSurfaceReadFixture(), snapshot: agentruntime.Snapshot{Identity: q.Identity, Availability: "AVAILABLE", ConnectionState: "CONNECTED", Requests: []agentruntime.Request{q}}}}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "native-approval", Version: "test"}, nil)
	RegisterSurfaceBusinessTools(server, f)
	client := connectPresentationTest(t, context.Background(), server)
	in := agentruntime.Resolution{Identity: q.Identity, RuntimeRequestID: q.ID, RequestID: q.RequestID, ItemID: q.ItemID, PayloadSHA256: q.PayloadSHA256, ExpectedState: "PENDING", Response: json.RawMessage(`{"decision":"accept"}`)}
	args := map[string]any{"kind": "RUNTIME_APPROVAL", "aiSessionId": "session", "resolution": in}
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "runtime_request_respond" {
			if err := resolveSchema(t, tool.InputSchema).Validate(jsonValue(t, args)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, change := range []func(*agentruntime.Resolution){func(v *agentruntime.Resolution) { v.RuntimeRequestID = "foreign" }, func(v *agentruntime.Resolution) { v.PayloadSHA256 = "wrong" }, func(v *agentruntime.Resolution) { v.RuntimeEpoch = "old" }, func(v *agentruntime.Resolution) { v.ExpectedState = "RESOLVED" }, func(v *agentruntime.Resolution) { v.UserDecisionID = "semantic-decision" }} {
		bad := in
		change(&bad)
		_, err := SurfaceRuntimeRequestCore(context.Background(), f, SurfaceRuntimeRequestInput{Kind: "RUNTIME_APPROVAL", AISessionID: "session", Resolution: &bad})
		if err == nil || f.responseCalls != 0 {
			t.Fatal("stale permission reached resolver")
		}
	}
	f.snapshot.Requests[0].Kind = "CODEX_REQUEST"
	if _, err := resolveNativeApproval(context.Background(), f, "session", in); err == nil {
		t.Fatal("semantic question routed as approval")
	}
	f.snapshot.Requests[0].Kind = "RUNTIME_APPROVAL"
	result, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "runtime_request_respond", Arguments: args})
	if err != nil || result.IsError || f.responseCalls != 1 {
		t.Fatalf("%+v %v calls=%d", result, err, f.responseCalls)
	}
	if string(f.delivered.Response) != `{"decision":"accept"}` || f.delivered.UserDecisionID != "" || f.delivered.Identity != q.Identity {
		t.Fatal(f.delivered)
	}
	out := jsonValue(t, result.StructuredContent)
	if out["guarantee"] != "EXACT_RUNTIME_REQUEST_STATE" || out["approval"] != nil {
		t.Fatal(out)
	}
}
