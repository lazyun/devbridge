package codex

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// The coordinator separately tests the exact v1 schemas. This wire test uses
// all v1 names to prove start serialization and cold-client registration, not
// generated-schema assumptions about thread/resume.dynamicTools.
func TestFeatureAreaV1WireStartAndColdResumeRegistersAllHandlers(t *testing.T) {
	names := []string{"repository_search", "repository_read", "repository_list", "report_goal_progress"}
	specs := make([]DynamicToolSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, DynamicToolSpec{Type: "function", Name: name, Description: name, InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	for _, resume := range []bool{false, true} {
		label := "start"
		if resume {
			label = "cold_resume"
		}
		t.Run(label, func(t *testing.T) {
			calls := make(chan string, 4)
			broker := newDynamicToolBroker(func(_ context.Context, p DynamicToolCallParams) (DynamicToolCallResponse, error) {
				calls <- p.Tool
				return DynamicToolCallResponse{Success: true}, nil
			})
			fixture := newRPCFixture(t, func(ctx context.Context, r ServerRequest) (any, error) {
				return broker.process(ctx, r, builtInServerRequestPolicy)
			}, nil)
			client := &Client{rpc: fixture.transport, turns: make(map[turnKey]*turnState), dynamicTools: broker}
			done := make(chan error, 1)
			go func() {
				if resume {
					_, err := client.ResumeThread(context.Background(), ThreadResumeOptions{ThreadID: "feature-thread", CWD: "/new/workspace", DynamicTools: specs})
					done <- err
				} else {
					_, err := client.StartThread(context.Background(), ThreadOptions{CWD: "/original/workspace", DynamicTools: specs})
					done <- err
				}
			}()
			request := fixture.readRequest(t)
			var method string
			_ = json.Unmarshal(request["method"], &method)
			var params map[string]json.RawMessage
			if err := json.Unmarshal(request["params"], &params); err != nil {
				t.Fatal(err)
			}
			if resume {
				if method != "thread/resume" {
					t.Fatal(method)
				}
				if _, exists := params["dynamicTools"]; exists {
					t.Fatal("cold resume resent immutable server-side declarations")
				}
				fixture.write(t, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": "feature-thread"}, "cwd": "/new/workspace"}})
			} else {
				if method != "thread/start" {
					t.Fatal(method)
				}
				var got []DynamicToolSpec
				if err := json.Unmarshal(params["dynamicTools"], &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, specs) {
					t.Fatalf("wire declarations=%+v want=%+v", got, specs)
				}
				fixture.write(t, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": "feature-thread"}}})
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			for _, name := range names {
				fixture.write(t, map[string]any{"id": "call-" + name, "method": "item/tool/call", "params": map[string]any{"callId": "call-" + name, "threadId": "feature-thread", "turnId": "operation-turn", "tool": name, "arguments": map[string]any{}}})
				select {
				case actual := <-calls:
					if actual != name {
						t.Fatalf("handler=%q want=%q", actual, name)
					}
				case <-time.After(time.Second):
					t.Fatalf("%s handler not registered on %s", name, label)
				}
				response := fixture.readRequest(t)
				var result DynamicToolCallResponse
				if err := json.Unmarshal(response["result"], &result); err != nil || !result.Success {
					t.Fatalf("tool response=%s err=%v", response["result"], err)
				}
			}
		})
	}
}
