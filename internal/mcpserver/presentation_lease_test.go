package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"reflect"
	"sync"
	"testing"
)

func TestCurrentPresentationRetriesRetainCompleteIdentity(t *testing.T) {
	leases := newPresentationLeases()
	calls := 0
	fail := false
	next := func(context.Context, string, sdkmcp.Request) (sdkmcp.Result, error) {
		calls++
		if fail {
			return nil, errors.New("read unavailable")
		}
		return &sdkmcp.CallToolResult{StructuredContent: map[string]any{"aiSessionId": "session", "projectId": "project", "entryIdentity": "entry:first", "workspaceIdentity": "workspace:session:project", "display": "COLLAPSED"}, Meta: sdkmcp.Meta{"workspace": map[string]any{"selectedTaskId": "task"}}, Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "exact result"}}}, nil
	}
	call := func(args string) (*sdkmcp.CallToolResult, error) {
		out, err := leases.emit(context.Background(), next, "tools/call", nil, &sdkmcp.CallToolParamsRaw{Name: "workspace_show", Arguments: json.RawMessage(args)})
		if err != nil {
			return nil, err
		}
		return out.(*sdkmcp.CallToolResult), nil
	}
	first, err := call(`{"aiSessionId":"session","presentationRequestId":"one"}`)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := call(`{"presentationRequestId":"one","display":"COLLAPSED","aiSessionId":"session"}`)
	if err != nil || calls != 2 || !reflect.DeepEqual(first, replay) {
		t.Fatalf("complete replay lost: first=%+v replay=%+v calls=%d err=%v", first, replay, calls, err)
	}
	if replay.Meta["openai/outputTemplate"] != nil || replay.Meta["ui"] != nil {
		t.Fatal("presentation replay must use the descriptor template, not redefine it in result metadata", replay.Meta)
	}
	if replay.Meta["hostVisibility"] != "UNCONFIRMED" || replay.Meta["presentationOperation"] != "MOUNT_REQUESTED" {
		t.Fatal("claimed host success", replay.Meta)
	}
	if _, err := call(`{"aiSessionId":"session","presentationRequestId":"one","display":"EXPANDED"}`); err == nil {
		t.Fatal("changed intent accepted")
	}
	later, err := call(`{"aiSessionId":"session","presentationRequestId":"two"}`)
	if err != nil || calls != 3 || later.Meta["emission"] != "MAIN" {
		t.Fatalf("later reply suppressed: %v %v", later, err)
	}
	fail = true
	if _, err := call(`{"aiSessionId":"session","presentationRequestId":"failed"}`); err == nil {
		t.Fatal("failed read succeeded")
	}
	fail = false
	if _, err := call(`{"aiSessionId":"session","presentationRequestId":"failed"}`); err != nil {
		t.Fatal("failed read poisoned retry", err)
	}
	if _, err := call(`{"aiSessionId":"session"}`); err == nil {
		t.Fatal("missing presentation request accepted")
	}
}
func TestConcurrentPresentationRetryRefreshesAndReturnsCompleteData(t *testing.T) {
	leases := newPresentationLeases()
	var wg sync.WaitGroup
	calls := 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := leases.emit(context.Background(), func(context.Context, string, sdkmcp.Request) (sdkmcp.Result, error) {
				calls++
				return &sdkmcp.CallToolResult{StructuredContent: map[string]any{"entryIdentity": "entry:one", "aiSessionId": "session"}}, nil
			}, "tools/call", nil, &sdkmcp.CallToolParamsRaw{Name: "workspace_show", Arguments: json.RawMessage(`{"aiSessionId":"session","presentationRequestId":"one"}`)})
			if err != nil {
				t.Error(err)
				return
			}
			value := out.(*sdkmcp.CallToolResult)
			if value.StructuredContent.(map[string]any)["entryIdentity"] != "entry:one" {
				t.Error("retry identity lost")
			}
		}()
	}
	wg.Wait()
	if calls != 12 {
		t.Fatalf("retry performed %d reads", calls)
	}
}

func TestPresentationRetryReadsCurrentAuthorityWithPinnedScope(t *testing.T) {
	ctx := context.Background()
	leases := newPresentationLeases()
	phase := "REVIEW_APPROVAL_PENDING"
	var observed map[string]any
	reader := func(ctx context.Context, _ string, request sdkmcp.Request) (sdkmcp.Result, error) {
		if err := json.Unmarshal(request.GetParams().(*sdkmcp.CallToolParamsRaw).Arguments, &observed); err != nil {
			t.Fatal(err)
		}
		if phase == "SUCCEEDED" && !presentationReplayExact(ctx) {
			t.Error("retry did not request exact selection")
		}
		return &sdkmcp.CallToolResult{StructuredContent: map[string]any{"phase": phase}, Meta: sdkmcp.Meta{"projectId": "project", "taskId": "original-task", "featureAreaId": "original-area", "entryIdentity": "entry:one", "workspace": map[string]any{"phase": phase}}}, nil
	}
	call := func() *sdkmcp.CallToolResult {
		params := &sdkmcp.CallToolParamsRaw{Name: "workspace_show", Arguments: json.RawMessage(`{"aiSessionId":"session","presentationRequestId":"one"}`)}
		request := &sdkmcp.CallToolRequest{Params: params}
		result, err := leases.emit(ctx, reader, "tools/call", request, params)
		if err != nil {
			t.Fatal(err)
		}
		if string(params.Arguments) != `{"aiSessionId":"session","presentationRequestId":"one"}` {
			t.Fatal("mutated caller request")
		}
		return result.(*sdkmcp.CallToolResult)
	}
	first := call()
	phase = "SUCCEEDED"
	retry := call()
	if retry.Meta["entryIdentity"] != first.Meta["entryIdentity"] || retry.Meta["workspace"].(map[string]any)["phase"] != "SUCCEEDED" {
		t.Fatal("retry lost identity or replayed stale authority", retry)
	}
	if observed["taskId"] != "original-task" || observed["featureAreaId"] != "original-area" || observed["projectId"] != "project" {
		t.Fatal("retry drifted from original selected scope", observed)
	}
}

func TestPresentationRetryActualSDKRefreshesWorkspaceFacts(t *testing.T) {
	ctx := context.Background()
	service := exactPresentationFixture()
	service.taskProjection.CurrentState = "REVIEW_APPROVAL_PENDING"
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "fresh-presentation-retry", Version: "1"}, nil)
	server.AddReceivingMiddleware(presentationResultMiddleware(service))
	RegisterSurfacePresentationTools(server, service)
	client := connectPresentationTest(t, ctx, server)
	args := map[string]any{"aiSessionId": "session", "taskId": "task", "presentationRequestId": "same-intent"}
	call := func() *sdkmcp.CallToolResult {
		out, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: "workspace_show", Arguments: args})
		if err != nil || out.IsError {
			t.Fatalf("%+v %v", out, err)
		}
		return out
	}
	first := call()
	service.taskProjection.CurrentState = "SUCCEEDED"
	retry := call()
	if retry.Meta["entryIdentity"] != first.Meta["entryIdentity"] {
		t.Fatal("same intent lost entry identity")
	}
	workspace := jsonValue(t, retry.Meta["workspace"])
	if workspace["presentation"].(map[string]any)["currentState"] != "SUCCEEDED" {
		t.Fatal("SDK retry served stale business snapshot", workspace)
	}
}
